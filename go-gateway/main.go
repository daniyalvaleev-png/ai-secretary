package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/joho/godotenv"
)

type EmailDecision struct {
	Sender               string `json:"отправитель"`
	Urgency              string `json:"срочность"`
	RequiresConfirmation bool   `json:"требуется_подтверждение"`
	SuggestedAction      string `json:"предлагаемое_действие"`
	Suspicious           bool   `json:"-"`
	SuspicionReason      string `json:"-"` // заполняет код по результату проверки на мошенничество
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
		OllamaModel: getenvDefault("OLLAMA_MODEL", "granite4.2:3b"),
		TgToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		TgChatID:    os.Getenv("TELEGRAM_CHAT_ID"),
		Interval:    interval,
	}
}

func main() {
	// Разовая команда: go run . gmail-auth — вход ботом в Google и сохранение token.json
	if len(os.Args) > 1 && os.Args[1] == "gmail-auth" {
		if err := runGmailAuth(); err != nil {
			log.Fatalf("❌ %v", err)
		}
		return
	}
	// Разовые команды для пользователей API: go run . create-user <логин> / reset-password <логин>
	if len(os.Args) > 1 && (os.Args[1] == "create-user" || os.Args[1] == "reset-password") {
		if err := runUserCommand(os.Args[1], os.Args[2:]); err != nil {
			log.Fatalf("❌ %v", err)
		}
		return
	}
	if err := godotenv.Load(); err != nil {
		log.Fatal("Ошибка загрузки .env файла. Убедись, что он лежит в той же папке")
	}

	cfg := loadConfig()

	if err := initDB(); err != nil {
		log.Fatalf("Не поднялась база %s: %v", dbFile, err)
	}
	defer db.Close()
	resetStuckStatuses()
	// API_CORS_ORIGINS — с каких адресов браузеру можно ходить в API (Flutter Web), через запятую
	// App — действия над письмами; им пользуются и Telegram-бот, и HTTP API
	mailer, err := newReplySender(cfg)
	if err != nil {
		log.Fatalf("❌ Отправка почты не настроена: %v", err)
	}
	// Лента событий для приложения: новые письма и результаты фоновой работы
	events := NewEventHub(200)
	app := &App{AgentURL: getenvDefault("AGENT_URL", "http://127.0.0.1:8090"), Mailer: mailer, Events: events}

	// Календарь необязателен: нет ключа — «В календарь» ответит, что он не настроен
	if key := os.Getenv("CALENDAR_API_KEY"); key != "" {
		app.Calendar = &CalendarClient{URL: getenvDefault("CALENDAR_URL", "http://127.0.0.1:8095/mcp"), APIKey: key}
	}

	// Результаты фоновой работы — и в ленту приложения, и в Telegram (если он настроен).
	// Бот подключаем до старта API: результаты действий из приложения тоже приходят в Telegram
	notify := multiNotifier{events}
	if cfg.TgToken != "" && cfg.TgChatID != "" {
		bot := &Bot{Token: cfg.TgToken, ChatID: cfg.TgChatID, App: app}
		notify = append(notify, bot)
		go bot.Run()
	}
	app.Notify = notify

	// API_CORS_ORIGINS — с каких адресов браузеру можно ходить в API (Flutter Web), через запятую
	go startAPI(getenvDefault("API_ADDR", "127.0.0.1:8080"), strings.Split(os.Getenv("API_CORS_ORIGINS"), ","), app)

	// Приёмная: что делать с новым письмом + защита от спама и бомбера (intake.go)
	guard := NewGuard(loadGuardConfig())
	guard.Known = isKnownSender
	intake := &Intake{
		Guard:  guard,
		Events: events,
		Analyze: func(from, subject, text string) (EmailDecision, error) {
			return analyzeEmail(cfg.OllamaURL, cfg.OllamaModel, from, subject, text)
		},
		Done: markUIDProcessed,
	}
	if cfg.TgToken != "" && cfg.TgChatID != "" {
		intake.Alert = func(uid uint32, from, subject string, d EmailDecision) {
			if err := sendEmailAlert(cfg.TgToken, cfg.TgChatID, uid, buildTelegramAlert(from, subject, d)); err != nil {
				log.Printf("⚠️ Не удалось отправить в Telegram: %v", err)
			} else {
				log.Println("📨 Уведомление отправлено в Telegram")
			}
		}
		intake.Notice = func(text string) {
			if err := sendTelegramMessage(cfg.TgToken, cfg.TgChatID, text); err != nil {
				log.Printf("⚠️ Не удалось отправить в Telegram: %v", err)
			}
		}
	} else {
		log.Println("⚠️ Telegram не настроен. Добавь TELEGRAM_BOT_TOKEN и TELEGRAM_CHAT_ID в .env")
	}

	maxPerCheck := getenvInt("MAX_PER_CHECK", 20)
	log.Printf("🚀 Шлюз запущен, проверяю почту каждые %s (Ctrl+C — стоп)", cfg.Interval)

	for {
		more, err := checkMail(cfg, intake, maxPerCheck)
		if err != nil {
			log.Printf("⚠️ Проверка почты упала: %v", err)
		}
		if more {
			// Писем больше, чем берём за раз: следующую пачку — почти сразу, а не через CHECK_INTERVAL
			time.Sleep(3 * time.Second)
			continue
		}
		time.Sleep(cfg.Interval)
	}
}

// Сколько письма скачиваем: первые 512 КБ хватает на текст. Вложения на 25 МБ не тянем целиком
const fetchBodyLimit = 512 << 10

var fetchBodyItem = imap.FetchItem(fmt.Sprintf("BODY.PEEK[]<0.%d>", fetchBodyLimit))

// checkMail забирает новые письма (не больше maxPerCheck за раз) и отдаёт их в приёмную.
// more = true — в ящике остались ещё необработанные.
func checkMail(cfg Config, intake *Intake, maxPerCheck int) (more bool, err error) {
	c, err := client.DialTLS(cfg.Addr, nil)
	if err != nil {
		return false, fmt.Errorf("подключение к %s: %w", cfg.Addr, err)
	}
	defer c.Logout()

	if err := c.Login(cfg.Email, cfg.Password); err != nil {
		return false, fmt.Errorf("авторизация (проверь .env): %w", err)
	}

	if _, err := c.Select("INBOX", false); err != nil {
		return false, fmt.Errorf("не открылся INBOX: %w", err)
	}

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}

	uids, err := c.UidSearch(criteria)
	if err != nil {
		return false, fmt.Errorf("поиск писем: %w", err)
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
		return false, nil
	}

	// Старые — первыми. И не больше maxPerCheck за раз: при бомбере тысяча писем не займёт шлюз на часы,
	// а остаток заберём следующей пачкой
	slices.Sort(fresh)
	if len(fresh) > maxPerCheck {
		log.Printf("🔥 Новых писем: %d — беру первые %d, остальные следующей пачкой", len(fresh), maxPerCheck)
		fresh = fresh[:maxPerCheck]
		more = true
	} else {
		log.Printf("🔥 Новых писем: %d", len(fresh))
	}

	uidset := new(imap.SeqSet)
	uidset.AddNum(fresh...)

	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, fetchBodyItem}

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
		return more, fmt.Errorf("fetch: %w", err)
	}

	for _, msg := range msgs {
		if m, ok := toIncoming(msg); ok {
			intake.Handle(m)
		}
	}

	return more, nil
}

// toIncoming достаёт из ответа IMAP отправителя, тему и само письмо
func toIncoming(msg *imap.Message) (incoming, bool) {
	if msg.Uid == 0 || msg.Envelope == nil {
		return incoming{}, false
	}

	m := incoming{UID: msg.Uid, Subject: decodeHeader(msg.Envelope.Subject)}
	if len(msg.Envelope.From) > 0 {
		m.From = msg.Envelope.From[0].Address()
	}

	for _, lit := range msg.Body {
		if lit == nil {
			continue
		}
		b, err := io.ReadAll(lit)
		if err != nil {
			log.Printf("⚠️ Не удалось прочитать тело письма UID %d: %v", msg.Uid, err)
			continue
		}
		m.Raw = b
		break
	}
	return m, true
}

// getenvInt — число из .env; нет или кривое — значение по умолчанию
func getenvInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
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

// newReplySender выбирает способ отправки по MAIL_SENDER из .env
func newReplySender(cfg Config) (ReplySender, error) {
	switch mode := getenvDefault("MAIL_SENDER", "smtp"); mode {
	case "gmail_api":
		sender, err := newGmailAPISender(cfg.Email)
		if err != nil {
			return nil, err
		}
		log.Println("📮 Почту отправляю через Gmail API")
		return sender, nil

	case "smtp":
		log.Println("📮 Почту отправляю через SMTP")
		return Mailer{
			Host:     getenvDefault("SMTP_SERVER", "smtp.gmail.com"),
			Port:     getenvDefault("SMTP_PORT", "587"),
			User:     cfg.Email,
			Password: cfg.Password,
		}, nil

	default:
		return nil, fmt.Errorf("MAIL_SENDER=%q, а надо smtp или gmail_api", mode)
	}
}
