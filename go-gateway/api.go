package main

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
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
}

// startAPI поднимает HTTP API. Блокирует, поэтому запускаем в горутине.
func startAPI(addr, apiKey string) {
	if strings.TrimSpace(apiKey) == "" {
		log.Println("⚠️ API_KEY не задан — HTTP API не запущен")
		return
	}

	mux := http.NewServeMux()

	// Go 1.22+: метод и {uid} прямо в шаблоне пути
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("GET /api/emails", handleListEmails)
	mux.HandleFunc("GET /api/emails/{uid}", handleGetEmail)
	mux.HandleFunc("POST /api/emails/{uid}/status", handleSetStatus)

	log.Printf("🌐 HTTP API слушает http://%s", addr)
	if err := http.ListenAndServe(addr, requireAPIKey(apiKey, mux)); err != nil {
		log.Printf("❌ HTTP API упал: %v", err)
	}
}

// requireAPIKey пускает только запросы с заголовком Authorization: Bearer <API_KEY>
func requireAPIKey(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

		// ConstantTimeCompare — чтобы ключ нельзя было подобрать по времени ответа
		if subtle.ConstantTimeCompare([]byte(got), []byte(apiKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "нужен заголовок Authorization: Bearer <API_KEY>")
			return
		}

		next.ServeHTTP(w, r)
	})
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
func handleSetStatus(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUID(w, r)
	if !ok {
		return
	}

	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "тело должно быть JSON: {\"status\":\"...\"}")
		return
	}

	if !allowedStatuses[req.Status] {
		writeError(w, http.StatusBadRequest, "status: pending, confirmed, rejected или done")
		return
	}

	if !updateEmailStatus(uid, req.Status) {
		writeError(w, http.StatusNotFound, "письмо не найдено")
		return
	}

	log.Printf("🌐 API: UID %d → %s", uid, req.Status)

	e, _ := getEmail(uid)
	writeJSON(w, http.StatusOK, e)
}
