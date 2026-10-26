package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Формат времени для модели: без часового пояса и секунд — так ей проще и меньше шансов ошибиться
const timeLayout = "2006-01-02 15:04"

var weekdaysRU = [...]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}

// tools — MCP-инструменты поверх любого Calendar. Про SQLite и Google они ничего не знают.
type tools struct {
	cal Calendar
	loc *time.Location
	now func() time.Time // подменяется в тестах
}

func newMCPServer(cal Calendar, loc *time.Location) *mcp.Server {
	t := &tools{cal: cal, loc: loc, now: time.Now}

	server := mcp.NewServer(&mcp.Implementation{Name: "calendar", Version: "v0.1.0"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "now",
		Description: "Текущие дата и время владельца календаря, день недели, а также даты «завтра» и «послезавтра». Вызывай первым, если в письме есть относительные даты: «завтра», «в пятницу», «через неделю».",
		// У инструмента нет аргументов. SDK сгенерировал бы схему без "properties",
		// а Ollama такую отвергает ("properties must be an object") — задаём пустой список явно.
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
	}, t.nowTool)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_events",
		Description: "События календаря за период. Время — в формате 2006-01-02 15:04 по часовому поясу владельца.",
	}, t.listEvents)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_free_slots",
		Description: "Свободные окна в рабочие часы выбранного дня. Используй, чтобы предложить время встречи или проверить, свободен ли владелец.",
	}, t.findFreeSlots)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_event",
		Description: "Создать событие в календаре. Если оно пересекается с другими, событие всё равно создаётся, а пересечения возвращаются в conflicts.",
	}, t.createEvent)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_event",
		Description: "Удалить событие по id (id берётся из list_events или create_event).",
	}, t.deleteEvent)

	return server
}

// --- Типы входа и выхода. Из них SDK сам строит JSON-схему для модели, jsonschema-тег = описание поля ---

type eventOut struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Start       string `json:"start"`
	End         string `json:"end"`
	Weekday     string `json:"weekday"`
	Description string `json:"description,omitempty"`
}

type slotOut struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type nowIn struct{}

type nowOut struct {
	Now              string `json:"now"`
	Weekday          string `json:"weekday"`
	Timezone         string `json:"timezone"`
	Tomorrow         string `json:"tomorrow"`
	DayAfterTomorrow string `json:"day_after_tomorrow"`
}

type listEventsIn struct {
	From string `json:"from" jsonschema:"начало периода: 2026-10-28 или 2026-10-28 09:00"`
	To   string `json:"to" jsonschema:"конец периода: 2026-10-30 или 2026-10-30 18:00; если указана только дата — весь этот день включительно"`
}

type listEventsOut struct {
	Events []eventOut `json:"events"`
}

type freeSlotsIn struct {
	Date            string `json:"date" jsonschema:"день: 2026-10-28"`
	DurationMinutes int    `json:"duration_minutes,omitempty" jsonschema:"минимальная длина окна в минутах, по умолчанию 60"`
	WorkStartHour   int    `json:"work_start_hour,omitempty" jsonschema:"начало рабочего дня, час 0-23, по умолчанию 9"`
	WorkEndHour     int    `json:"work_end_hour,omitempty" jsonschema:"конец рабочего дня, час 1-24, по умолчанию 19"`
}

type freeSlotsOut struct {
	Date    string    `json:"date"`
	Weekday string    `json:"weekday"`
	Slots   []slotOut `json:"slots"`
}

type createEventIn struct {
	Title           string `json:"title" jsonschema:"название события, коротко: «Встреча с Васей в парке»"`
	Start           string `json:"start" jsonschema:"начало: 2026-10-28 13:59"`
	End             string `json:"end,omitempty" jsonschema:"конец: 2026-10-28 15:00; если не указан — берётся duration_minutes"`
	DurationMinutes int    `json:"duration_minutes,omitempty" jsonschema:"длительность в минутах, если нет end; по умолчанию 60"`
	Description     string `json:"description,omitempty" jsonschema:"подробности: место, с кем, ссылка"`
}

type createEventOut struct {
	Event     eventOut   `json:"event"`
	Conflicts []eventOut `json:"conflicts"`
}

type deleteEventIn struct {
	ID string `json:"id" jsonschema:"id события"`
}

type deleteEventOut struct {
	Deleted bool `json:"deleted"`
}

// --- Обработчики. Возвращённая ошибка уходит модели как результат с isError — она увидит текст и сможет исправиться ---

func (t *tools) nowTool(ctx context.Context, _ *mcp.CallToolRequest, _ nowIn) (*mcp.CallToolResult, nowOut, error) {
	now := t.now().In(t.loc)
	return nil, nowOut{
		Now:              now.Format(timeLayout),
		Weekday:          weekdaysRU[now.Weekday()],
		Timezone:         t.loc.String(),
		Tomorrow:         t.dayLabel(now.AddDate(0, 0, 1)),
		DayAfterTomorrow: t.dayLabel(now.AddDate(0, 0, 2)),
	}, nil
}

func (t *tools) listEvents(ctx context.Context, _ *mcp.CallToolRequest, in listEventsIn) (*mcp.CallToolResult, listEventsOut, error) {
	from, _, err := t.parseTime(in.From)
	if err != nil {
		return nil, listEventsOut{}, fmt.Errorf("from: %w", err)
	}
	to, dateOnly, err := t.parseTime(in.To)
	if err != nil {
		return nil, listEventsOut{}, fmt.Errorf("to: %w", err)
	}
	if dateOnly {
		to = to.AddDate(0, 0, 1) // «до 30-го» = включая весь 30-й день
	}

	events, err := t.cal.ListEvents(ctx, from, to)
	if err != nil {
		return nil, listEventsOut{}, err
	}

	return nil, listEventsOut{Events: t.toOut(events)}, nil
}

func (t *tools) findFreeSlots(ctx context.Context, _ *mcp.CallToolRequest, in freeSlotsIn) (*mcp.CallToolResult, freeSlotsOut, error) {
	day, _, err := t.parseTime(in.Date)
	if err != nil {
		return nil, freeSlotsOut{}, fmt.Errorf("date: %w", err)
	}

	duration := orDefault(in.DurationMinutes, 60)
	workStart := orDefault(in.WorkStartHour, 9)
	workEnd := orDefault(in.WorkEndHour, 19)
	if workStart < 0 || workEnd > 24 || workStart >= workEnd {
		return nil, freeSlotsOut{}, fmt.Errorf("рабочие часы должны быть в пределах 0–24 и начало раньше конца")
	}

	slots, err := FreeSlots(ctx, t.cal, day, workStart, workEnd, time.Duration(duration)*time.Minute)
	if err != nil {
		return nil, freeSlotsOut{}, err
	}

	out := freeSlotsOut{Date: day.Format("2006-01-02"), Weekday: weekdaysRU[day.Weekday()], Slots: []slotOut{}}
	for _, s := range slots {
		out.Slots = append(out.Slots, slotOut{Start: s.Start.Format(timeLayout), End: s.End.Format(timeLayout)})
	}
	return nil, out, nil
}

func (t *tools) createEvent(ctx context.Context, _ *mcp.CallToolRequest, in createEventIn) (*mcp.CallToolResult, createEventOut, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, createEventOut{}, fmt.Errorf("title пустой")
	}

	start, dateOnly, err := t.parseTime(in.Start)
	if err != nil {
		return nil, createEventOut{}, fmt.Errorf("start: %w", err)
	}
	if dateOnly {
		return nil, createEventOut{}, fmt.Errorf("start: нужно время, а не только дата, например %s", start.Format("2006-01-02")+" 10:00")
	}

	end := start.Add(time.Duration(orDefault(in.DurationMinutes, 60)) * time.Minute)
	if in.End != "" {
		if end, _, err = t.parseTime(in.End); err != nil {
			return nil, createEventOut{}, fmt.Errorf("end: %w", err)
		}
	}

	// Пересечения ищем до создания — иначе новое событие «пересеклось бы» само с собой
	conflicts, err := t.cal.ListEvents(ctx, start, end)
	if err != nil {
		return nil, createEventOut{}, err
	}

	e, err := t.cal.CreateEvent(ctx, Event{Title: in.Title, Start: start, End: end, Description: in.Description})
	if err != nil {
		return nil, createEventOut{}, err
	}

	return nil, createEventOut{Event: t.toOut([]Event{e})[0], Conflicts: t.toOut(conflicts)}, nil
}

func (t *tools) deleteEvent(ctx context.Context, _ *mcp.CallToolRequest, in deleteEventIn) (*mcp.CallToolResult, deleteEventOut, error) {
	if err := t.cal.DeleteEvent(ctx, in.ID); err != nil {
		if errors.Is(err, ErrEventNotFound) {
			return nil, deleteEventOut{}, fmt.Errorf("события с id %q нет — проверь id через list_events", in.ID)
		}
		return nil, deleteEventOut{}, err
	}
	return nil, deleteEventOut{Deleted: true}, nil
}

// --- Помощники ---

// parseTime понимает «2026-10-28», «2026-10-28 13:59», «2026-10-28T13:59» и полный RFC3339.
// dateOnly = true, если времени не было — тогда это полночь начала дня.
func (t *tools) parseTime(s string) (tm time.Time, dateOnly bool, err error) {
	s = strings.TrimSpace(s)

	if tm, err := time.Parse(time.RFC3339, s); err == nil {
		return tm.In(t.loc), false, nil
	}
	for _, layout := range []string{timeLayout, "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if tm, err := time.ParseInLocation(layout, s, t.loc); err == nil {
			return tm, false, nil
		}
	}
	if tm, err := time.ParseInLocation("2006-01-02", s, t.loc); err == nil {
		return tm, true, nil
	}

	return time.Time{}, false, fmt.Errorf("не понял время %q — нужен формат 2026-10-28 или 2026-10-28 13:59", s)
}

func (t *tools) toOut(events []Event) []eventOut {
	out := []eventOut{}
	for _, e := range events {
		out = append(out, eventOut{
			ID:          e.ID,
			Title:       e.Title,
			Start:       e.Start.In(t.loc).Format(timeLayout),
			End:         e.End.In(t.loc).Format(timeLayout),
			Weekday:     weekdaysRU[e.Start.In(t.loc).Weekday()],
			Description: e.Description,
		})
	}
	return out
}

func (t *tools) dayLabel(d time.Time) string {
	return d.Format("2006-01-02") + " (" + weekdaysRU[d.Weekday()] + ")"
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
