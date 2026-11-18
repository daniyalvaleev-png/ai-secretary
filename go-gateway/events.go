package main

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Лента событий для приложения: «пришло письмо», «черновик готов», «отправлено»…
//
// Приложение спрашивает GET /api/events?after=N («что случилось после события N?»).
// Если новое уже есть — ответ сразу. Если нет — сервер держит запрос до 25 секунд и отвечает,
// как только что-то произойдёт. Это long polling: работает и в браузере, и на телефоне,
// без веб-сокетов, а уведомление приходит через доли секунды после события.

// Event — одно событие. Text — готовая фраза для уведомления.
type Event struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"` // new_email, status, draft_ready, draft_failed, sent, send_failed, calendar_added, calendar_failed
	UID     uint32 `json:"uid"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Status  string `json:"status"`
	Text    string `json:"text"`
	// Important — стоит показать системное уведомление, даже если приложение свёрнуто
	Important bool      `json:"important"`
	At        time.Time `json:"at"`
}

// EventHub хранит последние события в памяти и будит тех, кто их ждёт.
// После перезапуска шлюза лента начинается заново — приложение это заметит и просто перечитает список.
type EventHub struct {
	mu     sync.Mutex
	lastID int64
	buf    []Event
	max    int
	wake   chan struct{} // закрывается при каждом новом событии — так разом будятся все ждущие запросы
}

func NewEventHub(max int) *EventHub {
	return &EventHub{max: max, wake: make(chan struct{})}
}

func (h *EventHub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.lastID++
	ev.ID = h.lastID
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	h.buf = append(h.buf, ev)
	if len(h.buf) > h.max {
		h.buf = h.buf[len(h.buf)-h.max:]
	}
	close(h.wake)
	h.wake = make(chan struct{})
}

// after — события новее after, номер последнего события и канал «случилось новое»
func (h *EventHub) after(after int64) ([]Event, int64, <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var out []Event
	for _, e := range h.buf {
		if e.ID > after {
			out = append(out, e)
		}
	}
	return out, h.lastID, h.wake
}

// Wait отдаёт события новее after; если их нет — ждёт до timeout (или пока клиент не отключился)
func (h *EventHub) Wait(ctx context.Context, after int64, timeout time.Duration) ([]Event, int64) {
	evs, last, wake := h.after(after)
	// after > last — шлюз перезапускался и нумерация пошла сначала: отвечаем сразу, клиент начнёт с last
	if len(evs) > 0 || after > last {
		return evs, last
	}

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-wake:
		evs, last, _ = h.after(after)
	case <-t.C:
	case <-ctx.Done():
	}
	return evs, last
}

// --- Откуда берутся события ---

func emailEvent(e StoredEmail, typ, text string, important bool) Event {
	return Event{
		Type: typ, UID: e.UID, From: e.From, Subject: e.Subject, Status: e.Status,
		Text: text, Important: important,
	}
}

// NewEmail — письмо разобрано и сохранено
func (h *EventHub) NewEmail(e StoredEmail) {
	text := "Новое письмо"
	switch {
	case e.Suspicious:
		text = "🚨 Подозрительное письмо"
	case e.Urgency == "высокая":
		text = "⚡ Срочное письмо"
	}
	h.Publish(emailEvent(e, "new_email", text, true))
}

// StatusChanged — нажали кнопку (в приложении или в Telegram) и статус письма сменился
func (h *EventHub) StatusChanged(e StoredEmail) {
	h.Publish(emailEvent(e, "status", "", false))
}

// EventHub — тоже Notifier: результаты фоновой работы попадают в ленту
func (h *EventHub) DraftReady(e StoredEmail, _ string) {
	e.Status = "draft_ready"
	h.Publish(emailEvent(e, "draft_ready", "✍️ Черновик готов — можно отправлять", true))
}

func (h *EventHub) DraftFailed(e StoredEmail, _ string) {
	e.Status = "draft_failed"
	h.Publish(emailEvent(e, "draft_failed", "Черновик не получился — можно повторить", true))
}

func (h *EventHub) Sent(e StoredEmail) {
	e.Status = "sent"
	h.Publish(emailEvent(e, "sent", "📤 Ответ отправлен", false))
}

func (h *EventHub) SendFailed(e StoredEmail, _ error) {
	e.Status = "draft_ready"
	h.Publish(emailEvent(e, "send_failed", "Не удалось отправить ответ — попробуй ещё раз", true))
}

func (h *EventHub) CalendarAdded(e StoredEmail, _ createdEvent) {
	h.Publish(emailEvent(e, "calendar_added", "📅 Добавлено в календарь", false))
}

func (h *EventHub) CalendarFailed(e StoredEmail, _ error) {
	h.Publish(emailEvent(e, "calendar_failed", "Не удалось добавить в календарь", true))
}

// multiNotifier раздаёт результат всем сразу: в ленту для приложения и в Telegram
type multiNotifier []Notifier

func (m multiNotifier) DraftReady(e StoredEmail, p string) {
	for _, n := range m {
		n.DraftReady(e, p)
	}
}

func (m multiNotifier) DraftFailed(e StoredEmail, r string) {
	for _, n := range m {
		n.DraftFailed(e, r)
	}
}

func (m multiNotifier) Sent(e StoredEmail) {
	for _, n := range m {
		n.Sent(e)
	}
}

func (m multiNotifier) SendFailed(e StoredEmail, err error) {
	for _, n := range m {
		n.SendFailed(e, err)
	}
}

func (m multiNotifier) CalendarAdded(e StoredEmail, c createdEvent) {
	for _, n := range m {
		n.CalendarAdded(e, c)
	}
}

func (m multiNotifier) CalendarFailed(e StoredEmail, err error) {
	for _, n := range m {
		n.CalendarFailed(e, err)
	}
}

// --- HTTP ---

const eventsWait = 25 * time.Second // меньше WriteTimeout сервера (30 с)

// GET /api/events?after=N → {"events":[...], "last_id": M}
// Без after — сразу отвечает номером последнего события: с него приложение и начинает слушать.
func handleEvents(h *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h == nil {
			writeError(w, http.StatusServiceUnavailable, "лента событий не настроена")
			return
		}

		s := r.URL.Query().Get("after")
		if s == "" {
			_, last, _ := h.after(0)
			writeJSON(w, http.StatusOK, map[string]any{"events": []Event{}, "last_id": last})
			return
		}
		after, err := strconv.ParseInt(s, 10, 64)
		if err != nil || after < 0 {
			writeError(w, http.StatusBadRequest, "after — номер события, число от 0")
			return
		}

		evs, last := h.Wait(r.Context(), after, eventsWait)
		if evs == nil {
			evs = []Event{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": evs, "last_id": last})
	}
}
