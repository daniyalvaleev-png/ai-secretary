package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Скрипт долбит API — после лимита получает 429 с подсказкой, через сколько можно
func TestRateLimit(t *testing.T) {
	newTestApp(t)
	t.Setenv("API_RATE_PER_MIN", "5")
	srv := httptest.NewServer(newAPIHandler([]string{"http://localhost:5000"}, &App{}))
	defer srv.Close()

	for i := 0; i < 5; i++ {
		if code, _, _ := call(t, srv, "GET", "/api/health", "", ""); code != 200 {
			t.Fatalf("запрос %d: %d", i+1, code)
		}
	}
	code, out, hdr := call(t, srv, "GET", "/api/health", "", "", "Origin", "http://localhost:5000")
	if code != 429 || hdr.Get("Retry-After") == "" || !strings.Contains(out["error"].(string), "подожди") {
		t.Fatalf("6-й запрос должен получить 429: %d %v %v", code, out, hdr)
	}
	// Браузер должен суметь прочитать 429, иначе приложение покажет «сеть упала»
	if hdr.Get("Access-Control-Allow-Origin") != "http://localhost:5000" {
		t.Fatal("на 429 нет CORS-заголовка")
	}
}

func TestRateLimiterWindow(t *testing.T) {
	l := newRateLimiter(2)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.allow("ip")
	l.allow("ip")
	if ok, wait := l.allow("ip"); ok || wait <= 0 || wait > time.Minute {
		t.Fatal("третий запрос за минуту должен быть отклонён:", ok, wait)
	}
	if ok, _ := l.allow("другой ip"); !ok {
		t.Fatal("лимит у каждого адреса свой")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := l.allow("ip"); !ok {
		t.Fatal("через минуту снова можно")
	}
}

func TestSecurityHeaders(t *testing.T) {
	newTestApp(t)
	srv := httptest.NewServer(newAPIHandler(nil, &App{}))
	defer srv.Close()

	_, _, hdr := call(t, srv, "GET", "/api/health", "", "")
	for k, v := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "no-store",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if hdr.Get(k) != v {
			t.Errorf("%s = %q, а надо %q", k, hdr.Get(k), v)
		}
	}
	if !strings.Contains(hdr.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Error("нет Content-Security-Policy")
	}
}

func TestStreamLimiter(t *testing.T) {
	s := newStreamLimiter(3)
	var releases []func()
	for i := 0; i < 3; i++ {
		r, ok := s.acquire(1)
		if !ok {
			t.Fatal("первые три должны пройти")
		}
		releases = append(releases, r)
	}
	if _, ok := s.acquire(1); ok {
		t.Fatal("четвёртая прослушка у того же пользователя — нельзя")
	}
	if _, ok := s.acquire(2); !ok {
		t.Fatal("у другого пользователя свой лимит")
	}
	releases[0]()
	if _, ok := s.acquire(1); !ok {
		t.Fatal("закрыли одну — можно открыть новую")
	}
}

// SQL-инъекции: все запросы к базе параметризованы, поэтому «'; DROP TABLE» — просто текст.
// Тест закрепляет это: если кто-то потом склеит SQL из строк, он упадёт.
func TestSQLInjection(t *testing.T) {
	app, _, _, _ := newTestApp(t)
	nasty := []string{
		`'); DROP TABLE emails;--`,
		`" OR "1"="1`,
		`x' UNION SELECT login, password_hash FROM users--`,
		`1; DELETE FROM sessions`,
	}

	// 1. Хитрые строки в письме сохраняются и читаются как обычный текст, таблицы целы
	for i, s := range nasty {
		uid := uint32(500 + i)
		saveEmail(StoredEmail{UID: uid, From: s, Subject: s, Body: s, Status: "pending", ReceivedAt: time.Now()})
		e, ok := getEmail(uid)
		if !ok || e.Subject != s || e.Body != s || e.From != s {
			t.Fatalf("строка %q исказилась: %+v", s, e)
		}
	}
	if emails, err := listEmails("", 100); err != nil || len(emails) != len(nasty)+1 {
		t.Fatal("таблица emails пострадала:", len(emails), err)
	}

	// 2. Черновик с SQL — тоже просто текст
	transitionStatus(500, "draft_ready", "pending")
	if e, err := app.EditDraft(500, nasty[0]); err != nil || e.Draft != nasty[0] {
		t.Fatal("черновик:", e.Draft, err)
	}

	// 3. Через API: кривые параметры отсекаются до базы, вход с инъекцией не проходит
	if err := createUser("daniyal", "correct-horse-1"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newAPIHandler(nil, app))
	defer srv.Close()
	_, token := login(t, srv, "daniyal", "correct-horse-1")

	for _, path := range []string{
		"/api/emails?status=" + url.QueryEscape(`pending' OR '1'='1`),
		"/api/emails?limit=" + url.QueryEscape(`1; DROP TABLE emails`),
		"/api/emails/" + url.PathEscape(`1 OR 1=1`),
	} {
		if code, _, _ := call(t, srv, "GET", path, token, ""); code != http.StatusBadRequest {
			t.Errorf("%s → %d, а надо 400", path, code)
		}
	}
	for _, s := range []string{`daniyal' --`, `' OR '1'='1`, `daniyal" OR "1"="1`} {
		if code, _ := login(t, srv, s, "что угодно"); code != http.StatusUnauthorized {
			t.Errorf("вход с логином %q → %d", s, code)
		}
	}
	if code, _ := login(t, srv, "daniyal", `' OR '1'='1`); code != http.StatusUnauthorized {
		t.Error("вход с паролем-инъекцией прошёл!")
	}
	if code, _, _ := call(t, srv, "GET", "/api/me", token, ""); code != 200 {
		t.Fatal("таблицы пользователей и сессий пострадали:", code)
	}
}

// Шлюз без правильного ключа к агенту — понятная ошибка, а не «что-то сломалось»
func TestAgentKeyMismatch(t *testing.T) {
	app, _, _, rec := newTestApp(t)
	app.AgentKey = "неправильный"
	app.Confirm(1)
	app.Wait()
	if status(1) != "draft_failed" || !strings.Contains(rec.all(), "draft_failed") {
		t.Fatal("с чужим ключом черновик не должен написаться:", status(1), rec.all())
	}
	if _, err := requestDraft(app.AgentURL, "неправильный", StoredEmail{UID: 1}); err == nil || !strings.Contains(err.Error(), "AGENT_API_KEY") {
		t.Fatal("ошибка должна подсказывать про AGENT_API_KEY:", err)
	}
}
