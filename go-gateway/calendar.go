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
// Шлюзу нужны create_event (кнопка «В календарь») и list_events (календарь в приложении).
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

// calendarEvent — событие, как его отдаёт calendar-mcp (время «2026-10-28 13:59» по поясу владельца)
type calendarEvent struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Start       string `json:"start"`
	End         string `json:"end"`
	Weekday     string `json:"weekday"`
	Description string `json:"description,omitempty"`
}

// bearerTransport добавляет ключ календаря к каждому HTTP-запросу MCP-клиента
type bearerTransport struct{ key string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

// callTool — один вызов MCP-инструмента календаря; ответ кладётся в out
func (c *CalendarClient) callTool(ctx context.Context, name string, args map[string]any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "go-gateway", Version: "v0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   c.URL,
		HTTPClient: &http.Client{Transport: bearerTransport{c.APIKey}},
		MaxRetries: -1, // календарь лежит — сразу ошибка, без ретраев
	}, nil)
	if err != nil {
		return fmt.Errorf("календарь недоступен (запущен calendar-mcp?): %w", err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err
	}

	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if res.IsError {
		return fmt.Errorf("календарь: %s", text)
	}

	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("кривой ответ календаря: %w", err)
	}
	return nil
}

func (c *CalendarClient) CreateEvent(ctx context.Context, title, start, end, description string) (createdEvent, error) {
	var out createdEvent
	err := c.callTool(ctx, "create_event", map[string]any{
		"title":       title,
		"start":       start,
		"end":         end,
		"description": description,
	}, &out)
	return out, err
}

// ListEvents — события за период; from/to — «2026-10-28» или «2026-10-28 09:00», как понимает calendar-mcp
func (c *CalendarClient) ListEvents(ctx context.Context, from, to string) ([]calendarEvent, error) {
	var out struct {
		Events []calendarEvent `json:"events"`
	}
	err := c.callTool(ctx, "list_events", map[string]any{"from": from, "to": to}, &out)
	return out.Events, err
}
