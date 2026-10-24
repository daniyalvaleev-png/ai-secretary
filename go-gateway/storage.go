package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
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

var (
	emailsMu   sync.RWMutex
	emailsDB   = map[uint32]StoredEmail{}
	emailsFile = "emails.json"
)

func loadEmails() {
	data, err := os.ReadFile(emailsFile)
	if err != nil {
		return
	}

	if err := json.Unmarshal(data, &emailsDB); err != nil {
		log.Printf("⚠️ Не удалось прочитать emails.json: %v", err)
		return
	}

	log.Printf("📂 Загружено писем из локальной базы: %d", len(emailsDB))
}

func saveEmailsLocked() {
	data, err := json.MarshalIndent(emailsDB, "", "  ")
	if err != nil {
		log.Printf("⚠️ Не удалось сериализовать emails.json: %v", err)
		return
	}

	if err := os.WriteFile(emailsFile, data, 0644); err != nil {
		log.Printf("⚠️ Не удалось сохранить emails.json: %v", err)
	}
}

func saveEmail(e StoredEmail) {
	emailsMu.Lock()
	defer emailsMu.Unlock()

	if e.Status == "" {
		e.Status = "pending"
	}

	emailsDB[e.UID] = e
	saveEmailsLocked()
}

func getEmail(uid uint32) (StoredEmail, bool) {
	emailsMu.RLock()
	defer emailsMu.RUnlock()

	e, ok := emailsDB[uid]
	return e, ok
}

func updateEmailStatus(uid uint32, status string) bool {
	emailsMu.Lock()
	defer emailsMu.Unlock()

	e, ok := emailsDB[uid]
	if !ok {
		return false
	}

	e.Status = status
	emailsDB[uid] = e
	saveEmailsLocked()

	return true
}
