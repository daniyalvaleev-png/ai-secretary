package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEventHubWait(t *testing.T) {
	h := NewEventHub(3)
	ctx := context.Background()

	// Пусто — ждём до таймаута и получаем ничего
	start := time.Now()
	if evs, last := h.Wait(ctx, 0, 50*time.Millisecond); len(evs) != 0 || last != 0 {
		t.Fatal("пустая лента:", evs, last)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("должен был подождать")
	}

	// Событие посреди ожидания будит сразу, а не через таймаут
	go func() {
		time.Sleep(20 * time.Millisecond)
		h.Publish(Event{Type: "new_email", UID: 7})
	}()
	start = time.Now()
	evs, last := h.Wait(ctx, 0, 5*time.Second)
	if len(evs) != 1 || evs[0].UID != 7 || last != 1 || time.Since(start) > time.Second {
		t.Fatal("разбудить при событии:", evs, last, time.Since(start))
	}

	// Хранятся только последние 3
	for i := 0; i < 5; i++ {
		h.Publish(Event{Type: "status", UID: uint32(i)})
	}
	if evs, last = h.Wait(ctx, 0, time.Millisecond); len(evs) != 3 || evs[0].ID != 4 || last != 6 {
		t.Fatal("буфер:", evs, last)
	}

	// Шлюз перезапустился (у клиента номер больше нашего) — отвечаем сразу, без ожидания
	start = time.Now()
	if evs, last = h.Wait(ctx, 100, 5*time.Second); len(evs) != 0 || last != 6 || time.Since(start) > time.Second {
		t.Fatal("после перезапуска:", evs, last)
	}
}

// Приложение слушает ленту и видит: подтвердил → пишу черновик → черновик готов
func TestAPIEvents(t *testing.T) {
	app, _, _, rec := newTestApp(t)
	hub := NewEventHub(100)
	app.Events = hub
	app.Notify = multiNotifier{hub, rec} // и в ленту, и «в Telegram»
	if err := createUser("daniyal", "correct-horse-1"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newAPIHandler(nil, app))
	defer srv.Close()
	_, token := login(t, srv, "daniyal", "correct-horse-1")

	if code, _, _ := call(t, srv, "GET", "/api/events", "", ""); code != 401 {
		t.Fatal("без входа:", code)
	}
	if code, _, _ := call(t, srv, "GET", "/api/events?after=abc", token, ""); code != 400 {
		t.Fatal("кривой after:", code)
	}

	// Без after — просто номер последнего события, с него начинаем слушать
	code, out, _ := call(t, srv, "GET", "/api/events", token, "")
	if code != 200 || out["last_id"] != float64(0) {
		t.Fatal("старт:", code, out)
	}

	hub.NewEmail(StoredEmail{UID: 1, From: "a@b.ru", Subject: "Кино", Urgency: "высокая", Status: "pending"})
	call(t, srv, "POST", "/api/emails/1/confirm", token, "")
	app.Wait()

	_, out, _ = call(t, srv, "GET", "/api/events?after=0", token, "")
	var got []string
	for _, e := range out["events"].([]any) {
		m := e.(map[string]any)
		got = append(got, fmt.Sprintf("%s/%s/%v", m["type"], m["status"], m["important"]))
	}
	want := "[new_email/pending/true status/drafting/false draft_ready/draft_ready/true]"
	if fmt.Sprint(got) != want {
		t.Fatalf("лента:\n got %v\nwant %s", got, want)
	}
	if out["last_id"] != float64(3) {
		t.Fatal("last_id:", out["last_id"])
	}
	// В Telegram результат тоже ушёл
	if rec.all() != "draft_ready:черновик №1" {
		t.Fatal("telegram:", rec.all())
	}

	// Ожидание прерывается новым событием — ответ приходит, не дожидаясь 25 секунд
	go func() {
		time.Sleep(100 * time.Millisecond)
		hub.NewEmail(StoredEmail{UID: 2, Subject: "Ещё одно", Suspicious: true})
	}()
	start := time.Now()
	_, out, _ = call(t, srv, "GET", "/api/events?after=3", token, "")
	evs := out["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["text"] != "🚨 Подозрительное письмо" || time.Since(start) > 5*time.Second {
		t.Fatal("long polling:", out, time.Since(start))
	}
}
