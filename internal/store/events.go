package store

import (
	"context"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// EventFilter selects events. Zero values mean "no filter"; Limit defaults to 100.
type EventFilter struct {
	Network string
	Devices []model.MemberRef // only events of these devices
	Limit   int
	Before  int64 // only events with ID < Before
}

// AddEvents stores events.
func (s *Store) AddEvents(ctx context.Context, evs []model.Event) error {
	if len(evs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range evs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (type, network, device_key, message, old, new, at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, e.Type, e.Network, e.DeviceKey, e.Message, e.Old, e.New, e.At.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Events returns events newest first.
func (s *Store) Events(ctx context.Context, f EventFilter) ([]model.Event, error) {
	q := `SELECT id, type, network, device_key, message, old, new, at FROM events WHERE 1=1`
	var args []any
	if f.Network != "" {
		q += ` AND network = ?`
		args = append(args, f.Network)
	}
	if len(f.Devices) > 0 {
		q += ` AND (0`
		for _, d := range f.Devices {
			q += ` OR (network = ? AND device_key = ?)`
			args = append(args, d.Network, d.Key)
		}
		q += `)`
	}
	if f.Before > 0 {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var at int64
		if err := rows.Scan(&e.ID, &e.Type, &e.Network, &e.DeviceKey, &e.Message, &e.Old, &e.New, &at); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
