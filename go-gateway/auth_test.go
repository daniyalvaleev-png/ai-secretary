package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// newTestAPI — чистая база во временной папке, один пользователь и тот же обработчик, что в бою
func newTestAPI(t *testing.T, origins ...string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { db.Close(); os.Chdir(old) })

	if err := initDB(); err != nil {
		t.Fatal(err)
	}
	if err := createUser("Daniyal", "correct-horse-1"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newAPIHandler(origins))
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, token, body string, headers ...string) (int, map[string]any, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, resp.Header
}

func login(t *testing.T, srv *httptest.Server, user, pass string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"login": user, "password": pass})
	code, out, _ := call(t, srv, "POST", "/api/login", "", string(b))
	token, _ := out["token"].(string)
	return code, token
}

func TestLoginFlow(t *testing.T) {
	srv := newTestAPI(t)

	if code, _, _ := call(t, srv, "GET", "/api/emails", "", ""); code != 401 {
		t.Fatal("без входа ожидали 401, получили", code)
	}
	if code, _, _ := call(t, srv, "GET", "/api/health", "", ""); code != 200 {
		t.Fatal("health должен быть открыт:", code)
	}

	// Логин без учёта регистра и пробелов, пароль — с учётом
	code, token := login(t, srv, "  DANIYAL ", "correct-horse-1")
	if code != 200 || len(token) < 40 {
		t.Fatal("вход не удался:", code, token)
	}

	if code, out, _ := call(t, srv, "GET", "/api/me", token, ""); code != 200 || out["login"] != "daniyal" {
		t.Fatal("/api/me:", code, out)
	}
	if code, _, _ := call(t, srv, "GET", "/api/emails", token, ""); code != 200 {
		t.Fatal("с токеном письма должны отдаваться:", code)
	}

	// В базе нет самого токена — только хэш
	var stored string
	db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored)
	if stored == token || stored != hashToken(token) {
		t.Fatal("в базе должен лежать хэш токена, а не сам токен")
	}

	if code, _, _ := call(t, srv, "POST", "/api/logout", token, ""); code != 204 {
		t.Fatal("logout:", code)
	}
	if code, _, _ := call(t, srv, "GET", "/api/me", token, ""); code != 401 {
		t.Fatal("после выхода токен должен перестать работать:", code)
	}
}

func TestWrongPasswordAndBruteForce(t *testing.T) {
	srv := newTestAPI(t)

	code, _, _ := call(t, srv, "POST", "/api/login", "", `{"login":"daniyal","password":"wrong-password"}`)
	_, outNoUser, _ := call(t, srv, "POST", "/api/login", "", `{"login":"nobody","password":"wrong-password"}`)
	_, outWrongPass, _ := call(t, srv, "POST", "/api/login", "", `{"login":"daniyal","password":"wrong-password"}`)
	if code != 401 || outNoUser["error"] != outWrongPass["error"] {
		t.Fatal("ответы на «нет логина» и «не тот пароль» должны совпадать:", outNoUser, outWrongPass)
	}

	// Ещё 3 ошибки — итого 5 по этому логину, дальше блок, причём даже с правильным паролем
	for i := 0; i < 3; i++ {
		login(t, srv, "daniyal", "wrong-password")
	}
	if code, _ := login(t, srv, "daniyal", "correct-horse-1"); code != 429 {
		t.Fatal("после 5 ошибок ожидали 429, получили", code)
	}

	if code, _, _ := call(t, srv, "POST", "/api/login", "", `не json`); code != 400 {
		t.Fatal("кривое тело:", code)
	}
	big := `{"login":"x","password":"` + strings.Repeat("a", 100<<10) + `"}`
	if code, _, _ := call(t, srv, "POST", "/api/login", "", big); code != 400 {
		t.Fatal("тело больше 64 КБ должно отбрасываться:", code)
	}
}

func TestSessionExpiryAndPasswordReset(t *testing.T) {
	srv := newTestAPI(t)
	_, token := login(t, srv, "daniyal", "correct-horse-1")

	// Протухшая сессия не пускает
	db.Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(-time.Minute).Unix())
	if code, _, _ := call(t, srv, "GET", "/api/me", token, ""); code != 401 {
		t.Fatal("протухшая сессия пустила:", code)
	}
	cleanupSessions()
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 0 {
		t.Fatal("cleanupSessions не удалил протухшие:", n)
	}

	// Смена пароля выкидывает со всех устройств
	_, token = login(t, srv, "daniyal", "correct-horse-1")
	if err := setPassword("daniyal", "new-password-2"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := call(t, srv, "GET", "/api/me", token, ""); code != 401 {
		t.Fatal("после смены пароля старая сессия жива:", code)
	}
	if code, _ := login(t, srv, "daniyal", "new-password-2"); code != 200 {
		t.Fatal("новый пароль не подошёл:", code)
	}

	if err := createUser("daniyal", "another-password"); err != ErrUserExists {
		t.Fatal("повторный логин:", err)
	}
	if err := createUser("x", "another-password"); err == nil {
		t.Fatal("короткий логин должен отклоняться")
	}
	if err := createUser("petya", "short"); err == nil {
		t.Fatal("короткий пароль должен отклоняться")
	}
}

func TestCORS(t *testing.T) {
	srv := newTestAPI(t, "http://localhost:5000")

	// Preflight с разрешённого адреса
	code, _, h := call(t, srv, "OPTIONS", "/api/emails", "", "", "Origin", "http://localhost:5000", "Access-Control-Request-Method", "GET")
	if code != 204 || h.Get("Access-Control-Allow-Origin") != "http://localhost:5000" || !strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatal("preflight:", code, h)
	}

	// Чужой сайт заголовков не получает — браузер не отдаст ему ответ
	_, _, h = call(t, srv, "GET", "/api/health", "", "", "Origin", "https://evil.example")
	if h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("чужому сайту разрешили CORS")
	}

	// Обычный запрос с разрешённого адреса тоже несёт заголовок
	_, token := login(t, srv, "daniyal", "correct-horse-1")
	_, _, h = call(t, srv, "GET", "/api/me", token, "", "Origin", "http://localhost:5000")
	if h.Get("Access-Control-Allow-Origin") != "http://localhost:5000" {
		t.Fatal("нет CORS на обычном запросе")
	}
}