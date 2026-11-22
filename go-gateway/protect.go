package main

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Защита HTTP API от перегрузки и мелких браузерных атак.
//
//  - rateLimiter: не больше API_RATE_PER_MIN запросов в минуту с одного адреса. Скрипт, который долбит
//    API тысячами запросов, получает 429 «подожди», а не кладёт шлюз и базу.
//  - streamLimiter: у каждого пользователя не больше 3 одновременно открытых «прослушек» ленты событий —
//    каждая держит соединение до 25 секунд, и тысяча таких тоже положила бы сервер.
//  - securityHeaders: заголовки, которые запрещают браузеру лишнее (см. ниже).
//
// Вход защищён отдельно и строже — loginLimiter в auth.go (5 неудачных попыток на логин за 15 минут).

// securityHeaders — заголовки на каждый ответ API
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff") // браузер не «угадывает» тип: JSON никогда не исполнится как скрипт
		h.Set("Cache-Control", "no-store")         // письма и токены не оседают в кэше браузера и прокси
		h.Set("X-Frame-Options", "DENY")           // API нельзя встроить в чужую страницу (кликджекинг)
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// rateLimiter — скользящее окно: сколько запросов было с адреса за последнюю минуту.
// Считаем по IP, а не по токену: иначе атакующий подставлял бы каждый раз новый выдуманный токен.
type rateLimiter struct {
	perMin int
	now    func() time.Time

	mu   sync.Mutex
	hits map[string][]time.Time
}

func newRateLimiter(perMin int) *rateLimiter {
	return &rateLimiter{perMin: perMin, now: time.Now, hits: map[string][]time.Time{}}
}

// allow — можно ли ещё один запрос; если нет — через сколько можно
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	ts := since(l.hits[key], now.Add(-time.Minute))
	if len(ts) >= l.perMin {
		l.hits[key] = ts
		return false, ts[0].Add(time.Minute).Sub(now)
	}
	l.hits[key] = append(ts, now)

	if len(l.hits) > 10000 { // чистим давно молчащих, чтобы карта не росла бесконечно
		for k, v := range l.hits {
			if len(since(v, now.Add(-time.Minute))) == 0 {
				delete(l.hits, k)
			}
		}
	}
	return true, 0
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := l.allow(clientIP(r)); !ok {
			secs := int(wait.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("слишком много запросов — подожди %d с", secs))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// streamLimiter — сколько «прослушек» ленты открыто у каждого пользователя прямо сейчас
type streamLimiter struct {
	max  int
	mu   sync.Mutex
	open map[int64]int
}

func newStreamLimiter(max int) *streamLimiter {
	return &streamLimiter{max: max, open: map[int64]int{}}
}

// acquire занимает место; release — вернуть его, когда ответ отправлен
func (s *streamLimiter) acquire(user int64) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open[user] >= s.max {
		return nil, false
	}
	s.open[user]++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.open[user]--; s.open[user] <= 0 {
			delete(s.open, user)
		}
	}, true
}
