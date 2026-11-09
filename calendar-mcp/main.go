// calendar-mcp — микросервис календаря. Отдаёт агенту инструменты по протоколу MCP (Streamable HTTP).
package main

import (
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	_ "time/tzdata" // база часовых поясов внутри бинарника — на Windows своей у Go может не быть

	"github.com/joho/godotenv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	
)

func main() {
	// Разовая команда: go run . google-auth — вход в Google и сохранение google_token.json
	if len(os.Args) > 1 && os.Args[1] == "google-auth" {
		if err := runGoogleAuth(); err != nil {
			log.Fatalf("❌ %v", err)
		}
		return
	}
	_ = godotenv.Load() // .env необязателен — можно задать переменные окружения напрямую

	addr := getenvDefault("CALENDAR_ADDR", "127.0.0.1:8095")
	apiKey := os.Getenv("CALENDAR_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		log.Fatal("❌ Задай CALENDAR_API_KEY в .env — без ключа календарь не запускаю")
	}

	loc, err := time.LoadLocation(getenvDefault("CALENDAR_TZ", "Europe/Moscow"))
	if err != nil {
		log.Fatalf("❌ Кривой CALENDAR_TZ: %v", err)
	}

	cal, err := newCalendar(loc)
	if err != nil {
		log.Fatalf("❌ Календарь не поднялся: %v", err)
	}

	server := newMCPServer(cal, loc)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	log.Printf("📅 Календарь (MCP) слушает http://%s/mcp, часовой пояс %s", addr, loc)

	mux := http.NewServeMux()
	mux.Handle("/mcp", requireAPIKey(apiKey, handler))
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("❌ %v", err)
	}
}

// newCalendar выбирает реализацию по CALENDAR_BACKEND из .env — это единственное место, которое про неё знает
func newCalendar(loc *time.Location) (Calendar, error) {
	switch backend := getenvDefault("CALENDAR_BACKEND", "sqlite"); backend {
	case "google":
		log.Println("📅 Календарь: Google Calendar")
		return newGoogleCalendar(getenvDefault("CALENDAR_GOOGLE_ID", "primary"), loc)
	case "sqlite":
		log.Println("📅 Календарь: локальный SQLite")
		return NewSQLiteCalendar(getenvDefault("CALENDAR_DB", "calendar.db"), loc)
	default:
		return nil, fmt.Errorf("CALENDAR_BACKEND=%q, а надо sqlite или google", backend)
	}
}

// requireAPIKey пускает только запросы с заголовком Authorization: Bearer <CALENDAR_API_KEY>
func requireAPIKey(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(apiKey)) != 1 {
			http.Error(w, "нужен заголовок Authorization: Bearer <CALENDAR_API_KEY>", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
