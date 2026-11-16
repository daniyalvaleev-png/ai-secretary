package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Таймаут больше 30 сек, потому что getUpdates висит до 30 сек (long polling)
var tgHTTP = &http.Client{Timeout: 40 * time.Second}

func shouldSendTelegramAlert(d EmailDecision) bool {
	urgency := strings.ToLower(strings.TrimSpace(d.Urgency))

	// Подозрительное шлём всегда — пусть владелец знает, что его пытались развести
	return d.Suspicious ||
		d.RequiresConfirmation ||
		urgency == "высокая" ||
		urgency == "high" ||
		urgency == "срочно"
}

func buildTelegramAlert(from, subject string, d EmailDecision) string {
	var b strings.Builder

	if d.Suspicious {
		b.WriteString("🚨 ПОХОЖЕ НА МОШЕННИЧЕСТВО: " + d.SuspicionReason + "\n")
		b.WriteString("Не переходи по ссылкам и ничего не отправляй, пока не проверишь.\n\n")
	}

	b.WriteString("📩 Новое письмо для ИИ-секретаря\n\n")
	b.WriteString("От: " + from + "\n")
	b.WriteString("Тема: " + subject + "\n\n")
	b.WriteString("Срочность: " + d.Urgency + "\n")
	b.WriteString("Нужно подтверждение: " + fmt.Sprintf("%t", d.RequiresConfirmation) + "\n")
	b.WriteString("Действие: " + d.SuggestedAction)

	return b.String()
}

// tgCall — один вызов Telegram Bot API. Если out != nil, туда кладётся поле result.
func tgCall(token, method string, payload any, out any) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("не задан TELEGRAM_BOT_TOKEN")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/%s", token, method)

	resp, err := tgHTTP.Post(apiURL, "application/json", bytes.NewReader(body))
	if err != nil {
		// Go кладёт URL с токеном в текст ошибки — вырезаем, чтобы не светить в логах
		return fmt.Errorf("telegram %s: %s", method, strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer resp.Body.Close()

	var tgResp struct {
		Ok          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tgResp); err != nil {
		return fmt.Errorf("telegram %s: не удалось разобрать ответ (HTTP %d): %w", method, resp.StatusCode, err)
	}

	if !tgResp.Ok {
		return fmt.Errorf("telegram %s: %s", method, tgResp.Description)
	}

	if out != nil {
		return json.Unmarshal(tgResp.Result, out)
	}

	return nil
}

// Telegram принимает chat_id как число или строку.
// Если получается распарсить в число — отправляем числом.
func chatIDValue(chatID string) any {
	if parsed, err := strconv.ParseInt(chatID, 10, 64); err == nil {
		return parsed
	}
	return chatID
}

func sendTelegramMessage(token, chatID, text string) error {
	return tgCall(token, "sendMessage", map[string]any{
		"chat_id":                  chatIDValue(chatID),
		"text":                     text,
		"disable_web_page_preview": true,
	}, nil)
}

// sendEmailAlert — уведомление о письме с кнопками под ним
func sendEmailAlert(token, chatID string, uid uint32, text string) error {
	return sendWithKeyboard(token, chatID, text, [][]map[string]string{
		{button("✅ Подтвердить", "confirm", uid), button("❌ Отклонить", "reject", uid)},
		{button("📄 Показать письмо", "show", uid)},
	})
}

func button(text, action string, uid uint32) map[string]string {
	return map[string]string{
		"text":          text,
		"callback_data": action + ":" + strconv.FormatUint(uint64(uid), 10),
	}
}

func sendWithKeyboard(token, chatID, text string, rows [][]map[string]string) error {
	return tgCall(token, "sendMessage", map[string]any{
		"chat_id":                  chatIDValue(chatID),
		"text":                     text,
		"disable_web_page_preview": true,
		"reply_markup":             map[string]any{"inline_keyboard": rows},
	}, nil)
}

// sendDraftMessage — черновик с кнопками «Отправить / Переписать / Не отвечать»
func sendDraftMessage(token, chatID string, e StoredEmail, problems string) error {
	text := fmt.Sprintf("✍️ Черновик ответа\nКому: %s\nТема: Re: %s\n\n%s", e.From, e.Subject, e.Draft)
	if problems != "" {
		text += "\n\n⚠️ Агент так и не справился: " + problems
	}
	if e.CalendarNote != "" {
		text += "\n\n🗓 " + e.CalendarNote
	}
	if e.MeetingStart != "" {
		text += fmt.Sprintf("\n📅 Встреча: %s, %s–%s", e.MeetingTitle, humanTime(e.MeetingStart), shortTime(e.MeetingEnd))
	}

	rows := [][]map[string]string{
		{button("📤 Отправить", "send", e.UID), button("🔄 Переписать", "redraft", e.UID)},
		{button("🙅 Не отвечать", "skip", e.UID)},
	}
	// Кнопка календаря — только если в письме есть встреча и её ещё не добавили
	if e.MeetingStart != "" && e.CalendarEventID == "" {
		rows[1] = append(rows[1], button("📅 В календарь", "cal", e.UID))
	}

	return sendWithKeyboard(token, chatID, text, rows)
}

// sendDraftFailed — «черновик не получился» с кнопками «Повторить / Не отвечать»
func sendDraftFailed(token, chatID string, e StoredEmail, reason string) error {
	text := fmt.Sprintf("⚠️ Не получилось написать черновик для «%s»:\n%s", e.Subject, reason)

	return sendWithKeyboard(token, chatID, text, [][]map[string]string{
		{button("🔄 Повторить", "redraft", e.UID), button("🙅 Не отвечать", "skip", e.UID)},
	})
}

var weekdaysShortRU = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

// humanTime: «2026-10-28 13:59» → «ср 28.10 13:59»
func humanTime(s string) string {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		return s
	}
	return weekdaysShortRU[t.Weekday()] + " " + t.Format("02.01 15:04")
}

// shortTime: «2026-10-28 14:59» → «14:59»
func shortTime(s string) string {
	if len(s) >= 16 {
		return s[11:16]
	}
	return s
}

type tgCallback struct {
	ID      string `json:"id"`
	Data    string `json:"data"`
	Message *struct {
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

type tgMessage struct {
	Text string `json:"text"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	CallbackQuery *tgCallback `json:"callback_query"`
	Message       *tgMessage  `json:"message"`
}

// --- Бот ---

// Bot — Telegram-интерфейс к действиям: переводит нажатия кнопок в вызовы App
// и сообщает в чат, чем кончилась фоновая работа (реализует Notifier).
type Bot struct {
	Token  string
	ChatID string
	App    *App
}

// Run крутится в отдельной горутине и ловит нажатия кнопок и команды (long polling)
func (b *Bot) Run() {
	log.Println("🤖 Telegram-бот слушает кнопки")
	b.setCommands()

	var offset int64
	for {
		var updates []tgUpdate
		err := tgCall(b.Token, "getUpdates", map[string]any{
			"offset":          offset,
			"timeout":         20,
			"allowed_updates": []string{"callback_query", "message"},
		}, &updates)
		if err != nil {
			log.Printf("⚠️ getUpdates: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, u := range updates {
			// offset = последний update_id + 1, иначе Telegram будет слать те же нажатия снова
			offset = u.UpdateID + 1
			if u.CallbackQuery != nil {
				b.handleCallback(u.CallbackQuery)
			}
			if u.Message != nil {
				b.handleCommand(u.Message)
			}
		}
	}
}

func (b *Bot) send(text string) {
	if err := sendTelegramMessage(b.Token, b.ChatID, text); err != nil {
		log.Printf("⚠️ Telegram: %v", err)
	}
}

func (b *Bot) answer(callbackID, text string) {
	if err := tgCall(b.Token, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
	}, nil); err != nil {
		log.Printf("⚠️ answerCallbackQuery: %v", err)
	}
}

// editMessage дописывает строку к сообщению с кнопкой и убирает кнопки (нет reply_markup)
func (b *Bot) editMessage(cb *tgCallback, suffix string) {
	if err := tgCall(b.Token, "editMessageText", map[string]any{
		"chat_id":    cb.Message.Chat.ID,
		"message_id": cb.Message.MessageID,
		"text":       cb.Message.Text + "\n\n" + suffix,
	}, nil); err != nil {
		log.Printf("⚠️ editMessageText: %v", err)
	}
}

// buttonAction — что делает кнопка: действие App, всплывашка после нажатия и что дописать к сообщению
type buttonAction struct {
	do     func(uint32) (StoredEmail, error)
	answer string
	edit   string // пусто — сообщение не трогаем, кнопки остаются
}

func (b *Bot) handleCallback(cb *tgCallback) {
	if cb.Message == nil {
		b.answer(cb.ID, "Сообщение слишком старое")
		return
	}

	// Кнопки жмёт только твой чат, чужим — болт
	if strconv.FormatInt(cb.Message.Chat.ID, 10) != b.ChatID {
		b.answer(cb.ID, "Нет доступа")
		return
	}

	action, idStr, _ := strings.Cut(cb.Data, ":")
	uid64, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		b.answer(cb.ID, "Кривые данные кнопки")
		return
	}
	uid := uint32(uid64)

	if action == "show" {
		b.showEmail(cb, uid)
		return
	}

	actions := map[string]buttonAction{
		"confirm": {b.App.Confirm, "✅ Пишу черновик…", "✅ Подтверждено — пишу черновик…"},
		"reject":  {b.App.Reject, "❌ Отклонено", "❌ Отклонено"},
		"redraft": {b.App.Redraft, "🔄 Переписываю…", "🔄 Переписываю…"},
		"send":    {b.App.Send, "📤 Отправляю…", "📤 Отправляю…"},
		"skip":    {b.App.Skip, "🙅 Не отвечаем", "🙅 Не отвечаем"},
		"cal":     {b.App.AddToCalendar, "📅 Добавляю…", ""},
	}

	ba, ok := actions[action]
	if !ok {
		b.answer(cb.ID, "Неизвестная кнопка")
		return
	}

	// Правила (какой статус куда можно) живут в App — бот только показывает результат
	if _, err := ba.do(uid); err != nil {
		b.answer(cb.ID, buttonError(err))
		return
	}
	b.answer(cb.ID, ba.answer)
	if ba.edit != "" {
		b.editMessage(cb, ba.edit)
	}
}

// buttonError — короткий текст для всплывашки
func buttonError(err error) string {
	var wrong ErrWrongStatus
	switch {
	case errors.As(err, &wrong):
		return "Уже обработано: " + wrong.Status
	case errors.Is(err, ErrAlreadyInCalendar):
		return "Уже в календаре"
	default:
		s := err.Error()
		return strings.ToUpper(s[:1]) + s[1:]
	}
}

func (b *Bot) showEmail(cb *tgCallback, uid uint32) {
	e, found := getEmail(uid)
	if !found {
		b.answer(cb.ID, "Письмо не найдено в базе")
		return
	}
	b.answer(cb.ID, "")

	// Лимит Telegram — 4096 символов, поэтому тело режем
	b.send(fmt.Sprintf("📄 Письмо UID %d\nОт: %s\nТема: %s\n\n%s", e.UID, e.From, e.Subject, truncate(e.Body, 3500)))
}

// --- Notifier: чем кончилась фоновая работа. Срабатывает и на кнопки, и на действия из приложения ---

func (b *Bot) DraftReady(e StoredEmail, problems string) {
	if err := sendDraftMessage(b.Token, b.ChatID, e, problems); err != nil {
		log.Printf("⚠️ Не удалось отправить черновик в Telegram: %v (письмо найдётся через /pending)", err)
	}
}

func (b *Bot) DraftFailed(e StoredEmail, reason string) {
	if err := sendDraftFailed(b.Token, b.ChatID, e, reason); err != nil {
		log.Printf("⚠️ Telegram: %v (письмо найдётся через /pending)", err)
	}
}

func (b *Bot) Sent(e StoredEmail) {
	b.send(fmt.Sprintf("✅ Ответ на «%s» отправлен → %s", e.Subject, e.From))
}

func (b *Bot) SendFailed(e StoredEmail, err error) {
	text := fmt.Sprintf("⚠️ Не удалось отправить ответ на «%s»:\n%v", e.Subject, err)
	if err := sendWithKeyboard(b.Token, b.ChatID, text, [][]map[string]string{
		{button("📤 Попробовать ещё", "send", e.UID), button("🙅 Не отвечать", "skip", e.UID)},
	}); err != nil {
		log.Printf("⚠️ Telegram: %v", err)
	}
}

func (b *Bot) CalendarAdded(e StoredEmail, created createdEvent) {
	text := fmt.Sprintf("📅 Добавил в календарь: %s\n%s–%s", created.Event.Title, humanTime(created.Event.Start), shortTime(created.Event.End))
	if len(created.Conflicts) > 0 {
		text += "\n\n⚠️ Пересекается с:"
		for _, c := range created.Conflicts {
			text += fmt.Sprintf("\n• %s, %s–%s", c.Title, humanTime(c.Start), shortTime(c.End))
		}
	}
	b.send(text)
}

func (b *Bot) CalendarFailed(e StoredEmail, err error) {
	b.send("⚠️ Не удалось добавить в календарь: " + err.Error())
}

// --- Команды ---

// setCommands — команды появятся в меню бота (кнопка «/» слева от поля ввода)
func (b *Bot) setCommands() {
	if err := tgCall(b.Token, "setMyCommands", map[string]any{
		"commands": []map[string]string{
			{"command": "pending", "description": "Письма, которые ждут решения"},
		},
	}, nil); err != nil {
		log.Printf("⚠️ setMyCommands: %v", err)
	}
}

func (b *Bot) handleCommand(m *tgMessage) {
	// Команды принимаем только из своего чата, остальных молча игнорим
	if strconv.FormatInt(m.Chat.ID, 10) != b.ChatID {
		return
	}

	cmd, _, _ := strings.Cut(strings.TrimSpace(m.Text), " ")
	cmd, _, _ = strings.Cut(cmd, "@") // в группах команда приходит как /pending@ИмяБота

	switch cmd {
	case "/pending":
		log.Println("📋 /pending")
		b.sendPendingList()
	case "/start", "/help":
		b.send("Я ИИ-секретарь. Новые письма присылаю сам.\n/pending — письма, которые ждут твоего решения")
	}
}

// sendPendingList заново присылает все письма, где ход за тобой, — с живыми кнопками.
// Старые сообщения по этим письмам не мешают: transitionStatus не даст нажать одно и то же дважды.
func (b *Bot) sendPendingList() {
	total := 0

	for _, status := range []string{"pending", "draft_failed", "draft_ready"} {
		emails, err := listEmails(status, 10)
		if err != nil {
			log.Printf("⚠️ /pending: %v", err)
			continue
		}

		for _, e := range emails {
			var err error
			switch status {
			case "pending":
				d := EmailDecision{Urgency: e.Urgency, RequiresConfirmation: e.RequiresConfirmation, SuggestedAction: e.SuggestedAction, Suspicious: e.Suspicious, SuspicionReason: e.SuspicionReason}
				err = sendEmailAlert(b.Token, b.ChatID, e.UID, buildTelegramAlert(e.From, e.Subject, d))
			case "draft_failed":
				err = sendDraftFailed(b.Token, b.ChatID, e, "черновик так и не дописался")
			case "draft_ready":
				err = sendDraftMessage(b.Token, b.ChatID, e, "")
			}
			if err != nil {
				log.Printf("⚠️ /pending UID %d: %v", e.UID, err)
				continue
			}
			total++
		}
	}

	if total == 0 {
		b.send("🎉 Ничего не висит, всё разобрано")
	}
}
