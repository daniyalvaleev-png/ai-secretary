package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Вход в API.
// Пароль хранится только как bcrypt-хэш. После входа выдаётся случайный токен сессии,
// а в базе лежит только его SHA-256: даже утёкшая база не даёт войти ни паролем, ни сессией.

const (
	sessionTTL    = 30 * 24 * time.Hour
	bcryptCost    = 12 // ~0.25 с на проверку: пользователю незаметно, перебору — больно
	minPasswordLn = 10
)

var loginRe = regexp.MustCompile(`^[a-z0-9_.-]{3,32}$`)

var authSchema = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		login         TEXT    NOT NULL UNIQUE,
		password_hash TEXT    NOT NULL,
		created_at    INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		token_hash   TEXT    PRIMARY KEY,
		user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at   INTEGER NOT NULL,
		expires_at   INTEGER NOT NULL
	)`,
}

type User struct {
	ID    int64  `json:"-"`
	Login string `json:"login"`
}

var ErrUserExists = errors.New("такой пользователь уже есть")

func validateCredentials(login, password string) (string, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	if !loginRe.MatchString(login) {
		return "", fmt.Errorf("логин: 3–32 символа, латиница, цифры и _ . -")
	}
	if len([]rune(password)) < minPasswordLn {
		return "", fmt.Errorf("пароль короче %d символов", minPasswordLn)
	}
	return login, nil
}

func createUser(login, password string) error {
	login, err := validateCredentials(login, password)
	if err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return err
	}

	_, err = db.Exec(`INSERT INTO users (login, password_hash, created_at) VALUES (?, ?, ?)`, login, string(hash), time.Now().Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrUserExists
	}
	return err
}

// setPassword меняет пароль и выкидывает пользователя со всех устройств
func setPassword(login, password string) error {
	login, err := validateCredentials(login, password)
	if err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return err
	}

	res, err := db.Exec(`UPDATE users SET password_hash = ? WHERE login = ?`, string(hash), login)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("пользователя %q нет", login)
	}

	_, err = db.Exec(`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE login = ?)`, login)
	return err
}

func countUsers() int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n
}

var (
	dummyHashOnce sync.Once
	dummyHash     []byte
)

// checkPassword. Если логина нет, всё равно гоняем bcrypt по фиктивному хэшу —
// иначе по скорости ответа можно узнать, какие логины существуют.
func checkPassword(login, password string) (User, bool) {
	login = strings.ToLower(strings.TrimSpace(login))

	var u User
	var hash string
	err := db.QueryRow(`SELECT id, login, password_hash FROM users WHERE login = ?`, login).Scan(&u.ID, &u.Login, &hash)
	if err != nil {
		dummyHashOnce.Do(func() { dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password"), bcryptCost) })
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return User{}, false
	}

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, false
	}
	return u, true
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newSession(userID int64) (token string, expires time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	expires = time.Now().Add(sessionTTL)

	_, err = db.Exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, time.Now().Unix(), expires.Unix())
	return token, expires, err
}

func userBySession(token string) (User, bool) {
	var u User
	err := db.QueryRow(`SELECT u.id, u.login FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, hashToken(token), time.Now().Unix()).Scan(&u.ID, &u.Login)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("⚠️ Проверка сессии: %v", err)
		}
		return User{}, false
	}
	return u, true
}

func deleteSession(token string) {
	if _, err := db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token)); err != nil {
		log.Printf("⚠️ Удаление сессии: %v", err)
	}
}

func cleanupSessions() {
	if _, err := db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix()); err != nil {
		log.Printf("⚠️ Чистка сессий: %v", err)
	}
}

// --- HTTP ---

type ctxKey int

const userCtxKey ctxKey = 0

func currentUser(r *http.Request) User {
	u, _ := r.Context().Value(userCtxKey).(User)
	return u
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if token, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(token)
	}
	return ""
}

// requireSession пускает только с действующей сессией и кладёт пользователя в контекст запроса
func requireSession(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := userBySession(bearerToken(r))
		if !ok {
			writeError(w, http.StatusUnauthorized, "нужно войти: POST /api/login")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userCtxKey, u)))
	})
}

// loginLimiter — защита от перебора паролей: не больше max неудачных попыток за window
type loginLimiter struct {
	mu     sync.Mutex
	fails  map[string][]time.Time
	max    int
	window time.Duration
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{fails: map[string][]time.Time{}, max: max, window: window}
}

func (l *loginLimiter) recent(key string) []time.Time {
	cutoff := time.Now().Add(-l.window)
	kept := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = kept
	return kept
}

func (l *loginLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) >= l.max
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.recent(key), time.Now())
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type authHandlers struct {
	perLogin *loginLimiter // против перебора пароля к одному логину
	perIP    *loginLimiter // против перебора логинов с одного адреса
}

func newAuthHandlers() *authHandlers {
	return &authHandlers{
		perLogin: newLoginLimiter(5, 15*time.Minute),
		perIP:    newLoginLimiter(20, 15*time.Minute),
	}
}

// POST /api/login {"login":"...","password":"..."} → {"token":"...","expires_at":"...","login":"..."}
func (a *authHandlers) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "тело должно быть JSON: {\"login\":\"...\",\"password\":\"...\"}")
		return
	}

	loginKey := "login:" + strings.ToLower(strings.TrimSpace(req.Login))
	ipKey := "ip:" + clientIP(r)
	if a.perLogin.blocked(loginKey) || a.perIP.blocked(ipKey) {
		writeError(w, http.StatusTooManyRequests, "слишком много неудачных попыток — подожди 15 минут")
		return
	}

	u, ok := checkPassword(req.Login, req.Password)
	if !ok {
		a.perLogin.fail(loginKey)
		a.perIP.fail(ipKey)
		log.Printf("🔒 Неудачный вход: %q с %s", req.Login, clientIP(r))
		// Одинаковый ответ для «нет логина» и «не тот пароль» — не подсказываем, что угадано
		writeError(w, http.StatusUnauthorized, "неверный логин или пароль")
		return
	}

	a.perLogin.reset(loginKey)
	token, expires, err := newSession(u.ID)
	if err != nil {
		log.Printf("⚠️ Создание сессии: %v", err)
		writeError(w, http.StatusInternalServerError, "ошибка базы")
		return
	}

	log.Printf("🔓 Вход: %s с %s", u.Login, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{
		"token":      token,
		"expires_at": expires.Format(time.RFC3339),
		"login":      u.Login,
	})
}

// POST /api/logout — закрывает текущую сессию
func (a *authHandlers) logout(w http.ResponseWriter, r *http.Request) {
	deleteSession(bearerToken(r))
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/me — кто я
func handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r))
}
