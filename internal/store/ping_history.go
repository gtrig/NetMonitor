package store

import (
	"context"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// PingCursor is a position in a device's history; the zero value is the newest end.
type PingCursor struct{ At, ID int64 }

// PingHistory returns the runs of a device since a time, oldest first.
func (s *Store) PingHistory(ctx context.Context, network, key string, since time.Time) ([]model.PingPoint, error) {
	out, _, err := s.pingRows(ctx, `SELECT id, at, origin, rtt_us FROM ping_history
		WHERE network = ? AND key = ? AND at >= ? ORDER BY at, id`, network, key, since.Unix())
	return out, err
}

// PingPage returns up to limit runs before cur, newest first, and where the next page starts
// (zero: there is none).
func (s *Store) PingPage(ctx context.Context, network, key string, cur PingCursor, limit int) ([]model.PingPoint, PingCursor, error) {
	if cur == (PingCursor{}) {
		cur = PingCursor{At: 1 << 62, ID: 1 << 62}
	}
	rows, pos, err := s.pingRows(ctx, `SELECT id, at, origin, rtt_us FROM ping_history
		WHERE network = ? AND key = ? AND (at, id) < (?, ?) ORDER BY at DESC, id DESC LIMIT ?`,
		network, key, cur.At, cur.ID, limit+1)
	if err != nil || len(rows) <= limit {
		return rows, PingCursor{}, err
	}
	return rows[:limit], pos[limit-1], nil
}

func (s *Store) pingRows(ctx context.Context, q string, args ...any) ([]model.PingPoint, []PingCursor, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []model.PingPoint
	var pos []PingCursor
	for rows.Next() {
		var c PingCursor
		var p model.PingPoint
		var us int64
		if err := rows.Scan(&c.ID, &c.At, &p.Origin, &us); err != nil {
			return nil, nil, err
		}
		p.At, p.OK = time.Unix(c.At, 0).UTC(), us >= 0
		if p.OK {
			p.RTT = time.Duration(us) * time.Microsecond
		}
		out, pos = append(out, p), append(pos, c)
	}
	return out, pos, rows.Err()
}

// PrunePings deletes the history before a time.
func (s *Store) PrunePings(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ping_history WHERE at < ?`, before.Unix())
	return err
}
