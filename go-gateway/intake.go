package main

import (
	"bytes"
	"fmt"
	"log"
	"net/mail"
	"strings"
	"sync"
	"time"
)

// Приёмная входящей почты: что делать с каждым новым письмом, и защита от спама и «почтового бомбера».
//
// Бомбер — когда на адрес шлют сотни писем с одного ящика или подписывают его на сотни сайтов.
// Без защиты каждое письмо — это 2 запроса к модели, сообщение в Telegram и уведомление в приложении:
// шлюз часами разбирает мусор, Telegram упирается в лимиты, а нормальное письмо ждёт в очереди.
//
// Поэтому ДО модели (дёшево, на каждом письме) работает Guard:
//  1. С одного адреса больше SPAM_PER_SENDER_HOUR писем за час → дальше всё от него — спам.
//  2. Всего больше FLOOD_PER_HOUR писем за час → «наплыв»: рассылки и роботы сразу в спам,
//     письма от людей разбираем как обычно.
//  3. Больше FLOOD_PER_HOUR×3 — «сильный наплыв» (бомбер шлёт с сотен разных ящиков):
//     пропускаем только знакомых — тех, кому ты уже отвечал.
//  4. О спаме не уведомляем по одному: раз в 15 минут — одна сводка.
// А в checkMail: за одну проверку не больше MAX_PER_CHECK писем, и от каждого — только первые 512 КБ.

// GuardConfig — пороги защиты, задаются в .env
type GuardConfig struct {
	PerSenderHour int           // писем в час с одного адреса, дальше — спам
	FloodHour     int           // писем в час всего, дальше — режим наплыва
	SummaryEvery  time.Duration // как часто присылать сводку «приглушил столько-то»
}

func loadGuardConfig() GuardConfig {
	return GuardConfig{
		PerSenderHour: getenvInt("SPAM_PER_SENDER_HOUR", 5),
		FloodHour:     getenvInt("FLOOD_PER_HOUR", 30),
		SummaryEvery:  15 * time.Minute,
	}
}

// Guard считает письма за последний час и решает, спам ли очередное.
// Счётчики живут в памяти: после перезапуска шлюза бомбер снова упрётся в пороги за пару минут.
type Guard struct {
	cfg   GuardConfig
	now   func() time.Time       // в тестах — подменные часы
	Known func(addr string) bool // знакомый отправитель: ему уже отвечали. nil — знакомых нет

	mu          sync.Mutex
	all         []time.Time            // когда приходили письма (за последний час)
	bySender    map[string][]time.Time // то же по каждому адресу
	seen        map[uint32]time.Time   // какие UID уже посчитаны — повторная попытка не считается заново
	muted       int                    // сколько приглушено с прошлой сводки
	lastSummary time.Time
}

func NewGuard(cfg GuardConfig) *Guard {
	return &Guard{
		cfg:      cfg,
		now:      time.Now,
		bySender: map[string][]time.Time{},
		seen:     map[uint32]time.Time{},
	}
}

// Verdict — что Guard решил про письмо
type Verdict struct {
	Spam   bool
	Reason string
}

// Check учитывает письмо и решает, спам ли оно. Вызывать на каждое новое письмо до модели.
func (g *Guard) Check(uid uint32, from string, h mail.Header) Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()
	hourAgo := now.Add(-time.Hour)
	addr := normalizeAddr(from)

	g.all = since(g.all, hourAgo)
	g.bySender[addr] = since(g.bySender[addr], hourAgo)
	for id, t := range g.seen {
		if t.Before(hourAgo) {
			delete(g.seen, id)
		}
	}
	// Анализ упал и письмо пришло на повторную обработку — второй раз не считаем
	if _, ok := g.seen[uid]; !ok {
		g.seen[uid] = now
		g.all = append(g.all, now)
		g.bySender[addr] = append(g.bySender[addr], now)
	}
	if len(g.bySender) > 1000 { // чистим давно молчащих отправителей, чтобы карта не росла бесконечно
		for a, ts := range g.bySender {
			if len(ts) == 0 {
				delete(g.bySender, a)
			}
		}
	}

	if n := len(g.bySender[addr]); n > g.cfg.PerSenderHour {
		return Verdict{true, fmt.Sprintf("похоже на бомбер: %d писем за час с одного адреса", n)}
	}
	n := len(g.all)
	if n > g.cfg.FloodHour && isAutomated(addr, h) {
		return Verdict{true, fmt.Sprintf("наплыв писем (%d за час), а это рассылка или робот", n)}
	}
	if n > 3*g.cfg.FloodHour && (g.Known == nil || !g.Known(addr)) {
		return Verdict{true, fmt.Sprintf("сильный наплыв писем (%d за час), а отправитель незнакомый", n)}
	}
	return Verdict{}
}

// Muted — письмо ушло в спам. Возвращает текст сводки, если её пора прислать, иначе пустую строку.
// Первая сводка — сразу, следующие — не чаще раза в SummaryEvery.
func (g *Guard) Muted() string {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.muted++
	now := g.now()
	if !g.lastSummary.IsZero() && now.Sub(g.lastSummary) < g.cfg.SummaryEvery {
		return ""
	}
	text := fmt.Sprintf("📛 Похоже на спам-атаку: за последний час пришло %d писем, приглушил как спам ещё %d. "+
		"Уведомлять о них не буду, письма от людей разбираю как обычно.", len(g.all), g.muted)
	g.muted = 0
	g.lastSummary = now
	return text
}

func since(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	return ts[i:]
}

func normalizeAddr(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		from = a.Address
	}
	return strings.ToLower(strings.TrimSpace(from))
}

// isAutomated — письмо от рассылки или робота, а не от человека.
// Это видно по служебным заголовкам (их ставят все сервисы рассылок) и по адресу вроде noreply@.
func isAutomated(addr string, h mail.Header) bool {
	if h.Get("List-Unsubscribe") != "" || h.Get("List-Id") != "" {
		return true
	}
	switch strings.ToLower(h.Get("Precedence")) {
	case "bulk", "list", "junk":
		return true
	}
	if a := strings.ToLower(h.Get("Auto-Submitted")); a != "" && a != "no" {
		return true
	}
	local, _, _ := strings.Cut(addr, "@")
	for _, w := range []string{"noreply", "no-reply", "donotreply", "do-not-reply", "mailer-daemon", "newsletter", "notification", "bounce", "robot"} {
		if strings.Contains(local, w) {
			return true
		}
	}
	return false
}

// --- Обработка одного письма ---

// incoming — новое письмо, как его отдал IMAP
type incoming struct {
	UID     uint32
	From    string
	Subject string
	Raw     []byte // письмо целиком (или его первые 512 КБ)
}

// Intake — что делать с новым письмом. Зависимости — функциями, чтобы в тестах подставить подделки.
type Intake struct {
	Guard   *Guard
	Events  *EventHub
	Analyze func(from, subject, text string) (EmailDecision, error) // модель
	Alert   func(uid uint32, from, subject string, d EmailDecision) // карточка письма в Telegram; nil — Telegram не настроен
	Notice  func(text string)                                       // просто сообщение в Telegram (сводка о спаме)
	Done    func(uid uint32)                                        // пометить письмо обработанным
}

// Handle разбирает одно письмо. Если модель недоступна — письмо не помечается и попадёт в следующую проверку.
func (in *Intake) Handle(m incoming) {
	if strings.Contains(m.From, "no-reply@accounts.google.com") {
		log.Printf("⏭ Пропускаю служебное письмо от %s (UID %d)", m.From, m.UID)
		in.Done(m.UID)
		return
	}
	if len(m.Raw) == 0 {
		log.Printf("⚠️ У письма UID %d пустое тело", m.UID)
		in.Done(m.UID)
		return
	}

	var header mail.Header
	if msg, err := mail.ReadMessage(bytes.NewReader(m.Raw)); err == nil {
		header = msg.Header
	}
	text := truncate(extractPlainText(m.Raw), 4000)

	stored := StoredEmail{
		UID:        m.UID,
		From:       m.From,
		Subject:    m.Subject,
		Body:       text,
		ReceivedAt: time.Now(),
		Status:     "pending",
	}

	// Сначала дешёвая проверка на спам — без модели, Telegram и уведомлений
	if v := in.Guard.Check(m.UID, m.From, header); v.Spam {
		stored.Status = "spam"
		stored.Urgency = "низкая"
		stored.SuggestedAction = "Ничего не делать — похоже на спам"
		stored.Suspicious = true
		stored.SuspicionReason = v.Reason
		saveEmail(stored)
		in.Done(m.UID)
		log.Printf("🗑 UID %d от %s → спам: %s", m.UID, m.From, v.Reason)

		if summary := in.Guard.Muted(); summary != "" {
			if in.Notice != nil {
				in.Notice(summary)
			}
			in.Events.Publish(Event{Type: "flood", Text: summary, Important: true})
		}
		return
	}

	decision, err := in.Analyze(m.From, m.Subject, text)
	if err != nil {
		log.Printf("❌ Ошибка Ollama для UID %d: %v", m.UID, err)
		return // не помечаем — попробуем на следующем круге
	}

	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("🆔 UID: %d\n👤 От: %s\n📌 Тема: %s\n", m.UID, m.From, m.Subject)
	fmt.Printf("🧠 Срочность: %s\n❓ Подтверждение: %t\n⚙️ Действие: %s\n",
		decision.Urgency, decision.RequiresConfirmation, decision.SuggestedAction)
	if decision.Suspicious {
		fmt.Printf("🚨 Подозрительное: %s\n", decision.SuspicionReason)
	}

	stored.Urgency = decision.Urgency
	stored.RequiresConfirmation = decision.RequiresConfirmation
	stored.SuggestedAction = decision.SuggestedAction
	stored.Suspicious = decision.Suspicious
	stored.SuspicionReason = decision.SuspicionReason
	saveEmail(stored)
	in.Events.NewEmail(stored) // приложение покажет уведомление сразу

	if in.Alert != nil && shouldSendTelegramAlert(decision) {
		in.Alert(m.UID, m.From, m.Subject, decision)
	}
	in.Done(m.UID)
}
