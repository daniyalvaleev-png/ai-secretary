package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CalendarClient ходит в микросервис calendar-mcp по тому же протоколу MCP, что и агент.
// Шлюз — тоже MCP-клиент: ему нужен только create_event для кнопки «В календарь».
type CalendarClient struct {
	URL    string
	APIKey string
}

type createdEvent struct {
	Event struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"event"`
	Conflicts []struct {
		Title string `json:"title"`
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"conflicts"`
}

// bearerTransport добавляет ключ календаря к каждому HTTP-запросу MCP-клиента
type bearerTransport struct{ key string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

func (c *CalendarClient) CreateEvent(ctx context.Context, title, start, end, description string) (createdEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "go-gateway", Version: "v0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   c.URL,
		HTTPClient: &http.Client{Transport: bearerTransport{c.APIKey}},
		MaxRetries: -1, // календарь лежит — сразу ошибка, без ретраев
	}, nil)
	if err != nil {
		return createdEvent{}, fmt.Errorf("календарь недоступен (запущен calendar-mcp?): %w", err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "create_event",
		Arguments: map[string]any{
			"title":       title,
			"start":       start,
			"end":         end,
			"description": description,
		},
	})
	if err != nil {
		return createdEvent{}, err
	}

	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if res.IsError {
		return createdEvent{}, fmt.Errorf("календарь: %s", text)
	}

	var out createdEvent
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return createdEvent{}, fmt.Errorf("кривой ответ календаря: %w", err)
	}
	return out, nil
}
