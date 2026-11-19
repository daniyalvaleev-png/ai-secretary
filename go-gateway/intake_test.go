package main

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

func useTestDB(t *testing.T) {
	t.Helper()
	old, _ := os.Getwd()
	os.Chdir(t.TempDir())
	t.Cleanup(func() { db.Close(); os.Chdir(old) })
	if err := initDB(); err != nil {
		t.Fatal(err)
	}
}

func rawMail(from, subject, body string, headers ...string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\nSubject: " + subject + "\r\n")
	for _, h := range headers {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n" + body)
	return []byte(b.String())
}

// harness — приёмная с подделками вместо модели и Telegram: считаем, сколько раз их дёрнули
type harness struct {
	in       *Intake
	clock    time.Time
	analyzed int
	alerts   int
	notices  []string
	hub      *EventHub
	nextUID  uint32
	failNext bool
}

func newHarness(t *testing.T) *harness {
	useTestDB(t)
	h := &harness{hub: NewEventHub(10000), clock: time.Date(2026, 11, 19, 12, 0, 0, 0, time.Local), nextUID: 100}
	g := NewGuard(GuardConfig{PerSenderHour: 5, FloodHour: 30, SummaryEvery: 15 * time.Minute})
	g.now = func() time.Time { return h.clock }
	g.Known = isKnownSender
	h.in = &Intake{
		Guard:  g,
		Events: h.hub,
		Analyze: func(from, subject, text string) (EmailDecision, error) {
			if h.failNext {
				return EmailDecision{}, errors.New("Ollama лежит")
			}
			h.analyzed++
			return EmailDecision{Urgency: "высокая", RequiresConfirmation: true, SuggestedAction: "Ответить"}, nil
		},
		Alert:  func(uint32, string, string, EmailDecision) { h.alerts++ },
		Notice: func(s string) { h.notices = append(h.notices, s) },
		Done:   markUIDProcessed,
	}
	return h
}

func (h *harness) receive(from, subject string, headers ...string) uint32 {
	h.nextUID++
	h.in.Handle(incoming{UID: h.nextUID, From: from, Subject: subject, Raw: rawMail(from, subject, "текст письма", headers...)})
	return h.nextUID
}

func (h *harness) events(typ string) int {
	evs, _ := h.hub.Wait(context.Background(), 0, time.Millisecond)
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func countStatus(t *testing.T, status string) int {
	t.Helper()
	emails, err := listEmails(status, 500)
	if err != nil {
		t.Fatal(err)
	}
	return len(emails)
}

// 200 писем с одного адреса: модель, Telegram и приложение видят только первые 5, остальное — молча в спам
func TestBomberFromOneAddress(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 200; i++ {
		h.receive("bomber@evil.ru", fmt.Sprintf("Письмо №%d", i))
	}

	if h.analyzed != 5 || h.alerts != 5 {
		t.Fatalf("модель %d раз, Telegram %d раз — а должно быть по 5", h.analyzed, h.alerts)
	}
	if len(h.notices) != 1 || !strings.Contains(h.notices[0], "спам-атак") {
		t.Fatalf("должна быть одна сводка, а не 195 сообщений: %q", h.notices)
	}
	if h.events("new_email") != 5 || h.events("flood") != 1 {
		t.Fatalf("приложение: новых писем %d, сводок %d", h.events("new_email"), h.events("flood"))
	}
	if countStatus(t, "spam") != 195 || countStatus(t, "pending") != 5 {
		t.Fatalf("в базе: спам %d, ждут решения %d", countStatus(t, "spam"), countStatus(t, "pending"))
	}
	// Фильтр списка в API принимает новый статус
	if !allowedStatuses["spam"] {
		t.Fatal("GET /api/emails?status=spam должен работать")
	}
	e, _ := getEmail(h.nextUID)
	if !e.Suspicious || !strings.Contains(e.SuspicionReason, "бомбер") {
		t.Fatalf("причина спама должна быть видна в приложении: %+v", e)
	}

	// Ошиблись — «Не спам» возвращает письмо в «ждут решения». С обычным письмом так нельзя
	app := &App{}
	if e, err := app.NotSpam(h.nextUID); err != nil || e.Status != "pending" {
		t.Fatal("не спам:", e.Status, err)
	}
	if _, err := app.NotSpam(h.nextUID); err == nil {
		t.Fatal("второй раз «Не спам» — письмо уже не в спаме, должна быть ошибка")
	}

	// Через 15 минут бомбер не унялся — ещё одна сводка, но не на каждое письмо
	h.clock = h.clock.Add(16 * time.Minute)
	for i := 0; i < 50; i++ {
		h.receive("bomber@evil.ru", "ещё")
	}
	if len(h.notices) != 2 {
		t.Fatalf("сводок должно быть 2, а не %d", len(h.notices))
	}

	// Прошёл час — счётчик отправителя обнулился, его письма снова разбираются
	h.clock = h.clock.Add(61 * time.Minute)
	h.receive("bomber@evil.ru", "Извините, это была ошибка")
	if h.analyzed != 6 {
		t.Fatal("через час отправитель должен снова проходить, analyzed =", h.analyzed)
	}
}

// Подписочный бомбер: сотни рассылок с разных адресов. Люди при этом должны доходить
func TestSubscriptionBomb(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 60; i++ {
		h.receive(fmt.Sprintf("news@shop%d.ru", i), "Подтвердите подписку",
			"List-Unsubscribe: <mailto:unsub@shop.ru>")
	}
	if h.analyzed != 30 {
		t.Fatal("до порога наплыва (30 в час) рассылки разбираются, дальше — нет; analyzed =", h.analyzed)
	}
	if countStatus(t, "spam") != 30 {
		t.Fatal("спам:", countStatus(t, "spam"))
	}

	// Посреди наплыва пишет живой человек — его письмо разбирается и уведомление приходит
	alerts := h.alerts
	h.receive("masha@mail.ru", "Встреча завтра")
	if h.analyzed != 31 || h.alerts != alerts+1 {
		t.Fatal("письмо человека во время наплыва потерялось")
	}

	// Роботы без List-Unsubscribe, но с адресом noreply@ — тоже рассылка
	h.receive("noreply@service.ru", "Ваш код")
	if h.analyzed != 31 {
		t.Fatal("noreply@ во время наплыва должен уйти в спам")
	}
}

// Бомбер шлёт с сотен «человеческих» адресов: при сильном наплыве пропускаем только знакомых
func TestHeavyFloodLetsOnlyKnownSenders(t *testing.T) {
	h := newHarness(t)
	// Мама — знакомая: ей мы уже отвечали
	saveEmail(StoredEmail{UID: 1, From: "mama@mail.ru", Subject: "Привет", Status: "sent", ReceivedAt: time.Now()})

	for i := 0; i < 100; i++ {
		h.receive(fmt.Sprintf("user%d@random.ru", i), "привет")
	}
	before := h.analyzed

	h.receive("Mama@Mail.ru", "Позвони мне") // регистр адреса не важен
	if h.analyzed != before+1 {
		t.Fatal("знакомый отправитель должен проходить и во время сильного наплыва")
	}
	h.receive("stranger@new.ru", "Здравствуйте")
	if h.analyzed != before+1 {
		t.Fatal("незнакомый при сильном наплыве — в спам")
	}
}

// Модель упала — письмо обработается на следующем круге, но в счётчиках бомбера посчитано один раз
func TestRetryIsNotCountedTwice(t *testing.T) {
	h := newHarness(t)
	h.failNext = true
	for i := 0; i < 10; i++ {
		h.in.Handle(incoming{UID: 7, From: "friend@mail.ru", Subject: "Кино", Raw: rawMail("friend@mail.ru", "Кино", "го")})
	}
	if isProcessed(7) {
		t.Fatal("письмо, которое не удалось разобрать, нельзя помечать обработанным")
	}
	h.failNext = false
	h.in.Handle(incoming{UID: 7, From: "friend@mail.ru", Subject: "Кино", Raw: rawMail("friend@mail.ru", "Кино", "го")})
	for i := 0; i < 4; i++ {
		h.receive("friend@mail.ru", "ещё")
	}
	if h.analyzed != 5 || countStatus(t, "spam") != 0 {
		t.Fatalf("10 неудачных попыток посчитались как 10 писем: analyzed=%d spam=%d", h.analyzed, countStatus(t, "spam"))
	}
}

func TestIsAutomated(t *testing.T) {
	hdr := func(k, v string) mail.Header { return mail.Header{k: {v}} }
	cases := []struct {
		addr string
		h    mail.Header
		want bool
	}{
		{"masha@mail.ru", nil, false},
		{"news@shop.ru", hdr("List-Unsubscribe", "<mailto:x@y>"), true},
		{"a@b.ru", hdr("Precedence", "bulk"), true},
		{"a@b.ru", hdr("Auto-Submitted", "auto-generated"), true},
		{"a@b.ru", hdr("Auto-Submitted", "no"), false},
		{"no-reply@bank.ru", nil, true},
		{"notifications@github.com", nil, true},
	}
	for _, c := range cases {
		if got := isAutomated(c.addr, c.h); got != c.want {
			t.Errorf("isAutomated(%q, %v) = %v", c.addr, c.h, got)
		}
	}
}

// Огромное письмо: качаем только первые 512 КБ — вложение обрезано, но текст письма достаётся
func TestBigEmailIsCutButTextSurvives(t *testing.T) {
	sec, err := imap.ParseBodySectionName(fetchBodyItem)
	if err != nil || !sec.Peek || len(sec.Partial) != 2 || sec.Partial[1] != fetchBodyLimit {
		t.Fatalf("запрос IMAP должен брать только первые %d байт: %+v %v", fetchBodyLimit, sec, err)
	}

	attachment := strings.Repeat("QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFB\r\n", 300000) // ~23 МБ
	raw := "From: a@b.ru\r\nSubject: Отчёт\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=XYZ\r\n\r\n" +
		"--XYZ\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nПривет! Отчёт во вложении.\r\n" +
		"--XYZ\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\n\r\n" + attachment +
		"--XYZ--\r\n"
	cut := []byte(raw)[:fetchBodyLimit] // столько отдаст сервер

	if text := extractPlainText(cut); !strings.Contains(text, "Отчёт во вложении") {
		t.Fatalf("текст потерялся: %q", text)
	}
}
