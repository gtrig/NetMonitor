package store

import (
	"context"
	"database/sql"
	"time"
)

// maxGaps is how many gaps are kept per device.
const maxGaps = 100

// AddGap stores how long a device was silent before it was seen again at ended.
func (s *Store) AddGap(ctx context.Context, network, key string, ended time.Time, d time.Duration) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO presence_gaps (network, key, ended_at, seconds) VALUES (?, ?, ?, ?)`,
			network, key, ended.Unix(), int64(d/time.Second)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM presence_gaps WHERE network = ?1 AND key = ?2 AND id NOT IN
			(SELECT id FROM presence_gaps WHERE network = ?1 AND key = ?2 ORDER BY id DESC LIMIT ?3)`, network, key, maxGaps)
		return err
	})
}

// Gaps returns the gaps of the devices of network that ended since a time.
func (s *Store) Gaps(ctx context.Context, network string, since time.Time) (map[string][]time.Duration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, seconds FROM presence_gaps WHERE network = ? AND ended_at >= ? ORDER BY id`, network, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]time.Duration{}
	for rows.Next() {
		var key string
		var sec int64
		if err := rows.Scan(&key, &sec); err != nil {
			return nil, err
		}
		out[key] = append(out[key], time.Duration(sec)*time.Second)
	}
	return out, rows.Err()
}

// PruneGaps deletes the gaps that ended before a time.
func (s *Store) PruneGaps(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM presence_gaps WHERE ended_at < ?`, before.Unix())
	return err
}
