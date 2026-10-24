package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
	b.WriteString("Действие: " + d.SuggestedAction + "\n\n")
	b.WriteString("Позже здесь будут кнопки:\n")
	b.WriteString("[Подтвердить] [Отклонить] [Показать письмо]\n")

	return b.String()
}

func sendTelegramMessage(token, chatID, text string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("не задан TELEGRAM_BOT_TOKEN")
	}

	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("не задан TELEGRAM_CHAT_ID")
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	// Telegram принимает chat_id как число или строку.
	// Если получается распарсить в число — отправляем числом.
	chatIDValue := any(chatID)
	if parsed, err := strconv.ParseInt(chatID, 10, 64); err == nil {
		chatIDValue = parsed
	}

	payload := map[string]any{
		"chat_id":                  chatIDValue,
		"text":                     text,
		"disable_web_page_preview": true,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var tgResp struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}

	if err := json.Unmarshal(respBody, &tgResp); err != nil {
		return fmt.Errorf("не удалось разобрать ответ Telegram: %w; сырой ответ: %s", err, string(respBody))
	}

	if !tgResp.Ok {
		return fmt.Errorf("telegram API error: %s", tgResp.Description)
	}

	return nil
}
