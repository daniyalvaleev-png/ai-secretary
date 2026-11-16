package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Жизнь письма:
// pending → (Подтвердить) → drafting → draft_ready → (Отправить) → approved → [4c: sent]
//
//	↘ (Отклонить) → rejected     ↘ (Не отвечать) → skipped
//
// Если агент упал: drafting → draft_failed → (Повторить) → drafting
type StoredEmail struct {
	UID                  uint32    `json:"uid"`
	From                 string    `json:"from"`
	Subject              string    `json:"subject"`
	Body                 string    `json:"body"`
	ReceivedAt           time.Time `json:"received_at"`
	Urgency              string    `json:"urgency"`
	RequiresConfirmation bool      `json:"requires_confirmation"`
	SuggestedAction      string    `json:"suggested_action"`
	Status               string    `json:"status"`
	Draft                string    `json:"draft"`
	Suspicious           bool      `json:"suspicious"`
	SuspicionReason      string    `json:"suspicion_reason"`
	MeetingTitle         string    `json:"meeting_title"` // встреча, которую агент нашёл в письме
	MeetingStart         string    `json:"meeting_start"` // «2026-10-28 13:59», пусто — встречи нет
	MeetingEnd           string    `json:"meeting_end"`
	CalendarNote         string    `json:"calendar_note"`     // что агент узнал из календаря
	CalendarEventID      string    `json:"calendar_event_id"` // id события после «В календарь»; "adding" — создаётся прямо сейчас
}

const dbFile = "secretary.db"

// Одна строка колонок на все запросы, порядок совпадает со scanEmail
const emailColumns = `uid, from_addr, subject, body, received_at, urgency, requires_confirmation, suggested_action, status, draft, suspicious, suspicion_reason, meeting_title, meeting_start, meeting_end, calendar_note, calendar_event_id`

var db *sql.DB

func initDB() error {
	var err error

	// busy_timeout — подождать 5 сек, если база занята, вместо мгновенной ошибки "database is locked"
	db, err = sql.Open("sqlite", dbFile+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}

	// SQLite пишет в один поток — одно соединение, и никаких гонок между почтой и ботом
	db.SetMaxOpenConns(1)

	schema := []string{
		`CREATE TABLE IF NOT EXISTS emails (
			uid                   INTEGER PRIMARY KEY,
			from_addr             TEXT    NOT NULL,
			subject               TEXT    NOT NULL,
			body                  TEXT    NOT NULL,
			received_at           DATETIME NOT NULL,
			urgency               TEXT    NOT NULL,
			requires_confirmation INTEGER NOT NULL,
			suggested_action      TEXT    NOT NULL,
			status                TEXT    NOT NULL DEFAULT 'pending'
		)`,
		`CREATE TABLE IF NOT EXISTS processed_uids (
			uid INTEGER PRIMARY KEY
		)`,
	}

	// Таблицы пользователей и сессий описаны в auth.go — рядом с кодом, который с ними работает
	for _, q := range append(schema, authSchema...) {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("создание таблиц: %w", err)
		}
	}

	// Миграции для базы, созданной раньше: докидываем новые колонки.
	// В SQLite нет "ADD COLUMN IF NOT EXISTS", поэтому просто глотаем ошибку "уже есть".
	migrations := []string{
		`ALTER TABLE emails ADD COLUMN draft TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE emails ADD COLUMN suspicious INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE emails ADD COLUMN suspicion_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE emails ADD COLUMN meeting_title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE emails ADD COLUMN meeting_start TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE emails ADD COLUMN meeting_end TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE emails ADD COLUMN calendar_note TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE emails ADD COLUMN calendar_event_id TEXT NOT NULL DEFAULT ''`,
	}
	for _, q := range migrations {
		if _, err := db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("миграция %q: %w", q, err)
		}
	}

	return migrateFromJSON()
}

// migrateFromJSON один раз переносит старые emails.json и processed_uids.json в базу,
// а сами файлы переименовывает в *.migrated, чтобы не импортировать их повторно
func migrateFromJSON() error {
	if data, err := os.ReadFile("emails.json"); err == nil {
		var old map[uint32]StoredEmail
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("emails.json битый: %w", err)
		}
		for _, e := range old {
			saveEmail(e)
		}
		if err := os.Rename("emails.json", "emails.json.migrated"); err != nil {
			return err
		}
		log.Printf("📦 Перенёс в SQLite писем: %d", len(old))
	}

	if data, err := os.ReadFile("processed_uids.json"); err == nil {
		var old map[uint32]bool
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("processed_uids.json битый: %w", err)
		}
		for uid := range old {
			markUIDProcessed(uid)
		}
		if err := os.Rename("processed_uids.json", "processed_uids.json.migrated"); err != nil {
			return err
		}
		log.Printf("📦 Перенёс в SQLite обработанных UID: %d", len(old))
	}

	return nil
}

func saveEmail(e StoredEmail) {
	if e.Status == "" {
		e.Status = "pending"
	}

	_, err := db.Exec(`INSERT OR REPLACE INTO emails (`+emailColumns+`) VALUES (`+placeholders(emailColumns)+`)`,
		e.UID, e.From, e.Subject, e.Body, e.ReceivedAt, e.Urgency, e.RequiresConfirmation, e.SuggestedAction, e.Status, e.Draft,
		e.Suspicious, e.SuspicionReason, e.MeetingTitle, e.MeetingStart, e.MeetingEnd, e.CalendarNote, e.CalendarEventID)
	if err != nil {
		log.Printf("⚠️ Не удалось сохранить письмо UID %d: %v", e.UID, err)
	}
}

// scanEmail подходит и для QueryRow, и для Rows — у обоих есть Scan
// scanEmail подходит и для QueryRow, и для Rows — у обоих есть Scan
func scanEmail(s interface{ Scan(...any) error }) (StoredEmail, error) {
	var e StoredEmail
	err := s.Scan(&e.UID, &e.From, &e.Subject, &e.Body, &e.ReceivedAt, &e.Urgency,
		&e.RequiresConfirmation, &e.SuggestedAction, &e.Status, &e.Draft, &e.Suspicious, &e.SuspicionReason,
		&e.MeetingTitle, &e.MeetingStart, &e.MeetingEnd, &e.CalendarNote, &e.CalendarEventID)
	return e, err
}

// placeholders("a, b, c") → "?, ?, ?" — чтобы не пересчитывать знаки вопроса руками при каждой новой колонке
func placeholders(columns string) string {
	n := strings.Count(columns, ",") + 1
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func getEmail(uid uint32) (StoredEmail, bool) {
	e, err := scanEmail(db.QueryRow(`SELECT `+emailColumns+` FROM emails WHERE uid = ?`, uid))
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("⚠️ Не удалось прочитать письмо UID %d: %v", uid, err)
		}
		return StoredEmail{}, false
	}

	return e, true
}

// listEmails — последние письма, новые сверху. status == "" — все статусы.
func listEmails(status string, limit int) ([]StoredEmail, error) {
	rows, err := db.Query(`SELECT `+emailColumns+`
		FROM emails
		WHERE ? = '' OR status = ?
		ORDER BY received_at DESC
		LIMIT ?`, status, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Пустой срез, а не nil — чтобы в JSON был [], а не null
	emails := []StoredEmail{}
	for rows.Next() {
		e, err := scanEmail(rows)
		if err != nil {
			return nil, err
		}
		emails = append(emails, e)
	}

	return emails, rows.Err()
}

func updateEmailStatus(uid uint32, status string) bool {
	res, err := db.Exec(`UPDATE emails SET status = ? WHERE uid = ?`, status, uid)
	if err != nil {
		log.Printf("⚠️ Не удалось обновить статус UID %d: %v", uid, err)
		return false
	}

	n, _ := res.RowsAffected()
	return n > 0
}

// transitionStatus меняет статус, только если сейчас он один из from.
// Проверка и запись — один UPDATE, поэтому два быстрых нажатия кнопки не запустят дело дважды.
func transitionStatus(uid uint32, to string, from ...string) bool {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")

	args := []any{to, uid}
	for _, f := range from {
		args = append(args, f)
	}

	res, err := db.Exec(`UPDATE emails SET status = ? WHERE uid = ? AND status IN (`+placeholders+`)`, args...)
	if err != nil {
		log.Printf("⚠️ Не удалось сменить статус UID %d: %v", uid, err)
		return false
	}

	n, _ := res.RowsAffected()
	return n > 0
}

func saveDraft(uid uint32, resp draftResponse) {
	var m draftMeeting
	if resp.Meeting != nil {
		m = *resp.Meeting
	}

	if _, err := db.Exec(`UPDATE emails SET draft = ?, status = 'draft_ready',
		meeting_title = ?, meeting_start = ?, meeting_end = ?, calendar_note = ? WHERE uid = ?`,
		resp.Draft, m.Title, m.Start, m.End, resp.CalendarNote, uid); err != nil {
		log.Printf("⚠️ Не удалось сохранить черновик UID %d: %v", uid, err)
	}
}

// claimCalendarAdd помечает «событие создаётся» одним UPDATE: два быстрых нажатия «В календарь»
// не создадут два события — второе нажатие увидит, что id уже не пустой.
func claimCalendarAdd(uid uint32) bool {
	res, err := db.Exec(`UPDATE emails SET calendar_event_id = 'adding' WHERE uid = ? AND calendar_event_id = '' AND meeting_start != ''`, uid)
	if err != nil {
		log.Printf("⚠️ claimCalendarAdd UID %d: %v", uid, err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func setCalendarEventID(uid uint32, id string) {
	if _, err := db.Exec(`UPDATE emails SET calendar_event_id = ? WHERE uid = ?`, id, uid); err != nil {
		log.Printf("⚠️ setCalendarEventID UID %d: %v", uid, err)
	}
}

func isProcessed(uid uint32) bool {
	var one int
	err := db.QueryRow(`SELECT 1 FROM processed_uids WHERE uid = ?`, uid).Scan(&one)
	return err == nil
}

func markUIDProcessed(uid uint32) {
	if _, err := db.Exec(`INSERT OR IGNORE INTO processed_uids (uid) VALUES (?)`, uid); err != nil {
		log.Printf("⚠️ Не удалось пометить UID %d обработанным: %v", uid, err)
	}
}

// resetStuckStatuses чинит письма, застрявшие посреди дела, когда шлюз выключили или ноут уснул:
// горутина, которая их вела, умерла вместе с процессом, и сами они из этих статусов не выйдут
func resetStuckStatuses() {
	fixes := []struct{ from, to string }{
		{"drafting", "draft_failed"}, // черновик не дописался — можно нажать «Повторить»
		{"sending", "draft_ready"},   // отправка оборвалась — можно нажать «Отправить» ещё раз
	}

	for _, f := range fixes {
		res, err := db.Exec(`UPDATE emails SET status = ? WHERE status = ?`, f.to, f.from)
		if err != nil {
			log.Printf("⚠️ Не удалось сбросить %s: %v", f.from, err)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("🧹 Зависших писем %s → %s: %d (смотри /pending)", f.from, f.to, n)
		}
	}
	// «В календарь» оборвалось на середине — отпускаем, чтобы кнопку можно было нажать снова
	if _, err := db.Exec(`UPDATE emails SET calendar_event_id = '' WHERE calendar_event_id = 'adding'`); err != nil {
		log.Printf("⚠️ Не удалось сбросить зависшие добавления в календарь: %v", err)
	}
}

// updateDraftText меняет текст черновика, только пока он готов и ещё не отправляется
func updateDraftText(uid uint32, text string) bool {
	res, err := db.Exec(`UPDATE emails SET draft = ? WHERE uid = ? AND status = 'draft_ready'`, text, uid)
	if err != nil {
		log.Printf("⚠️ updateDraftText UID %d: %v", uid, err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}
