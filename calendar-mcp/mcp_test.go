package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

// Полный цикл, как у агента: HTTP-сервер с ключом → MCP-клиент → вызовы инструментов
func TestMCPOverHTTP(t *testing.T) {
	ctx := context.Background()
	cal, loc := newTestCalendar(t)

	server := newMCPServer(cal, loc)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	srv := httptest.NewServer(requireAPIKey("secret", handler))
	defer srv.Close()

	// Без ключа — 401
	resp, _ := http.Post(srv.URL, "application/json", strings.NewReader(`{}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("без ключа ожидали 401, получили", resp.StatusCode)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   srv.URL,
		HTTPClient: &http.Client{Transport: bearer{"secret"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	t.Log("инструменты:", names)
	if len(names) != 5 {
		t.Fatal("ожидали 5 инструментов")
	}

	call := func(name string, args map[string]any) (map[string]any, bool) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(name, err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		var out map[string]any
		json.Unmarshal([]byte(text), &out)
		t.Logf("%s(%v) → isError=%v %s", name, args, res.IsError, text)
		return out, res.IsError
	}

	now, _ := call("now", nil)
	if !strings.Contains(now["tomorrow"].(string), "(") {
		t.Fatal(now)
	}

	created, isErr := call("create_event", map[string]any{"title": "Встреча с Васей в парке", "start": "2026-10-28 13:59", "description": "у вокзала"})
	if isErr {
		t.Fatal("create_event упал")
	}
	ev := created["event"].(map[string]any)
	if ev["end"] != "2026-10-28 14:59" || ev["weekday"] != "среда" {
		t.Fatal("по умолчанию час, 28.10.2026 — среда:", ev)
	}

	conflict, _ := call("create_event", map[string]any{"title": "Созвон", "start": "2026-10-28T14:30", "end": "2026-10-28 15:00"})
	if len(conflict["conflicts"].([]any)) != 1 {
		t.Fatal("ожидали конфликт с парком")
	}

	slots, _ := call("find_free_slots", map[string]any{"date": "2026-10-28", "duration_minutes": 60})
	if len(slots["slots"].([]any)) != 2 {
		t.Fatal("ожидали окна до и после встречи:", slots)
	}

	events, _ := call("list_events", map[string]any{"from": "2026-10-28", "to": "2026-10-28"})
	if len(events["events"].([]any)) != 2 {
		t.Fatal("to-датой должен включаться весь день:", events)
	}

	// Ошибки модели должны возвращаться ей текстом, а не ронять сервер
	if _, isErr := call("create_event", map[string]any{"title": "x", "start": "послезавтра в 2"}); !isErr {
		t.Fatal("кривое время должно быть isError")
	}
	if _, isErr := call("create_event", map[string]any{"title": "x", "start": "2026-10-28"}); !isErr {
		t.Fatal("дата без времени должна быть isError")
	}
	if _, isErr := call("delete_event", map[string]any{"id": "nope"}); !isErr {
		t.Fatal("удаление несуществующего должно быть isError")
	}
	if _, isErr := call("delete_event", map[string]any{"id": ev["id"]}); isErr {
		t.Fatal("удаление существующего упало")
	}
}
