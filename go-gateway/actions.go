package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"unicode/utf8"
)

// Действия над письмом — общий слой для Telegram-кнопок и HTTP API.
// Каждое действие проверяет и меняет статус одним UPDATE (transitionStatus), а долгую работу —
// агент, отправку, календарь — запускает в фоне. Чем кончилось, узнаёт Notifier (Telegram),
// а приложение видит по статусу письма.

// Notifier — кому сообщать о результатах фоновой работы. Сейчас это Telegram-бот.
type Notifier interface {
	DraftReady(e StoredEmail, problems string)
	DraftFailed(e StoredEmail, reason string)
	Sent(e StoredEmail)
	SendFailed(e StoredEmail, err error)
	CalendarAdded(e StoredEmail, created createdEvent)
	CalendarFailed(e StoredEmail, err error)
}

// App — всё, что нужно действиям. Пустые поля значат «не настроено».
type App struct {
	AgentURL string
	Mailer   ReplySender
	Calendar *CalendarClient
	Notify   Notifier
	Events   *EventHub // лента для приложения: смена статусов попадает туда сразу

	wg sync.WaitGroup // фоновые задачи — чтобы тесты могли их дождаться
}

var (
	ErrEmailNotFound     = errors.New("письмо не найдено")
	ErrNoMeeting         = errors.New("в письме нет встречи с временем")
	ErrAlreadyInCalendar = errors.New("уже в календаре")
	ErrBadDraft          = errors.New("черновик пустой или длиннее 5000 символов")
)

// ErrWrongStatus — действие не подходит к текущему статусу: например, «Отправить» до черновика или повторное нажатие
type ErrWrongStatus struct{ Status string }

func (e ErrWrongStatus) Error() string {
	return "сейчас нельзя: письмо в статусе " + e.Status
}

// ErrNotConfigured — для действия не хватает настройки (почта, календарь)
type ErrNotConfigured struct{ What string }

func (e ErrNotConfigured) Error() string { return e.What + " не настроен(а)" }

// transition меняет статус, только если сейчас он один из from, и возвращает письмо уже в новом статусе
func (a *App) transition(uid uint32, to string, from ...string) (StoredEmail, error) {
	if _, ok := getEmail(uid); !ok {
		return StoredEmail{}, ErrEmailNotFound
	}
	if !transitionStatus(uid, to, from...) {
		cur, _ := getEmail(uid)
		return cur, ErrWrongStatus{cur.Status}
	}

	e, _ := getEmail(uid)
	log.Printf("👆 UID %d → %s", uid, to)
	a.statusChanged(e)
	return e, nil
}

// statusChanged — сообщить приложению, что письмо поменялось (кнопка нажата здесь, в Telegram или с другого устройства)
func (a *App) statusChanged(e StoredEmail) {
	if a.Events != nil {
		a.Events.StatusChanged(e)
	}
}

// background запускает долгую работу, не задерживая ответ кнопке или API
func (a *App) background(f func()) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		f()
	}()
}

// Wait ждёт все фоновые задачи (нужно тестам)
func (a *App) Wait() { a.wg.Wait() }

// --- Действия ---

func (a *App) Confirm(uid uint32) (StoredEmail, error) {
	e, err := a.transition(uid, "drafting", "pending")
	if err == nil {
		a.background(func() { a.draft(e) })
	}
	return e, err
}

func (a *App) Reject(uid uint32) (StoredEmail, error) {
	return a.transition(uid, "rejected", "pending")
}

func (a *App) Redraft(uid uint32) (StoredEmail, error) {
	e, err := a.transition(uid, "drafting", "draft_ready", "draft_failed")
	if err == nil {
		a.background(func() { a.draft(e) })
	}
	return e, err
}

func (a *App) Skip(uid uint32) (StoredEmail, error) {
	return a.transition(uid, "skipped", "draft_ready", "draft_failed")
}

// NotSpam — защита от бомбера ошиблась: письмо возвращается в «ждут решения».
// Пометка «подозрительное» остаётся — главной кнопкой будет «Отклонить», а ответить можно осознанно.
func (a *App) NotSpam(uid uint32) (StoredEmail, error) {
	return a.transition(uid, "pending", "spam")
}

func (a *App) Send(uid uint32) (StoredEmail, error) {
	if a.Mailer == nil {
		return StoredEmail{}, ErrNotConfigured{"отправка почты"}
	}
	e, err := a.transition(uid, "sending", "draft_ready")
	if err == nil {
		a.background(func() { a.send(e) })
	}
	return e, err
}

func (a *App) AddToCalendar(uid uint32) (StoredEmail, error) {
	if a.Calendar == nil {
		return StoredEmail{}, ErrNotConfigured{"календарь (нет CALENDAR_API_KEY в .env)"}
	}
	e, ok := getEmail(uid)
	if !ok {
		return StoredEmail{}, ErrEmailNotFound
	}
	if e.MeetingStart == "" {
		return e, ErrNoMeeting
	}
	if !claimCalendarAdd(uid) {
		return e, ErrAlreadyInCalendar
	}

	log.Printf("👆 UID %d → в календарь", uid)
	a.background(func() { a.addToCalendar(e) })
	return e, nil
}

// EditDraft — поправить черновик руками перед отправкой. Только пока черновик готов и не отправляется.
func (a *App) EditDraft(uid uint32, text string) (StoredEmail, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 5000 {
		return StoredEmail{}, ErrBadDraft
	}
	if _, ok := getEmail(uid); !ok {
		return StoredEmail{}, ErrEmailNotFound
	}
	if !updateDraftText(uid, text) {
		cur, _ := getEmail(uid)
		return cur, ErrWrongStatus{cur.Status}
	}

	e, _ := getEmail(uid)
	log.Printf("✏️ UID %d: черновик поправлен вручную", uid)
	a.statusChanged(e)
	return e, nil
}

// --- Фоновая работа ---

func (a *App) draft(e StoredEmail) {
	resp, err := requestDraft(a.AgentURL, e)
	if err != nil {
		log.Printf("❌ Черновик для UID %d: %v", e.UID, err)
		updateEmailStatus(e.UID, "draft_failed")
		e.Status = "draft_failed"
		if a.Notify != nil {
			a.Notify.DraftFailed(e, err.Error())
		}
		return
	}

	saveDraft(e.UID, resp)
	if fresh, ok := getEmail(e.UID); ok {
		e = fresh // вместе с черновиком подтянулись встреча и заметка из календаря
	}
	log.Printf("✍️ Черновик для UID %d готов (попыток: %d)", e.UID, resp.Attempts)
	if resp.CalendarNote != "" {
		log.Printf("📅 UID %d: %s", e.UID, resp.CalendarNote)
	}

	if a.Notify != nil {
		a.Notify.DraftReady(e, resp.Problems)
	}
}

func (a *App) send(e StoredEmail) {
	if err := a.Mailer.SendReply(e); err != nil {
		log.Printf("❌ Отправка UID %d: %v", e.UID, err)
		// Откатываем в draft_ready, чтобы можно было нажать «Отправить» ещё раз
		transitionStatus(e.UID, "draft_ready", "sending")
		if a.Notify != nil {
			a.Notify.SendFailed(e, err)
		}
		return
	}

	transitionStatus(e.UID, "sent", "sending")
	log.Printf("📤 UID %d: ответ отправлен на %s", e.UID, e.From)
	if a.Notify != nil {
		a.Notify.Sent(e)
	}
}

func (a *App) addToCalendar(e StoredEmail) {
	created, err := a.Calendar.CreateEvent(context.Background(), e.MeetingTitle, e.MeetingStart, e.MeetingEnd,
		fmt.Sprintf("Из письма от %s: %s", e.From, e.Subject))
	if err != nil {
		setCalendarEventID(e.UID, "") // отпускаем — можно нажать ещё раз
		log.Printf("❌ В календарь UID %d: %v", e.UID, err)
		if a.Notify != nil {
			a.Notify.CalendarFailed(e, err)
		}
		return
	}

	setCalendarEventID(e.UID, created.Event.ID)
	log.Printf("📅 UID %d: событие %s создано", e.UID, created.Event.ID)
	if a.Notify != nil {
		a.Notify.CalendarAdded(e, created)
	}
}
