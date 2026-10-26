package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Системный промпт живёт в коде (и в git), а не в Modelfile — так видно, как он менялся
const analyzeSystemPrompt = `Ты — ИИ-секретарь. Разбираешь входящие письма владельца почты и решаешь, что с ними делать.
Отвечай только JSON по заданной схеме, все тексты — на русском языке.

- "срочность": "высокая" — нужен ответ или действие сегодня; "средняя" — в ближайшие дни; "низкая" — можно не торопиться, а также рассылки и спам.
- "требуется_подтверждение": true, если письмо ждёт ответа или действия владельца; false для рассылок, уведомлений и спама.
- "предлагаемое_действие": одно короткое предложение — что сделать секретарю. Например: "Ответить, что встреча подходит", "Уточнить сумму и сроки", "Ничего, это рассылка". Не пересказывай письмо и ни на что не соглашайся.`

// Structured outputs: Ollama заставляет модель вернуть JSON ровно такой формы.
// enum — модель физически не сможет написать срочность "срочно!!!" или по-португальски.
var emailDecisionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"отправитель":             map[string]any{"type": "string"},
		"срочность":               map[string]any{"type": "string", "enum": []string{"высокая", "средняя", "низкая"}},
		"требуется_подтверждение": map[string]any{"type": "boolean"},
		"предлагаемое_действие":   map[string]any{"type": "string"},
	},
	"required": []string{"отправитель", "срочность", "требуется_подтверждение", "предлагаемое_действие"},
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatRequest struct {
	Model     string          `json:"model"`
	Messages  []ollamaMessage `json:"messages"`
	Format    any             `json:"format"`
	Stream    bool            `json:"stream"`
	KeepAlive string          `json:"keep_alive"`
	Options   map[string]any  `json:"options,omitempty"`
	Think     bool            `json:"think"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
}

var ollamaHTTP = &http.Client{Timeout: 3 * time.Minute}

// askOllama — /api/chat вместо /api/generate: модель получает системный промпт и письмо
// в своём родном чат-шаблоне, а не одной склеенной строкой
func askOllama(baseURL, model, prompt string) (EmailDecision, error) {
	payload := ollamaChatRequest{
		Model: model,
		Messages: []ollamaMessage{
			{Role: "system", Content: analyzeSystemPrompt},
			{Role: "user", Content: prompt},
		},
		Format: emailDecisionSchema,
		Stream: false,
		// Держим модель в видеопамяти 30 минут, а не 5 по умолчанию — не грузим её заново на каждое письмо
		KeepAlive: "30m",
		Options:   map[string]any{"temperature": 0},
		// granite4.2 — думающая модель: без этого пишет ~1000 токенов рассуждений на каждое письмо
		Think: false,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return EmailDecision{}, err
	}

	resp, err := ollamaHTTP.Post(baseURL+"/api/chat", "application/json", bytes.NewReader(data))
	if err != nil {
		return EmailDecision{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return EmailDecision{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return EmailDecision{}, fmt.Errorf("Ollama вернул HTTP %d: %s", resp.StatusCode, respBody)
	}

	var chatResp ollamaChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return EmailDecision{}, err
	}

	var decision EmailDecision
	if err := json.Unmarshal([]byte(chatResp.Message.Content), &decision); err != nil {
		return EmailDecision{}, fmt.Errorf("модель вернула невалидный JSON: %w; сырой ответ: %s", err, chatResp.Message.Content)
	}

	return decision, nil
}
