package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Модель на CPU думает до минуты, а граф может сделать до 3 попыток — даём с запасом
var agentHTTP = &http.Client{Timeout: 5 * time.Minute}

type draftRequest struct {
	UID     uint32 `json:"uid"`
	Sender  string `json:"sender"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type draftResponse struct {
	UID      uint32 `json:"uid"`
	Draft    string `json:"draft"`
	Attempts int    `json:"attempts"`
	Problems string `json:"problems"`
}

// requestDraft просит Python-агента (LangGraph) написать черновик ответа
func requestDraft(agentURL string, e StoredEmail) (draftResponse, error) {
	body, err := json.Marshal(draftRequest{
		UID:     e.UID,
		Sender:  e.From,
		Subject: e.Subject,
		Body:    e.Body,
	})
	if err != nil {
		return draftResponse{}, err
	}

	resp, err := agentHTTP.Post(agentURL+"/draft", "application/json", bytes.NewReader(body))
	if err != nil {
		return draftResponse{}, fmt.Errorf("агент недоступен (запущен uvicorn?): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return draftResponse{}, fmt.Errorf("агент вернул HTTP %d", resp.StatusCode)
	}

	var out draftResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return draftResponse{}, fmt.Errorf("кривой ответ агента: %w", err)
	}

	return out, nil
}
