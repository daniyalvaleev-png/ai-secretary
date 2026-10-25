package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

type StoredEmail struct {
	UID                  uint32    `json:"uid"`
	From                 string    `json:"from"`
	Subject              string    `json:"subject"`
	Body                 string    `json:"body"`
	ReceivedAt           time.Time `json:"received_at"`
	Urgency              string    `json:"urgency"`
	RequiresConfirmation bool      `json:"requires_confirmation"`
	SuggestedAction      string    `json:"suggested_action"`
	Status               string    `json:"status"` // pending, confirmed, rejected, done
}

const dbFile = "secretary.db"

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

	for _, q := range schema {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("создание таблиц: %w", err)
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

	_, err := db.Exec(`INSERT OR REPLACE INTO emails
		(uid, from_addr, subject, body, received_at, urgency, requires_confirmation, suggested_action, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.UID, e.From, e.Subject, e.Body, e.ReceivedAt, e.Urgency, e.RequiresConfirmation, e.SuggestedAction, e.Status)
	if err != nil {
		log.Printf("⚠️ Не удалось сохранить письмо UID %d: %v", e.UID, err)
	}
}

func getEmail(uid uint32) (StoredEmail, bool) {
	var e StoredEmail

	err := db.QueryRow(`SELECT uid, from_addr, subject, body, received_at, urgency, requires_confirmation, suggested_action, status
		FROM emails WHERE uid = ?`, uid).
		Scan(&e.UID, &e.From, &e.Subject, &e.Body, &e.ReceivedAt, &e.Urgency, &e.RequiresConfirmation, &e.SuggestedAction, &e.Status)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("⚠️ Не удалось прочитать письмо UID %d: %v", uid, err)
		}
		return StoredEmail{}, false
	}

	return e, true
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
