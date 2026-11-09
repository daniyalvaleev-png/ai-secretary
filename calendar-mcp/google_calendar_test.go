package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGoogle повторяет то, на что мы опираемся в Google Calendar API:
// отбор по timeMin/timeMax, страницы по nextPageToken, отменённые события в выдаче, 410 на удалённое
type fakeGoogle struct {
	mu     sync.Mutex
	events map[string]googleEvent
	nextID int
	loc    *time.Location
}

func (f *fakeGoogle) timeOf(g googleTime) time.Time {
	if g.DateTime != "" {
		t, _ := time.Parse(time.RFC3339, g.DateTime)
		return t
	}
	t, _ := time.ParseInLocation("2006-01-02", g.Date, f.loc)
	return t
}

func (f *fakeGoogle) add(e googleEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	e.ID = fmt.Sprintf("g%d", f.nextID)
	f.events[e.ID] = e
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	const prefix = "/calendars/primary/events"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		from, _ := time.Parse(time.RFC3339, r.URL.Query().Get("timeMin"))
		to, _ := time.Parse(time.RFC3339, r.URL.Query().Get("timeMax"))

		var items []googleEvent
		for _, e := range f.events {
			if f.timeOf(e.Start).Before(to) && f.timeOf(e.End).After(from) {
				items = append(items, e)
			}
		}
		sort.Slice(items, func(i, j int) bool { return f.timeOf(items[i].Start).Before(f.timeOf(items[j].Start)) })

		const pageSize = 2
		offset, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
		end := min(offset+pageSize, len(items))

		page := map[string]any{"items": items[min(offset, len(items)):end]}
		if end < len(items) {
			page["nextPageToken"] = strconv.Itoa(end)
		}
		json.NewEncoder(w).Encode(page)

	case http.MethodPost:
		var e googleEvent
		json.NewDecoder(r.Body).Decode(&e)
		if e.Start.TimeZone == "" {
			http.Error(w, `{"error":"Missing time zone"}`, http.StatusBadRequest)
			return
		}
		f.nextID++
		e.ID = fmt.Sprintf("g%d", f.nextID)
		f.events[e.ID] = e
		json.NewEncoder(w).Encode(e)

	case http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, prefix+"/")
		if _, ok := f.events[id]; !ok {
			w.WriteHeader(http.StatusGone)
			return
		}
		delete(f.events, id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func newFakeGoogle(t *testing.T) (*GoogleCalendar, *fakeGoogle) {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGoogle{events: map[string]googleEvent{}, loc: loc}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return &GoogleCalendar{Client: srv.Client(), BaseURL: srv.URL, CalendarID: "primary", Loc: loc}, fake
}

// Контракт: что обязана уметь ЛЮБАЯ реализация Calendar. Появится третья — добавим её сюда одной строкой.
func TestCalendarContract(t *testing.T) {
	implementations := map[string]func(t *testing.T) (Calendar, *time.Location){
		"sqlite": func(t *testing.T) (Calendar, *time.Location) { return newTestCalendar(t) },
		"google": func(t *testing.T) (Calendar, *time.Location) {
			cal, _ := newFakeGoogle(t)
			return cal, cal.Loc
		},
	}

	for name, build := range implementations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			cal, loc := build(t)

			e, err := cal.CreateEvent(ctx, Event{Title: "Парк", Description: "у вокзала", Start: at(loc, "2026-10-28 13:59"), End: at(loc, "2026-10-28 15:00")})
			if err != nil || e.ID == "" {
				t.Fatal(e, err)
			}
			if e.Title != "Парк" || e.Start.Location().String() != "Europe/Moscow" || e.Start.Format("15:04") != "13:59" {
				t.Fatal("создалось не то:", e)
			}

			if _, err := cal.CreateEvent(ctx, Event{Title: "кривое", Start: at(loc, "2026-10-28 15:00"), End: at(loc, "2026-10-28 14:00")}); err == nil {
				t.Fatal("конец раньше начала должен быть ошибкой")
			}

			if _, err := cal.CreateEvent(ctx, Event{Title: "Утро", Start: at(loc, "2026-10-28 09:00"), End: at(loc, "2026-10-28 10:00")}); err != nil {
				t.Fatal(err)
			}

			day, err := cal.ListEvents(ctx, at(loc, "2026-10-28 00:00"), at(loc, "2026-10-29 00:00"))
			if err != nil || len(day) != 2 || day[0].Title != "Утро" {
				t.Fatal("ожидали 2 события, сначала утро:", day, err)
			}
			if day[1].Description != "у вокзала" {
				t.Fatal("потерялось описание:", day[1])
			}

			if got, _ := cal.ListEvents(ctx, at(loc, "2026-10-28 15:00"), at(loc, "2026-10-28 16:00")); len(got) != 0 {
				t.Fatal("граница не должна считаться пересечением", got)
			}

			slots, err := FreeSlots(ctx, cal, at(loc, "2026-10-28 00:00"), 9, 19, time.Hour)
			if err != nil || len(slots) != 2 || slots[0].Start.Format("15:04") != "10:00" || slots[1].Start.Format("15:04") != "15:00" {
				t.Fatal("окна:", slots, err)
			}

			if err := cal.DeleteEvent(ctx, e.ID); err != nil {
				t.Fatal(err)
			}
			if err := cal.DeleteEvent(ctx, e.ID); !errors.Is(err, ErrEventNotFound) {
				t.Fatal("повторное удаление должно вернуть ErrEventNotFound, а вернуло", err)
			}
		})
	}
}

// То, что бывает только в настоящем календаре
func TestGoogleSpecifics(t *testing.T) {
	ctx := context.Background()
	cal, fake := newFakeGoogle(t)
	loc := cal.Loc

	fake.add(googleEvent{Summary: "ДР Васи", Transparency: "transparent", Start: googleTime{Date: "2026-10-28"}, End: googleTime{Date: "2026-10-29"}})
	fake.add(googleEvent{Status: "cancelled", Summary: "Отменили", Start: googleTime{DateTime: "2026-10-28T10:00:00+03:00"}, End: googleTime{DateTime: "2026-10-28T11:00:00+03:00"}})
	fake.add(googleEvent{Summary: "", Start: googleTime{DateTime: "2026-10-28T12:00:00+03:00"}, End: googleTime{DateTime: "2026-10-28T13:00:00+03:00"}})
	fake.add(googleEvent{Summary: "Лондон", Start: googleTime{DateTime: "2026-10-28T12:00:00Z"}, End: googleTime{DateTime: "2026-10-28T13:00:00Z"}})
	fake.add(googleEvent{Summary: "Отпуск", Start: googleTime{Date: "2026-10-30"}, End: googleTime{Date: "2026-10-31"}})

	// Больше одной страницы (в подделке по 2 события) — должны собраться все, кроме отменённого
	events, err := cal.ListEvents(ctx, at(loc, "2026-10-28 00:00"), at(loc, "2026-10-29 00:00"))
	if err != nil || len(events) != 3 {
		t.Fatal("ожидали 3 события:", events, err)
	}
	for _, e := range events {
		if e.Title == "Отменили" {
			t.Fatal("отменённое событие попало в список")
		}
		if e.Title == "Лондон" && e.Start.Format("15:04") != "15:00" {
			t.Fatal("UTC не перевели в Москву:", e.Start)
		}
	}

	titles := []string{}
	for _, e := range events {
		titles = append(titles, e.Title)
	}
	if !strings.Contains(strings.Join(titles, ","), "(без названия)") {
		t.Fatal("событие без названия:", titles)
	}

	// «ДР Васи» на весь день со статусом «Свободен» не занимает время
	slots, err := FreeSlots(ctx, cal, at(loc, "2026-10-28 00:00"), 9, 19, time.Hour)
	if err != nil || len(slots) != 3 || slots[0].Start.Format("15:04") != "09:00" {
		t.Fatal("ДР не должен блокировать день:", slots, err)
	}

	// Обычное событие на весь день — день занят
	slots, err = FreeSlots(ctx, cal, at(loc, "2026-10-30 00:00"), 9, 19, time.Hour)
	if err != nil || len(slots) != 0 {
		t.Fatal("в отпуск окон быть не должно:", slots, err)
	}
}

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestGoogleErrors(t *testing.T) {
	ctx := context.Background()
	loc, _ := time.LoadLocation("Europe/Moscow")

	// Протухший токен → понятная подсказка, что делать
	cal := &GoogleCalendar{
		Client:     &http.Client{Transport: failingTransport{errors.New(`oauth2: "invalid_grant" "Token has been expired or revoked."`)}},
		BaseURL:    "http://google.invalid",
		CalendarID: "primary",
		Loc:        loc,
	}
	if _, err := cal.ListEvents(ctx, time.Now(), time.Now().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "google-auth") {
		t.Fatal("ожидали подсказку про google-auth:", err)
	}

	// Ошибка API доходит с текстом Google
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"Google Calendar API has not been used in project"}}`)
	}))
	defer srv.Close()

	cal = &GoogleCalendar{Client: srv.Client(), BaseURL: srv.URL, CalendarID: "primary", Loc: loc}
	if _, err := cal.ListEvents(ctx, time.Now(), time.Now().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "has not been used") {
		t.Fatal("ожидали текст ошибки Google:", err)
	}
}
