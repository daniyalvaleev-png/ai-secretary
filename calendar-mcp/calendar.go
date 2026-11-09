package main

import (
	"context"
	"errors"
	"sort"
	"time"
)

// Event — событие календаря. Время всегда с часовым поясом.
type Event struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Description string    `json:"description,omitempty"`
	Free        bool      `json:"free,omitempty"` // событие есть, но время не занимает (в Google — «Свободен»)
}

// Calendar — всё, что секретарю нужно от календаря.
// Сегодня за ним SQLite, завтра Google Calendar — MCP-сервер и агент разницы не заметят.
type Calendar interface {
	// ListEvents — события, которые пересекаются с промежутком [from, to), по возрастанию начала
	ListEvents(ctx context.Context, from, to time.Time) ([]Event, error)
	CreateEvent(ctx context.Context, e Event) (Event, error)
	DeleteEvent(ctx context.Context, id string) error
}

var ErrEventNotFound = errors.New("событие не найдено")

type Slot struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// FreeSlots — свободные окна не короче minDuration в рабочие часы дня day.
// Считает код, а не модель: в арифметике времени маленькие LLM постоянно путаются.
func FreeSlots(ctx context.Context, cal Calendar, day time.Time, workStart, workEnd int, minDuration time.Duration) ([]Slot, error) {
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), workStart, 0, 0, 0, day.Location())
	dayEnd := time.Date(day.Year(), day.Month(), day.Day(), workEnd, 0, 0, 0, day.Location())

	events, err := cal.ListEvents(ctx, dayStart, dayEnd)
	if err != nil {
		return nil, err
	}
	events = busyOnly(events)
	sort.Slice(events, func(i, j int) bool { return events[i].Start.Before(events[j].Start) })

	var slots []Slot
	cursor := dayStart
	for _, e := range events {
		if e.Start.After(cursor) {
			addSlot(&slots, cursor, minTime(e.Start, dayEnd), minDuration)
		}
		if e.End.After(cursor) {
			cursor = e.End
		}
	}
	addSlot(&slots, cursor, dayEnd, minDuration)

	return slots, nil
}

func addSlot(slots *[]Slot, start, end time.Time, minDuration time.Duration) {
	if end.Sub(start) >= minDuration {
		*slots = append(*slots, Slot{Start: start, End: end})
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// busyOnly убирает события, которые не занимают время: день рождения друга — не повод отказывать во встрече
func busyOnly(events []Event) []Event {
	busy := []Event{}
	for _, e := range events {
		if !e.Free {
			busy = append(busy, e)
		}
	}
	return busy
}
