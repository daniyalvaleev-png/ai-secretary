package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/joho/godotenv"
)

type OllamaRequest struct {
	Model   string                 `json:"model"`
	Prompt  string                 `json:"prompt"`
	Format  string                 `json:"format"`
	Stream  bool                   `json:"stream"`
	Options map[string]interface{} `json:"options,omitempty"`
}

type OllamaResponse struct {
	Response string `json:"response"`
}

type EmailDecision struct {
	Sender               string `json:"отправитель"`
	Urgency              string `json:"срочность"`
	RequiresConfirmation bool   `json:"требуется_подтверждение"`
	SuggestedAction      string `json:"предлагаемое_действие"`
}

type Config struct {
	Email       string
	Password    string
	Addr        string
	OllamaURL   string
	OllamaModel string
	TgToken     string
	TgChatID    string
	Interval    time.Duration
}

func loadConfig() Config {
	interval, err := time.ParseDuration(getenvDefault("CHECK_INTERVAL", "2m"))
	if err != nil {
		log.Printf("⚠️ Кривой CHECK_INTERVAL, ставлю 2m")
		interval = 2 * time.Minute
	}

	return Config{
		Email:       os.Getenv("BOT_EMAIL"),
		Password:    os.Getenv("BOT_APP_PASSWORD"),
		Addr:        fmt.Sprintf("%s:%s", os.Getenv("IMAP_SERVER"), os.Getenv("IMAP_PORT")),
		OllamaURL:   getenvDefault("OLLAMA_URL", "http://localhost:11434"),
		OllamaModel: getenvDefault("OLLAMA_MODEL", "secretary"),
		TgToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		TgChatID:    os.Getenv("TELEGRAM_CHAT_ID"),
		Interval:    interval,
	}
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Ошибка загрузки .env файла. Убедись, что он лежит в той же папке")
	}

	cfg := loadConfig()

	if err := initDB(); err != nil {
		log.Fatalf("Не поднялась база %s: %v", dbFile, err)
	}
	defer db.Close()

	if cfg.TgToken != "" && cfg.TgChatID != "" {
		go runTelegramBot(cfg.TgToken, cfg.TgChatID)
	}

	log.Printf("🚀 Шлюз запущен, проверяю почту каждые %s (Ctrl+C — стоп)", cfg.Interval)

	for {
		if err := checkMail(cfg); err != nil {
			log.Printf("⚠️ Проверка почты упала: %v", err)
		}
		time.Sleep(cfg.Interval)
	}
}

func checkMail(cfg Config) error {
	c, err := client.DialTLS(cfg.Addr, nil)
	if err != nil {
		return fmt.Errorf("подключение к %s: %w", cfg.Addr, err)
	}
	defer c.Logout()

	if err := c.Login(cfg.Email, cfg.Password); err != nil {
		return fmt.Errorf("авторизация (проверь .env): %w", err)
	}

	if _, err := c.Select("INBOX", false); err != nil {
		return fmt.Errorf("не открылся INBOX: %w", err)
	}

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}

	uids, err := c.UidSearch(criteria)
	if err != nil {
		return fmt.Errorf("поиск писем: %w", err)
	}

	// Отсекаем уже обработанные ДО скачивания
	var fresh []uint32
	for _, uid := range uids {
		if !isProcessed(uid) {
			fresh = append(fresh, uid)
		}
	}

	if len(fresh) == 0 {
		log.Println("📭 Новых писем нет")
		return nil
	}

	log.Printf("🔥 Новых писем: %d", len(fresh))

	uidset := new(imap.SeqSet)
	uidset.AddNum(fresh...)

	items := []imap.FetchItem{
		imap.FetchUid,
		imap.FetchEnvelope,
		imap.FetchItem("BODY.PEEK[]"),
	}

	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)

	go func() {
		done <- c.UidFetch(uidset, items, messages)
	}()

	// Сначала забираем всё с сервера, потом думаем — чтобы IMAP не висел, пока Ollama тупит
	var msgs []*imap.Message
	for msg := range messages {
		msgs = append(msgs, msg)
	}

	if err := <-done; err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	for _, msg := range msgs {
		processMessage(c, cfg, msg)
	}

	return nil
}

func processMessage(c *client.Client, cfg Config, msg *imap.Message) {
	if msg.Uid == 0 || msg.Envelope == nil {
		return
	}

	env := msg.Envelope

	from := ""
	if len(env.From) > 0 {
		from = env.From[0].Address()
	}

	subject := decodeHeader(env.Subject)

	if strings.Contains(from, "no-reply@accounts.google.com") {
		log.Printf("⏭ Пропускаю служебное письмо от %s (UID %d)", from, msg.Uid)
		markProcessed(c, msg.Uid)
		return
	}

	var raw []byte
	for _, lit := range msg.Body {
		if lit == nil {
			continue
		}
		b, err := io.ReadAll(lit)
		if err != nil {
			log.Printf("⚠️ Не удалось прочитать тело письма UID %d: %v", msg.Uid, err)
			continue
		}
		raw = b
		break
	}

	if len(raw) == 0 {
		log.Printf("⚠️ У письма UID %d пустое тело", msg.Uid)
		markProcessed(c, msg.Uid)
		return
	}

	text := truncate(extractPlainText(raw), 4000)

	prompt := fmt.Sprintf(`Проанализируй входящее письмо для ИИ-секретаря.

Отправитель: %s
Тема: %s
Тело письма:
%s

Верни строго JSON по схеме:
{"отправитель":"string","срочность":"высокая/средняя/низкая","требуется_подтверждение":true,"предлагаемое_действие":"string"}`,
		from, subject, text)

	decision, err := askOllama(cfg.OllamaURL, cfg.OllamaModel, prompt)
	if err != nil {
		log.Printf("❌ Ошибка Ollama для UID %d: %v", msg.Uid, err)
		return // не помечаем — попробуем на следующем круге
	}

	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("🆔 UID: %d\n👤 От: %s\n📌 Тема: %s\n", msg.Uid, from, subject)
	fmt.Printf("🧠 Срочность: %s\n❓ Подтверждение: %t\n⚙️ Действие: %s\n",
		decision.Urgency, decision.RequiresConfirmation, decision.SuggestedAction)

	saveEmail(StoredEmail{
		UID:                  msg.Uid,
		From:                 from,
		Subject:              subject,
		Body:                 text,
		ReceivedAt:           time.Now(),
		Urgency:              decision.Urgency,
		RequiresConfirmation: decision.RequiresConfirmation,
		SuggestedAction:      decision.SuggestedAction,
		Status:               "pending",
	})

	if shouldSendTelegramAlert(decision) {
		if cfg.TgToken == "" || cfg.TgChatID == "" {
			log.Println("⚠️ Telegram не настроен. Добавь TELEGRAM_BOT_TOKEN и TELEGRAM_CHAT_ID в .env")
		} else if err := sendEmailAlert(cfg.TgToken, cfg.TgChatID, msg.Uid, buildTelegramAlert(from, subject, decision)); err != nil {
			log.Printf("⚠️ Не удалось отправить в Telegram: %v", err)
		} else {
			log.Println("📨 Уведомление отправлено в Telegram")
		}
	}

	markProcessed(c, msg.Uid)
}

func getenvDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func decodeHeader(s string) string {
	decoder := &mime.WordDecoder{}
	decoded, err := decoder.DecodeHeader(s)
	if err != nil {
		return s
	}
	return decoded
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "\n[обрезано]"
}

func markSeen(_ *client.Client, _ uint32) {
	// Пока отключено намеренно.
	//
	// В go-imap v1.2.1 API для STORE/Flags ведёт себя неочевидно,
	// а для MVP нам важнее не обрабатывать письмо повторно.
	//
	// Повторную обработку блокирует processed_uids.json.
	//
	// Позже, когда будем подключать Gmail API / нормальный почтовый провайдер,
	// вернём реальное описание прочитанным.
}

func markProcessed(c *client.Client, uid uint32) {
	markUIDProcessed(uid)
	markSeen(c, uid)
}

func decodeBody(r io.Reader, transferEncoding string) ([]byte, error) {
	enc := strings.ToLower(strings.TrimSpace(transferEncoding))

	switch enc {
	case "base64":
		return io.ReadAll(base64.NewDecoder(base64.StdEncoding, r))
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(r))
	default:
		return io.ReadAll(r)
	}
}

func extractPlainText(raw []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return string(raw)
	}

	body, err := decodeBody(msg.Body, msg.Header.Get("Content-Transfer-Encoding"))
	if err != nil {
		return ""
	}

	contentType := msg.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return string(body)
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		var plainBuilder strings.Builder
		var htmlBuilder strings.Builder

		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])

		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}

			partContentType := part.Header.Get("Content-Type")
			partMediaType, _, _ := mime.ParseMediaType(partContentType)

			partBody, err := decodeBody(part, part.Header.Get("Content-Transfer-Encoding"))
			if err != nil {
				continue
			}

			if partMediaType == "text/plain" {
				plainBuilder.Write(partBody)
				plainBuilder.WriteString("\n")
			}

			if partMediaType == "text/html" {
				htmlBuilder.Write(partBody)
				htmlBuilder.WriteString("\n")
			}
		}

		if plainBuilder.Len() > 0 {
			return plainBuilder.String()
		}

		if htmlBuilder.Len() > 0 {
			return stripHTML(htmlBuilder.String())
		}

		return "[Нет текстовой части]"
	}

	if mediaType == "text/plain" {
		return string(body)
	}

	if mediaType == "text/html" {
		return stripHTML(string(body))
	}

	return fmt.Sprintf("[Пока поддерживается только text/plain или text/html. Тип письма: %s]", mediaType)
}

var (
	scriptRegex = regexp.MustCompile(`(?is)<script.*?</script>`)
	styleRegex  = regexp.MustCompile(`(?is)<style.*?</style>`)
	tagRegex    = regexp.MustCompile(`(?is)<[^>]+>`)
	spacesRegex = regexp.MustCompile(`\s+`)
)

func stripHTML(s string) string {
	s = scriptRegex.ReplaceAllString(s, " ")
	s = styleRegex.ReplaceAllString(s, " ")
	s = tagRegex.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spacesRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func askOllama(baseURL, model, prompt string) (EmailDecision, error) {
	payload := OllamaRequest{
		Model:  model,
		Prompt: prompt,
		Format: "json",
		Stream: false,
		Options: map[string]interface{}{
			"temperature":    0.0,
			"repeat_penalty": 1.2,
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return EmailDecision{}, err
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/generate", bytes.NewBuffer(data))
	if err != nil {
		return EmailDecision{}, err
	}

	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{
		Timeout: 120 * time.Second,
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return EmailDecision{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return EmailDecision{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return EmailDecision{}, fmt.Errorf("Ollama вернул HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var ollamaResp OllamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return EmailDecision{}, err
	}

	var decision EmailDecision
	if err := json.Unmarshal([]byte(ollamaResp.Response), &decision); err != nil {
		return EmailDecision{}, fmt.Errorf("модель вернула невалидный JSON: %w; сырой ответ: %s", err, ollamaResp.Response)
	}

	return decision, nil
}
