package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	gmailCredentialsFile = "credentials.json" // OAuth-клиент из Google Cloud Console
	gmailTokenFile       = "token.json"       // появится после go run . gmail-auth
	gmailSendScope       = "https://www.googleapis.com/auth/gmail.send"
	gmailSendURL         = "https://gmail.googleapis.com/gmail/v1/users/me/messages/send"
)

// GmailAPISender шлёт письма через Gmail API по HTTPS (порт 443) — VPN и хостеры его не режут
type GmailAPISender struct {
	From    string
	Client  *http.Client // сам подставляет access token и обновляет его по refresh token
	SendURL string
}

func loadGmailOAuthConfig() (*oauth2.Config, error) {
	b, err := os.ReadFile(gmailCredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("нет %s — скачай OAuth-клиент из Google Cloud Console: %w", gmailCredentialsFile, err)
	}

	// Просим только право ОТПРАВЛЯТЬ — читать почту этим токеном нельзя
	return google.ConfigFromJSON(b, gmailSendScope)
}

func newGmailAPISender(from string) (*GmailAPISender, error) {
	cfg, err := loadGmailOAuthConfig()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(gmailTokenFile)
	if err != nil {
		return nil, fmt.Errorf("нет %s — сначала запусти: go run . gmail-auth", gmailTokenFile)
	}

	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("%s битый: %w", gmailTokenFile, err)
	}

	client := cfg.Client(context.Background(), &tok)
	client.Timeout = 30 * time.Second

	return &GmailAPISender{From: from, Client: client, SendURL: gmailSendURL}, nil
}

func (g *GmailAPISender) SendReply(e StoredEmail) error {
	_, msg, err := composeReply(g.From, e)
	if err != nil {
		return err
	}

	// Gmail API принимает готовое письмо целиком, закодированное в base64url
	body, err := json.Marshal(map[string]string{"raw": base64.RawURLEncoding.EncodeToString(msg)})
	if err != nil {
		return err
	}

	resp, err := g.Client.Post(g.SendURL, "application/json", bytes.NewReader(body))
	if err != nil {
		if strings.Contains(err.Error(), "invalid_grant") {
			return fmt.Errorf("доступ к Gmail отозван или протух — перезапусти: go run . gmail-auth")
		}
		return fmt.Errorf("gmail api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		return fmt.Errorf("gmail api HTTP %d: %s", resp.StatusCode, respBody)
	}

	return nil
}

// runGmailAuth — разовый вход ботом в Google через браузер. Сохраняет token.json.
// Схема "loopback": Google после входа редиректит на http://127.0.0.1:<порт>, а мы там ловим код.
func runGmailAuth() error {
	cfg, err := loadGmailOAuthConfig()
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

	fmt.Println("\n👉 Открой ссылку в браузере и войди ПОЧТОЙ БОТА (не своей!):")
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
		return fmt.Errorf("Google не выдал refresh token — удали доступ приложения в аккаунте бота и повтори")
	}

	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	// 0600 — токен = доступ к отправке писем от имени бота, другим пользователям компа его видеть незачем
	if err := os.WriteFile(gmailTokenFile, data, 0600); err != nil {
		return err
	}

	log.Printf("✅ Токен сохранён в %s. Теперь в .env поставь MAIL_SENDER=gmail_api и запускай шлюз", gmailTokenFile)
	return nil
}
