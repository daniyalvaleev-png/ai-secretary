package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteCalendar — локальный календарь в файле. Первая реализация интерфейса Calendar:
// работает сразу, без OAuth, и на нём удобно тестировать агента.
type SQLiteCalendar struct {
	db  *sql.DB
	loc *time.Location
}

func NewSQLiteCalendar(path string, loc *time.Location) (*SQLiteCalendar, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	// Время храним в unix-секундах: так сравнение «пересекается ли» — простое сравнение чисел
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS events (
		id          TEXT    PRIMARY KEY,
		title       TEXT    NOT NULL,
		start_unix  INTEGER NOT NULL,
		end_unix    INTEGER NOT NULL,
		description TEXT    NOT NULL DEFAULT ''
	)`); err != nil {
		return nil, fmt.Errorf("создание таблицы: %w", err)
	}

	return &SQLiteCalendar{db: db, loc: loc}, nil
}

func (c *SQLiteCalendar) Close() error { return c.db.Close() }

func (c *SQLiteCalendar) ListEvents(ctx context.Context, from, to time.Time) ([]Event, error) {
	// Событие пересекается с [from, to), если начинается раньше to и заканчивается позже from
	rows, err := c.db.QueryContext(ctx, `SELECT id, title, start_unix, end_unix, description
		FROM events WHERE start_unix < ? AND end_unix > ? ORDER BY start_unix`,
		to.Unix(), from.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		var e Event
		var start, end int64
		if err := rows.Scan(&e.ID, &e.Title, &start, &end, &e.Description); err != nil {
			return nil, err
		}
		e.Start = time.Unix(start, 0).In(c.loc)
		e.End = time.Unix(end, 0).In(c.loc)
		events = append(events, e)
	}

	return events, rows.Err()
}

func (c *SQLiteCalendar) CreateEvent(ctx context.Context, e Event) (Event, error) {
	if !e.End.After(e.Start) {
		return Event{}, fmt.Errorf("конец события должен быть позже начала")
	}

	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return Event{}, err
	}
	e.ID = hex.EncodeToString(id)

	if _, err := c.db.ExecContext(ctx, `INSERT INTO events (id, title, start_unix, end_unix, description)
		VALUES (?, ?, ?, ?, ?)`, e.ID, e.Title, e.Start.Unix(), e.End.Unix(), e.Description); err != nil {
		return Event{}, err
	}

	e.Start = e.Start.In(c.loc)
	e.End = e.End.In(c.loc)
	return e, nil
}

func (c *SQLiteCalendar) DeleteEvent(ctx context.Context, id string) error {
	res, err := c.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrEventNotFound
	}
	return nil
}
