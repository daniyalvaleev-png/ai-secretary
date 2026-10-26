package main

import (
	"bytes"
	"encoding/json"
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

	return d.RequiresConfirmation ||
		urgency == "высокая" ||
		urgency == "high" ||
		urgency == "срочно"
}

func buildTelegramAlert(from, subject string, d EmailDecision) string {
	var b strings.Builder

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
	id := strconv.FormatUint(uint64(uid), 10)

	keyboard := map[string]any{
		"inline_keyboard": [][]map[string]string{
			{
				{"text": "✅ Подтвердить", "callback_data": "confirm:" + id},
				{"text": "❌ Отклонить", "callback_data": "reject:" + id},
			},
			{
				{"text": "📄 Показать письмо", "callback_data": "show:" + id},
			},
		},
	}

	return tgCall(token, "sendMessage", map[string]any{
		"chat_id":                  chatIDValue(chatID),
		"text":                     text,
		"disable_web_page_preview": true,
		"reply_markup":             keyboard,
	}, nil)
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

type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	CallbackQuery *tgCallback `json:"callback_query"`
}

// runTelegramBot крутится в отдельной горутине и ловит нажатия кнопок (long polling)
func runTelegramBot(token, chatID, agentURL string, mailer Mailer) {
	log.Println("🤖 Telegram-бот слушает кнопки")

	var offset int64
	for {
		var updates []tgUpdate
		err := tgCall(token, "getUpdates", map[string]any{
			"offset":          offset,
			"timeout":         20,
			"allowed_updates": []string{"callback_query"},
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
				handleCallback(token, chatID, agentURL, mailer, u.CallbackQuery)
			}
		}
	}
}

func answerCallback(token, callbackID, text string) {
	if err := tgCall(token, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
	}, nil); err != nil {
		log.Printf("⚠️ answerCallbackQuery: %v", err)
	}
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

// editMessage дописывает строку к сообщению с кнопкой и убирает кнопки (нет reply_markup)
func editMessage(token string, cb *tgCallback, suffix string) {
	if err := tgCall(token, "editMessageText", map[string]any{
		"chat_id":    cb.Message.Chat.ID,
		"message_id": cb.Message.MessageID,
		"text":       cb.Message.Text + "\n\n" + suffix,
	}, nil); err != nil {
		log.Printf("⚠️ editMessageText: %v", err)
	}
}

func handleCallback(token, chatID, agentURL string, mailer Mailer, cb *tgCallback) {
	if cb.Message == nil {
		answerCallback(token, cb.ID, "Сообщение слишком старое")
		return
	}

	// Кнопки жмёт только твой чат, чужим — болт
	if strconv.FormatInt(cb.Message.Chat.ID, 10) != chatID {
		answerCallback(token, cb.ID, "Нет доступа")
		return
	}

	action, idStr, _ := strings.Cut(cb.Data, ":")
	uid64, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		answerCallback(token, cb.ID, "Кривые данные кнопки")
		return
	}
	uid := uint32(uid64)

	e, found := getEmail(uid)
	if !found {
		answerCallback(token, cb.ID, "Письмо не найдено в базе")
		return
	}

	// Каждая кнопка работает только из своих статусов. Не тот статус — значит, уже нажимали.
	switch action {
	case "confirm":
		if !transitionStatus(uid, "drafting", "pending") {
			answerCallback(token, cb.ID, "Уже обработано: "+e.Status)
			return
		}
		answerCallback(token, cb.ID, "✅ Пишу черновик…")
		editMessage(token, cb, "✅ Подтверждено — пишу черновик…")
		log.Printf("👆 UID %d → drafting", uid)
		go draftAndSend(token, chatID, agentURL, e)

	case "reject":
		if !transitionStatus(uid, "rejected", "pending") {
			answerCallback(token, cb.ID, "Уже обработано: "+e.Status)
			return
		}
		answerCallback(token, cb.ID, "❌ Отклонено")
		editMessage(token, cb, "❌ Отклонено")
		log.Printf("👆 UID %d → rejected", uid)

	case "redraft":
		if !transitionStatus(uid, "drafting", "draft_ready", "draft_failed") {
			answerCallback(token, cb.ID, "Сейчас нельзя: "+e.Status)
			return
		}
		answerCallback(token, cb.ID, "🔄 Переписываю…")
		editMessage(token, cb, "🔄 Переписываю…")
		log.Printf("👆 UID %d → drafting (заново)", uid)
		go draftAndSend(token, chatID, agentURL, e)

	case "send":
		if !transitionStatus(uid, "sending", "draft_ready") {
			answerCallback(token, cb.ID, "Сейчас нельзя: "+e.Status)
			return
		}
		answerCallback(token, cb.ID, "📤 Отправляю…")
		editMessage(token, cb, "📤 Отправляю…")
		log.Printf("👆 UID %d → sending", uid)
		go sendAndReport(token, chatID, mailer, e)

	case "skip":
		if !transitionStatus(uid, "skipped", "draft_ready", "draft_failed") {
			answerCallback(token, cb.ID, "Сейчас нельзя: "+e.Status)
			return
		}
		answerCallback(token, cb.ID, "🙅 Не отвечаем")
		editMessage(token, cb, "🙅 Не отвечаем")
		log.Printf("👆 UID %d → skipped", uid)

	case "show":
		answerCallback(token, cb.ID, "")

		// Лимит Telegram — 4096 символов, поэтому тело режем
		text := fmt.Sprintf("📄 Письмо UID %d\nОт: %s\nТема: %s\n\n%s",
			e.UID, e.From, e.Subject, truncate(e.Body, 3500))
		if err := sendTelegramMessage(token, chatID, text); err != nil {
			log.Printf("⚠️ Не удалось показать письмо: %v", err)
		}

	default:
		answerCallback(token, cb.ID, "Неизвестная кнопка")
	}
}

// draftAndSend крутится в своей горутине: агент думает до минуты,
// а бот в это время должен дальше ловить другие кнопки
func draftAndSend(token, chatID, agentURL string, e StoredEmail) {
	resp, err := requestDraft(agentURL, e)
	if err != nil {
		log.Printf("❌ Черновик для UID %d: %v", e.UID, err)
		updateEmailStatus(e.UID, "draft_failed")

		text := fmt.Sprintf("⚠️ Не получилось написать черновик для «%s»:\n%v", e.Subject, err)
		if err := sendWithKeyboard(token, chatID, text, [][]map[string]string{
			{button("🔄 Повторить", "redraft", e.UID), button("🙅 Не отвечать", "skip", e.UID)},
		}); err != nil {
			log.Printf("⚠️ Telegram: %v", err)
		}
		return
	}

	saveDraft(e.UID, resp.Draft)
	log.Printf("✍️ Черновик для UID %d готов (попыток: %d)", e.UID, resp.Attempts)

	text := fmt.Sprintf("✍️ Черновик ответа\nКому: %s\nТема: Re: %s\n\n%s", e.From, e.Subject, resp.Draft)
	if resp.Problems != "" {
		text += "\n\n⚠️ Агент так и не справился: " + resp.Problems
	}

	if err := sendWithKeyboard(token, chatID, text, [][]map[string]string{
		{button("📤 Отправить", "send", e.UID), button("🔄 Переписать", "redraft", e.UID)},
		{button("🙅 Не отвечать", "skip", e.UID)},
	}); err != nil {
		log.Printf("⚠️ Не удалось отправить черновик в Telegram: %v", err)
	}
}

// sendAndReport отправляет ответ по SMTP в своей горутине и пишет в Telegram, чем кончилось
func sendAndReport(token, chatID string, mailer Mailer, e StoredEmail) {
	if err := mailer.SendReply(e); err != nil {
		log.Printf("❌ Отправка UID %d: %v", e.UID, err)

		// Откатываем в draft_ready, чтобы можно было нажать «Отправить» ещё раз
		transitionStatus(e.UID, "draft_ready", "sending")

		text := fmt.Sprintf("⚠️ Не удалось отправить ответ на «%s»:\n%v", e.Subject, err)
		if err := sendWithKeyboard(token, chatID, text, [][]map[string]string{
			{button("📤 Попробовать ещё", "send", e.UID), button("🙅 Не отвечать", "skip", e.UID)},
		}); err != nil {
			log.Printf("⚠️ Telegram: %v", err)
		}
		return
	}

	transitionStatus(e.UID, "sent", "sending")
	log.Printf("📤 UID %d: ответ отправлен на %s", e.UID, e.From)

	if err := sendTelegramMessage(token, chatID, fmt.Sprintf("✅ Ответ на «%s» отправлен → %s", e.Subject, e.From)); err != nil {
		log.Printf("⚠️ Telegram: %v", err)
	}
}
