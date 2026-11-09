package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const googleCalendarAPI = "https://www.googleapis.com/calendar/v3"

// GoogleCalendar — реализация Calendar поверх Google Calendar API.
// Для MCP-инструментов и агента она неотличима от SQLiteCalendar.
type GoogleCalendar struct {
	Client     *http.Client // сам подставляет access token и обновляет его
	BaseURL    string       // в тестах подменяется на поддельный сервер
	CalendarID string       // "primary" — основной календарь аккаунта
	Loc        *time.Location
}

// Как Google описывает событие. У обычного события заполнен dateTime, у события на целый день — только date.
type googleTime struct {
	DateTime string `json:"dateTime,omitempty"`
	Date     string `json:"date,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

type googleEvent struct {
	ID          string     `json:"id,omitempty"`
	Status      string     `json:"status,omitempty"`
	Summary     string     `json:"summary"`
	Description string     `json:"description,omitempty"`
	Start       googleTime `json:"start"`
	End         googleTime `json:"end"`
	// "transparent" — в Google отмечено «Свободен»: событие есть, но время не занимает
	Transparency string `json:"transparency,omitempty"`
}

func (c *GoogleCalendar) eventsURL() string {
	return c.BaseURL + "/calendars/" + url.PathEscape(c.CalendarID) + "/events"
}

func (c *GoogleCalendar) parseTime(t googleTime) (time.Time, error) {
	if t.DateTime != "" {
		parsed, err := time.Parse(time.RFC3339, t.DateTime)
		if err != nil {
			return time.Time{}, err
		}
		return parsed.In(c.Loc), nil
	}
	// Событие на целый день: полночь по поясу владельца. Конец у Google не включается в событие.
	return time.ParseInLocation("2006-01-02", t.Date, c.Loc)
}

func (c *GoogleCalendar) toEvent(g googleEvent) (Event, error) {
	start, err := c.parseTime(g.Start)
	if err != nil {
		return Event{}, fmt.Errorf("начало события %s: %w", g.ID, err)
	}
	end, err := c.parseTime(g.End)
	if err != nil {
		return Event{}, fmt.Errorf("конец события %s: %w", g.ID, err)
	}

	title := g.Summary
	if title == "" {
		title = "(без названия)"
	}

	return Event{
		ID:          g.ID,
		Title:       title,
		Start:       start,
		End:         end,
		Description: g.Description,
		Free:        g.Transparency == "transparent",
	}, nil
}

// do выполняет запрос и превращает ответы Google с ошибкой в понятный текст
func (c *GoogleCalendar) do(ctx context.Context, method, target string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "invalid_grant") {
			return nil, fmt.Errorf("доступ к Google Календарю протух — запусти в calendar-mcp: go run . google-auth")
		}
		return nil, fmt.Errorf("google calendar: %w", err)
	}
	return resp, nil
}

func googleError(resp *http.Response) error {
	text, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
	return fmt.Errorf("google calendar HTTP %d: %s", resp.StatusCode, text)
}

func (c *GoogleCalendar) ListEvents(ctx context.Context, from, to time.Time) ([]Event, error) {
	events := []Event{}
	pageToken := ""

	for {
		q := url.Values{}
		q.Set("timeMin", from.Format(time.RFC3339))
		q.Set("timeMax", to.Format(time.RFC3339))
		q.Set("singleEvents", "true") // повторяющиеся события разворачиваем в отдельные
		q.Set("orderBy", "startTime")
		q.Set("maxResults", strconv.Itoa(250))
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}

		page, next, err := c.listPage(ctx, q)
		if err != nil {
			return nil, err
		}
		events = append(events, page...)

		if next == "" {
			return events, nil
		}
		pageToken = next
	}
}

func (c *GoogleCalendar) listPage(ctx context.Context, q url.Values) ([]Event, string, error) {
	resp, err := c.do(ctx, http.MethodGet, c.eventsURL()+"?"+q.Encode(), nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", googleError(resp)
	}

	var page struct {
		Items         []googleEvent `json:"items"`
		NextPageToken string        `json:"nextPageToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, "", fmt.Errorf("кривой ответ Google: %w", err)
	}

	var events []Event
	for _, g := range page.Items {
		if g.Status == "cancelled" {
			continue
		}
		e, err := c.toEvent(g)
		if err != nil {
			return nil, "", err
		}
		events = append(events, e)
	}
	return events, page.NextPageToken, nil
}

func (c *GoogleCalendar) CreateEvent(ctx context.Context, e Event) (Event, error) {
	if !e.End.After(e.Start) {
		return Event{}, fmt.Errorf("конец события должен быть позже начала")
	}

	resp, err := c.do(ctx, http.MethodPost, c.eventsURL(), googleEvent{
		Summary:     e.Title,
		Description: e.Description,
		Start:       googleTime{DateTime: e.Start.In(c.Loc).Format(time.RFC3339), TimeZone: c.Loc.String()},
		End:         googleTime{DateTime: e.End.In(c.Loc).Format(time.RFC3339), TimeZone: c.Loc.String()},
	})
	if err != nil {
		return Event{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Event{}, googleError(resp)
	}

	var created googleEvent
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return Event{}, fmt.Errorf("кривой ответ Google: %w", err)
	}
	return c.toEvent(created)
}

func (c *GoogleCalendar) DeleteEvent(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, c.eventsURL()+"/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound, http.StatusGone: // 410 — событие уже удалено
		return ErrEventNotFound
	default:
		return googleError(resp)
	}
}
