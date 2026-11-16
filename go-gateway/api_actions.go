package main

import (
	"errors"
	"log"
	"net/http"
	"time"
)

// HTTP-обёртки над действиями App. Логики тут нет — только перевод ошибок в HTTP-коды.
//
// Долгие действия (confirm, redraft, send, calendar) отвечают 202 Accepted сразу:
// работа идёт в фоне, а приложение узнаёт результат, перечитывая письмо (GET /api/emails/{uid}).

type actionHandlers struct {
	app *App
}

// writeActionError: какая ошибка действия каким HTTP-кодом
func writeActionError(w http.ResponseWriter, err error) {
	var wrong ErrWrongStatus
	var notConfigured ErrNotConfigured

	switch {
	case errors.Is(err, ErrEmailNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.As(err, &wrong):
		// 409 Conflict + текущий статус — приложение поймёт, что письмо уже кто-то обработал
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "status": wrong.Status})
	case errors.Is(err, ErrNoMeeting), errors.Is(err, ErrAlreadyInCalendar):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrBadDraft):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &notConfigured):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		log.Printf("⚠️ API: %v", err)
		writeError(w, http.StatusInternalServerError, "внутренняя ошибка")
	}
}

// handle превращает действие App в обработчик: POST /api/emails/{uid}/<действие> → письмо в новом статусе
func (h *actionHandlers) handle(action func(uint32) (StoredEmail, error), okStatus int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := parseUID(w, r)
		if !ok {
			return
		}

		e, err := action(uid)
		if err != nil {
			writeActionError(w, err)
			return
		}
		writeJSON(w, okStatus, e)
	}
}

// POST /api/emails/{uid}/draft {"draft":"..."} — поправить черновик перед отправкой
func (h *actionHandlers) editDraft(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUID(w, r)
	if !ok {
		return
	}

	var req struct {
		Draft string `json:"draft"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "тело должно быть JSON: {\"draft\":\"...\"}")
		return
	}

	e, err := h.app.EditDraft(uid, req.Draft)
	if err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// GET /api/calendar?from=2026-11-16&to=2026-11-22 — события за период (по умолчанию: сегодня + 7 дней)
func (h *actionHandlers) listCalendar(w http.ResponseWriter, r *http.Request) {
	if h.app.Calendar == nil {
		writeActionError(w, ErrNotConfigured{"календарь (нет CALENDAR_API_KEY в .env)"})
		return
	}

	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		from = time.Now().Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	}

	events, err := h.app.Calendar.ListEvents(r.Context(), from, to)
	if err != nil {
		// Ошибки календаря (кривая дата, протухший токен Google) — это 502: сломался сервис за нами, а не запрос
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if events == nil {
		events = []calendarEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "events": events})
}
