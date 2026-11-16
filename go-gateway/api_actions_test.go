package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Те же действия, но через HTTP — так их будет вызывать приложение
func TestAPIActions(t *testing.T) {
	app, agent, mailer, _ := newTestApp(t)
	if err := createUser("daniyal", "correct-horse-1"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newAPIHandler(nil, app))
	defer srv.Close()

	_, token := login(t, srv, "daniyal", "correct-horse-1")

	// Без входа действия закрыты
	if code, _, _ := call(t, srv, "POST", "/api/emails/1/confirm", "", ""); code != 401 {
		t.Fatal("без входа:", code)
	}

	code, out, _ := call(t, srv, "POST", "/api/emails/1/confirm", token, "")
	if code != 202 || out["status"] != "drafting" {
		t.Fatal("confirm:", code, out)
	}
	code, out, _ = call(t, srv, "POST", "/api/emails/1/confirm", token, "")
	if code != 409 || out["status"] != "drafting" {
		t.Fatal("повторный confirm должен дать 409 и текущий статус:", code, out)
	}
	app.Wait()

	// Приложение узнаёт результат, перечитывая письмо
	_, out, _ = call(t, srv, "GET", "/api/emails/1", token, "")
	if out["status"] != "draft_ready" || out["draft"] != "черновик №1" {
		t.Fatal("после черновика:", out)
	}

	if code, _, _ := call(t, srv, "POST", "/api/emails/1/draft", token, `{"draft":""}`); code != 400 {
		t.Fatal("пустой черновик:", code)
	}
	code, out, _ = call(t, srv, "POST", "/api/emails/1/draft", token, `{"draft":"Давай в 15:00"}`)
	if code != 200 || out["draft"] != "Давай в 15:00" {
		t.Fatal("правка черновика:", code, out)
	}

	if code, _, _ := call(t, srv, "POST", "/api/emails/1/calendar", token, ""); code != 409 {
		t.Fatal("календарь без встречи:", code)
	}

	if code, _, _ := call(t, srv, "POST", "/api/emails/1/send", token, ""); code != 202 {
		t.Fatal("send:", code)
	}
	app.Wait()
	if status(1) != "sent" || mailer.sent[0] != "Давай в 15:00" {
		t.Fatal("отправка:", status(1), mailer.sent)
	}

	if code, _, _ := call(t, srv, "POST", "/api/emails/999/reject", token, ""); code != 404 {
		t.Fatal("нет письма:", code)
	}
	if code, _, _ := call(t, srv, "POST", "/api/emails/abc/reject", token, ""); code != 400 {
		t.Fatal("кривой uid:", code)
	}

	// Старый «поставь любой статус» больше не существует
	if code, _, _ := call(t, srv, "POST", "/api/emails/1/status", token, `{"status":"draft_ready"}`); code != 404 && code != 405 {
		t.Fatal("POST /status должен исчезнуть:", code)
	}

	// Календарь: список событий
	code, out, _ = call(t, srv, "GET", "/api/calendar?from=2026-11-18&to=2026-11-19", token, "")
	events, _ := out["events"].([]any)
	if code != 200 || len(events) != 1 {
		t.Fatal("календарь:", code, out)
	}
	if code, _, _ := call(t, srv, "GET", "/api/calendar?from=кривая", token, ""); code != 502 {
		t.Fatal("ошибка календаря должна быть 502:", code)
	}

	// Встреча → «В календарь» через API
	saveEmail(StoredEmail{UID: 2, From: "a@b.c", Subject: "Созвон", Body: "x", ReceivedAt: time.Now(), Urgency: "высокая"})
	agent.set(func() {
		agent.meeting = &draftMeeting{Title: "Созвон", Start: "2026-11-18 14:00", End: "2026-11-18 15:00"}
	})
	call(t, srv, "POST", "/api/emails/2/confirm", token, "")
	app.Wait()
	if code, _, _ := call(t, srv, "POST", "/api/emails/2/calendar", token, ""); code != 202 {
		t.Fatal("в календарь:", code)
	}
	app.Wait()
	if e, _ := getEmail(2); e.CalendarEventID != "ev1" {
		t.Fatal("событие не создалось:", e.CalendarEventID)
	}

	// Без календаря — 503
	noCal := httptest.NewServer(newAPIHandler(nil, &App{}))
	defer noCal.Close()
	_, token2 := login(t, noCal, "daniyal", "correct-horse-1")
	if code, _, _ := call(t, noCal, "GET", "/api/calendar", token2, ""); code != 503 {
		t.Fatal("календарь не настроен:", code)
	}
}

// Telegram-кнопки идут через те же действия, а результат приходит сообщением
func TestBotButtons(t *testing.T) {
	app, _, _, _ := newTestApp(t)

	var tgLog strings.Builder // все вызовы Telegram API подряд
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		tgLog.WriteString(method + " " + string(b) + "\n")
		io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer tg.Close()
	tgHTTP = &http.Client{Transport: redirectTransport(tg.URL)}

	bot := &Bot{Token: "T", ChatID: "42", App: app}
	app.Notify = bot

	press := func(data string, chat int64) {
		cb := &tgCallback{ID: "cb", Data: data}
		cb.Message = &struct {
			MessageID int64  `json:"message_id"`
			Text      string `json:"text"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		}{MessageID: 7, Text: "📩 Письмо"}
		cb.Message.Chat.ID = chat
		bot.handleCallback(cb)
	}

	press("confirm:1", 666)
	if !strings.Contains(tgLog.String(), "Нет доступа") || status(1) != "pending" {
		t.Fatal("чужой чат:", tgLog.String())
	}

	press("confirm:1", 42)
	app.Wait()
	log := tgLog.String()
	for _, want := range []string{"Пишу черновик", "editMessageText", "Черновик ответа", "черновик №1", "send:1", "redraft:1"} {
		if !strings.Contains(log, want) {
			t.Fatalf("нет %q в:\n%s", want, log)
		}
	}

	press("confirm:1", 42) // повторное нажатие
	if !strings.Contains(tgLog.String(), "Уже обработано: draft_ready") {
		t.Fatal("повтор:", tgLog.String())
	}

	press("show:1", 42)
	if !strings.Contains(tgLog.String(), "го в парк") {
		t.Fatal("показать письмо:", tgLog.String())
	}

	press("send:1", 42)
	app.Wait()
	if !strings.Contains(tgLog.String(), "Ответ на «Парк» отправлен") || status(1) != "sent" {
		t.Fatal("отправка через кнопку:", tgLog.String())
	}
}

type redirectTransport string

func (u redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	nr, _ := http.NewRequest(req.Method, string(u)+req.URL.Path, req.Body)
	nr.Header = req.Header
	return http.DefaultTransport.RoundTrip(nr)
}
