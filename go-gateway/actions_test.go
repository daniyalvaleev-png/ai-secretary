package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Подделки: агент, почта, календарь и уведомления ---

type fakeAgent struct {
	mu      sync.Mutex
	fail    bool
	meeting *draftMeeting
	calls   int
}

func (f *fakeAgent) server(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		if f.fail {
			http.Error(w, "boom", 500)
			return
		}
		json.NewEncoder(w).Encode(draftResponse{
			Draft: fmt.Sprintf("черновик №%d", f.calls), Attempts: 1, Meeting: f.meeting,
			CalendarNote: map[bool]string{true: "Календарь: в среду в 14:00 — СВОБОДНО.", false: ""}[f.meeting != nil],
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// set меняет поведение подделки под замком — сервер агента читает поля из другой горутины
func (f *fakeAgent) set(change func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change()
}

type fakeMailer struct {
	mu   sync.Mutex
	fail bool
	sent []string
}

func (m *fakeMailer) SendReply(e StoredEmail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("smtp упал")
	}
	m.sent = append(m.sent, e.Draft)
	return nil
}

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, s)
}

func (r *recorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.events, "|")
}

func (r *recorder) DraftReady(e StoredEmail, _ string)  { r.add("draft_ready:" + e.Draft) }
func (r *recorder) DraftFailed(e StoredEmail, _ string) { r.add("draft_failed") }
func (r *recorder) Sent(e StoredEmail)                  { r.add("sent") }
func (r *recorder) SendFailed(e StoredEmail, _ error)   { r.add("send_failed") }
func (r *recorder) CalendarAdded(e StoredEmail, c createdEvent) {
	r.add(fmt.Sprintf("cal_added:%s:conflicts=%d", c.Event.ID, len(c.Conflicts)))
}
func (r *recorder) CalendarFailed(e StoredEmail, _ error) { r.add("cal_failed") }

// fakeCalendarMCP — настоящий MCP-сервер с двумя инструментами, как у calendar-mcp
func fakeCalendarMCP(t *testing.T) *CalendarClient {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-calendar", Version: "v0"}, nil)

	type createIn struct {
		Title       string `json:"title"`
		Start       string `json:"start"`
		End         string `json:"end"`
		Description string `json:"description,omitempty"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "create_event"}, func(_ context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{
			"event":     map[string]string{"id": "ev1", "title": in.Title, "start": in.Start, "end": in.End},
			"conflicts": []map[string]string{{"title": "Стоматолог", "start": in.Start, "end": in.End}},
		}, nil
	})

	type listIn struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "list_events"}, func(_ context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, map[string]any, error) {
		if in.From == "кривая" {
			return nil, nil, errors.New("не понял время")
		}
		return nil, map[string]any{"events": []map[string]string{{"id": "ev1", "title": "Парк", "start": in.From + " 14:00", "end": in.From + " 15:00", "weekday": "среда"}}}, nil
	})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cal-key" {
			http.Error(w, "нет ключа", 401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return &CalendarClient{URL: srv.URL, APIKey: "cal-key"}
}

// newTestApp — чистая база во временной папке, письмо UID 1 в статусе pending и App на подделках
func newTestApp(t *testing.T) (*App, *fakeAgent, *fakeMailer, *recorder) {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { db.Close(); os.Chdir(old) })

	if err := initDB(); err != nil {
		t.Fatal(err)
	}
	saveEmail(StoredEmail{UID: 1, From: "friend@mail.ru", Subject: "Парк", Body: "го в парк", ReceivedAt: time.Now(), Urgency: "высокая"})

	agent := &fakeAgent{}
	mailer := &fakeMailer{}
	rec := &recorder{}
	app := &App{AgentURL: agent.server(t), Mailer: mailer, Calendar: fakeCalendarMCP(t), Notify: rec}
	return app, agent, mailer, rec
}

func status(uid uint32) string {
	e, _ := getEmail(uid)
	return e.Status
}

func TestActionsHappyPath(t *testing.T) {
	app, _, mailer, rec := newTestApp(t)

	if _, err := app.Send(1); !errors.As(err, &ErrWrongStatus{}) {
		t.Fatal("отправить до черновика нельзя:", err)
	}

	e, err := app.Confirm(1)
	if err != nil || e.Status != "drafting" {
		t.Fatal(e.Status, err)
	}
	if _, err := app.Confirm(1); !errors.As(err, &ErrWrongStatus{}) {
		t.Fatal("второе подтверждение должно отклоняться:", err)
	}
	app.Wait()
	if status(1) != "draft_ready" || !strings.Contains(rec.all(), "draft_ready:черновик №1") {
		t.Fatal("черновик:", status(1), rec.all())
	}

	// Переписать → новый черновик
	app.Redraft(1)
	app.Wait()
	if e, _ := getEmail(1); e.Draft != "черновик №2" {
		t.Fatal("redraft:", e.Draft)
	}

	// Поправить руками → уйдёт именно исправленный текст
	if _, err := app.EditDraft(1, "   "); !errors.Is(err, ErrBadDraft) {
		t.Fatal("пустой черновик:", err)
	}
	if _, err := app.EditDraft(1, "Привет! Давай в 15:00."); err != nil {
		t.Fatal(err)
	}

	app.Send(1)
	app.Wait()
	if status(1) != "sent" || len(mailer.sent) != 1 || mailer.sent[0] != "Привет! Давай в 15:00." {
		t.Fatal("отправка:", status(1), mailer.sent)
	}

	// После отправки править и переписывать нельзя
	if _, err := app.EditDraft(1, "поздно"); !errors.As(err, &ErrWrongStatus{}) {
		t.Fatal("правка после отправки:", err)
	}
	if _, err := app.Redraft(1); !errors.As(err, &ErrWrongStatus{}) {
		t.Fatal("redraft после отправки:", err)
	}
	if _, err := app.Confirm(999); !errors.Is(err, ErrEmailNotFound) {
		t.Fatal("несуществующее письмо:", err)
	}
}

func TestActionsFailures(t *testing.T) {
	app, agent, mailer, rec := newTestApp(t)

	// Агент упал → draft_failed, «Повторить» работает
	agent.set(func() { agent.fail = true })
	app.Confirm(1)
	app.Wait()
	if status(1) != "draft_failed" || !strings.Contains(rec.all(), "draft_failed") {
		t.Fatal("агент упал:", status(1), rec.all())
	}
	agent.set(func() { agent.fail = false })
	app.Redraft(1)
	app.Wait()
	if status(1) != "draft_ready" {
		t.Fatal("повтор после падения:", status(1))
	}

	// Почта упала → обратно draft_ready, можно отправить ещё раз
	mailer.mu.Lock()
	mailer.fail = true
	mailer.mu.Unlock()
	app.Send(1)
	app.Wait()
	if status(1) != "draft_ready" || !strings.Contains(rec.all(), "send_failed") {
		t.Fatal("почта упала:", status(1), rec.all())
	}

	// Не настроено
	noMail := &App{AgentURL: app.AgentURL}
	if _, err := noMail.Send(1); !errors.As(err, &ErrNotConfigured{}) {
		t.Fatal("без почты:", err)
	}
	if _, err := noMail.AddToCalendar(1); !errors.As(err, &ErrNotConfigured{}) {
		t.Fatal("без календаря:", err)
	}

	// Skip из draft_ready
	if _, err := app.Skip(1); err != nil || status(1) != "skipped" {
		t.Fatal("skip:", status(1), err)
	}
}

func TestActionsCalendar(t *testing.T) {
	app, agent, _, rec := newTestApp(t)

	app.Confirm(1)
	app.Wait()
	if _, err := app.AddToCalendar(1); !errors.Is(err, ErrNoMeeting) {
		t.Fatal("без встречи:", err)
	}

	agent.set(func() {
		agent.meeting = &draftMeeting{Title: "Парк", Start: "2026-11-18 14:00", End: "2026-11-18 15:00"}
	})
	app.Redraft(1)
	app.Wait()

	if _, err := app.AddToCalendar(1); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AddToCalendar(1); !errors.Is(err, ErrAlreadyInCalendar) {
		t.Fatal("второй клик:", err)
	}
	app.Wait()

	if e, _ := getEmail(1); e.CalendarEventID != "ev1" || !strings.Contains(rec.all(), "cal_added:ev1:conflicts=1") {
		t.Fatal("календарь:", e.CalendarEventID, rec.all())
	}
}
