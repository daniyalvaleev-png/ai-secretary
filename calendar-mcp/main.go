// calendar-mcp — микросервис календаря. Отдаёт агенту инструменты по протоколу MCP (Streamable HTTP).
package main

import (
	"crypto/subtle"
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

	cal, err := NewSQLiteCalendar(getenvDefault("CALENDAR_DB", "calendar.db"), loc)
	if err != nil {
		log.Fatalf("❌ Не поднялась база календаря: %v", err)
	}
	defer cal.Close()

	server := newMCPServer(cal, loc)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	log.Printf("📅 Календарь (MCP) слушает http://%s/mcp, часовой пояс %s", addr, loc)

	mux := http.NewServeMux()
	mux.Handle("/mcp", requireAPIKey(apiKey, handler))
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("❌ %v", err)
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
