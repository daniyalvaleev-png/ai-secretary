package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	googleCredentialsFile = "credentials.json"  // тот же OAuth-клиент, что у go-gateway
	googleTokenFile       = "google_token.json" // появится после go run . google-auth
	googleCalendarScope   = "https://www.googleapis.com/auth/calendar.events"
)

func loadGoogleOAuthConfig() (*oauth2.Config, error) {
	b, err := os.ReadFile(googleCredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("нет %s — скопируй его из go-gateway: %w", googleCredentialsFile, err)
	}

	// calendar.events — только события: настройки календаря и доступы к нему этим токеном не тронуть
	return google.ConfigFromJSON(b, googleCalendarScope)
}

func newGoogleCalendar(calendarID string, loc *time.Location) (*GoogleCalendar, error) {
	cfg, err := loadGoogleOAuthConfig()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(googleTokenFile)
	if err != nil {
		return nil, fmt.Errorf("нет %s — сначала запусти: go run . google-auth", googleTokenFile)
	}

	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("%s битый: %w", googleTokenFile, err)
	}

	client := cfg.Client(context.Background(), &tok)
	client.Timeout = 20 * time.Second

	return &GoogleCalendar{Client: client, BaseURL: googleCalendarAPI, CalendarID: calendarID, Loc: loc}, nil
}

// runGoogleAuth — разовый вход через браузер. Сохраняет google_token.json.
// Схема "loopback": Google после входа редиректит на http://127.0.0.1:<порт>, а мы там ловим код.
func runGoogleAuth() error {
	cfg, err := loadGoogleOAuthConfig()
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0") // порт 0 — система сама даст свободный
	if err != nil {
		return err
	}
	defer ln.Close()

	cfg.RedirectURL = fmt.Sprintf("http://%s/callback", ln.Addr().String())

	// state — защита от подделанного редиректа, verifier — PKCE: код без него бесполезен
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return err
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	verifier := oauth2.GenerateVerifier()

	authURL := cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline, // нужен refresh token, чтобы работать без повторного входа
		oauth2.ApprovalForce,     // Google выдаёт refresh token только при явном согласии
		oauth2.S256ChallengeOption(verifier),
	)

	fmt.Println("\n👉 Открой ссылку в браузере и войди аккаунтом, ЧЕЙ КАЛЕНДАРЬ подключаем:")
	fmt.Println(authURL)
	fmt.Println("\nЖду ответа от Google…")

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		switch {
		case q.Get("state") != state:
			http.Error(w, "state не совпал", http.StatusBadRequest)
			done <- result{err: fmt.Errorf("state не совпал — кто-то подсунул чужой редирект")}
		case q.Get("error") != "":
			fmt.Fprintln(w, "Доступ не выдан, можно закрыть вкладку.")
			done <- result{err: fmt.Errorf("google: %s", q.Get("error"))}
		default:
			fmt.Fprintln(w, "Готово! Вкладку можно закрыть и вернуться в терминал.")
			done <- result{code: q.Get("code")}
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	var res result
	select {
	case res = <-done:
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("за 5 минут вход так и не завершился")
	}
	if res.err != nil {
		return res.err
	}

	tok, err := cfg.Exchange(context.Background(), res.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("обмен кода на токен: %w", err)
	}
	if tok.RefreshToken == "" {
		return fmt.Errorf("Google не выдал refresh token — удали доступ приложения в аккаунте и повтори")
	}

	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	// 0600 — токен даёт доступ к твоему календарю, другим пользователям компа его видеть незачем
	if err := os.WriteFile(googleTokenFile, data, 0600); err != nil {
		return err
	}

	log.Printf("✅ Токен сохранён в %s. Теперь в .env поставь CALENDAR_BACKEND=google", googleTokenFile)
	return nil
}
