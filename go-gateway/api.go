package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var allowedStatuses = map[string]bool{
	"pending":      true,
	"confirmed":    true, // старый статус, остался у писем до шага 4b
	"rejected":     true,
	"done":         true,
	"drafting":     true,
	"draft_ready":  true,
	"draft_failed": true,
	"approved":     true,
	"skipped":      true,
	"sent":         true,
	"sending":      true,
	"spam":         true, // приглушено защитой от бомбера (intake.go), без модели и уведомлений
}

// newAPIHandler собирает все маршруты. Отдельно от startAPI — чтобы тесты гоняли тот же самый обработчик.
func newAPIHandler(corsOrigins []string, app *App) http.Handler {
	auth := newAuthHandlers()
	act := &actionHandlers{app: app}
	mux := http.NewServeMux()

	// Открытые: без входа
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("POST /api/login", auth.login)

	// Закрытые: только с токеном сессии. Go 1.22+: метод и {uid} прямо в шаблоне пути
	mux.Handle("POST /api/logout", requireSession(auth.logout))
	mux.Handle("GET /api/me", requireSession(handleMe))
	mux.Handle("GET /api/emails", requireSession(handleListEmails))
	mux.Handle("GET /api/emails/{uid}", requireSession(handleGetEmail))

	// Действия — те же, что кнопки в Telegram
	mux.Handle("POST /api/emails/{uid}/confirm", requireSession(act.handle(app.Confirm, http.StatusAccepted)))
	mux.Handle("POST /api/emails/{uid}/reject", requireSession(act.handle(app.Reject, http.StatusOK)))
	mux.Handle("POST /api/emails/{uid}/redraft", requireSession(act.handle(app.Redraft, http.StatusAccepted)))
	mux.Handle("POST /api/emails/{uid}/send", requireSession(act.handle(app.Send, http.StatusAccepted)))
	mux.Handle("POST /api/emails/{uid}/skip", requireSession(act.handle(app.Skip, http.StatusOK)))
	mux.Handle("POST /api/emails/{uid}/not_spam", requireSession(act.handle(app.NotSpam, http.StatusOK)))
	mux.Handle("POST /api/emails/{uid}/calendar", requireSession(act.handle(app.AddToCalendar, http.StatusAccepted)))
	mux.Handle("POST /api/emails/{uid}/draft", requireSession(act.editDraft))
	mux.Handle("GET /api/calendar", requireSession(act.listCalendar))

	// Лента событий: приложение узнаёт о новых письмах и результатах сразу, без перечитывания списка
	mux.Handle("GET /api/events", requireSession(handleEvents(app.Events)))

	return withCORS(corsOrigins, mux)
}

// startAPI поднимает HTTP API. Блокирует, поэтому запускаем в горутине.
func startAPI(addr string, corsOrigins []string, app *App) {
	cleanupSessions()
	if countUsers() == 0 {
		log.Println("⚠️ В API ещё нет пользователей — создай: go run . create-user <логин>")
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: newAPIHandler(corsOrigins, app),
		// Таймауты — чтобы медленный или зависший клиент не держал соединения вечно
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	log.Printf("🌐 HTTP API слушает http://%s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("❌ HTTP API упал: %v", err)
	}
}

// withCORS разрешает браузеру ходить в API со страниц из списка (Flutter Web на другом порту).
// Токен идёт в заголовке Authorization, а не в cookie, поэтому чужой сайт не может слать запросы от твоего имени.
func withCORS(origins []string, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = true
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); allowed[origin] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
		}

		// Предварительный запрос браузера (preflight): отвечаем сразу, без проверки входа
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// decodeJSON читает тело запроса не больше 64 КБ — гигабайтный JSON не положит сервер
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	return json.NewDecoder(r.Body).Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func parseUID(w http.ResponseWriter, r *http.Request) (uint32, bool) {
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, "uid должен быть числом")
		return 0, false
	}
	return uint32(uid), true
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /api/emails?status=pending&limit=20
func handleListEmails(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && !allowedStatuses[status] {
		writeError(w, http.StatusBadRequest, "неизвестный status")
		return
	}

	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, "limit — число от 1 до 500")
			return
		}
		limit = n
	}

	emails, err := listEmails(status, limit)
	if err != nil {
		log.Printf("⚠️ listEmails: %v", err)
		writeError(w, http.StatusInternalServerError, "ошибка базы")
		return
	}

	writeJSON(w, http.StatusOK, emails)
}

func handleGetEmail(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUID(w, r)
	if !ok {
		return
	}

	e, found := getEmail(uid)
	if !found {
		writeError(w, http.StatusNotFound, "письмо не найдено")
		return
	}

	writeJSON(w, http.StatusOK, e)
}

// POST /api/emails/{uid}/status  {"status":"confirmed"}
