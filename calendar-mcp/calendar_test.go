package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestCalendar(t *testing.T) (*SQLiteCalendar, *time.Location) {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	cal, err := NewSQLiteCalendar(filepath.Join(t.TempDir(), "cal.db"), loc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cal.Close() })
	return cal, loc
}

func at(loc *time.Location, s string) time.Time {
	tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		panic(err)
	}
	return tm
}

func TestSQLiteCalendar(t *testing.T) {
	ctx := context.Background()
	cal, loc := newTestCalendar(t)

	e, err := cal.CreateEvent(ctx, Event{Title: "Парк", Start: at(loc, "2026-10-28 13:59"), End: at(loc, "2026-10-28 15:00")})
	if err != nil || e.ID == "" {
		t.Fatal(e, err)
	}
	if _, err := cal.CreateEvent(ctx, Event{Title: "кривое", Start: at(loc, "2026-10-28 15:00"), End: at(loc, "2026-10-28 14:00")}); err == nil {
		t.Fatal("конец раньше начала должен быть ошибкой")
	}

	// Пересечение: событие 13:59–15:00 попадает в 14:00–14:30, но не в 15:00–16:00 (граница не пересечение)
	if got, _ := cal.ListEvents(ctx, at(loc, "2026-10-28 14:00"), at(loc, "2026-10-28 14:30")); len(got) != 1 {
		t.Fatal("ожидали 1 событие, получили", len(got))
	}
	if got, _ := cal.ListEvents(ctx, at(loc, "2026-10-28 15:00"), at(loc, "2026-10-28 16:00")); len(got) != 0 {
		t.Fatal("граница не должна считаться пересечением", got)
	}
	got, _ := cal.ListEvents(ctx, at(loc, "2026-10-28 00:00"), at(loc, "2026-10-29 00:00"))
	if got[0].Start.Location().String() != "Europe/Moscow" || got[0].Start.Format("15:04") != "13:59" {
		t.Fatal("время вернулось не в том поясе:", got[0].Start)
	}

	if err := cal.DeleteEvent(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := cal.DeleteEvent(ctx, e.ID); err != ErrEventNotFound {
		t.Fatal("повторное удаление должно вернуть ErrEventNotFound, а вернуло", err)
	}
}

func TestFreeSlots(t *testing.T) {
	ctx := context.Background()
	cal, loc := newTestCalendar(t)
	day := at(loc, "2026-10-28 00:00")

	for _, ev := range []struct{ title, from, to string }{
		{"утро", "2026-10-28 08:00", "2026-10-28 09:30"}, // торчит из-за начала рабочего дня
		{"обед", "2026-10-28 12:00", "2026-10-28 13:00"},
		{"созвон", "2026-10-28 12:30", "2026-10-28 13:20"}, // пересекается с обедом
		{"вечер", "2026-10-28 18:30", "2026-10-28 20:00"},  // торчит за конец дня
	} {
		if _, err := cal.CreateEvent(ctx, Event{Title: ev.title, Start: at(loc, ev.from), End: at(loc, ev.to)}); err != nil {
			t.Fatal(err)
		}
	}

	slots, err := FreeSlots(ctx, cal, day, 9, 19, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"09:30-12:00", "13:20-18:30"}
	if len(slots) != len(want) {
		t.Fatalf("окон %d, ожидали %d: %v", len(slots), len(want), slots)
	}
	for i, s := range slots {
		if got := s.Start.Format("15:04") + "-" + s.End.Format("15:04"); got != want[i] {
			t.Errorf("окно %d: %s, ожидали %s", i, got, want[i])
		}
	}

	// Окна короче нужного не предлагаем: из двух окон только послеобеденное длиннее 5 часов
	slots, _ = FreeSlots(ctx, cal, day, 9, 19, 5*time.Hour)
	if len(slots) != 1 || slots[0].Start.Format("15:04") != "13:20" {
		t.Fatal("ожидали одно окно ≥5ч с 13:20:", slots)
	}
}
