package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

const (
	pingWindow    = 5 // runs kept per device
	pingMaxMisses = 3 // this many misses in a row: no current value
)

// RecordPings adds one ping run to the stored times of network. A change of origin starts over,
// since times from different origins don't compare. Devices that are no longer stored are skipped.
func (s *Store) RecordPings(ctx context.Context, network, origin string, at time.Time, samples []model.PingSample) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, sm := range samples {
			var known int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE network = ? AND key = ?`, network, sm.Key).Scan(&known); err != nil {
				return err
			}
			if known == 0 {
				continue
			}
			var prevOrigin, raw string
			var answered int64
			err := tx.QueryRowContext(ctx, `SELECT origin, samples, answered_at FROM device_pings WHERE network = ? AND key = ?`,
				network, sm.Key).Scan(&prevOrigin, &raw, &answered)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			var us []int64
			if err == nil && prevOrigin == origin {
				json.Unmarshal([]byte(raw), &us) // unreadable: start over
			} else {
				answered = 0
			}
			v := int64(-1)
			if sm.RTT >= 0 {
				v, answered = sm.RTT.Microseconds(), at.Unix()
			}
			us = append(us, v)
			if len(us) > pingWindow {
				us = us[len(us)-pingWindow:]
			}
			b, err := json.Marshal(us)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO device_pings (network, key, origin, samples, answered_at) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (network, key) DO UPDATE SET origin = excluded.origin, samples = excluded.samples, answered_at = excluded.answered_at`,
				network, sm.Key, origin, string(b), answered); err != nil {
				return err
			}
		}
		return nil
	})
}

// Pings returns the smoothed ping times of network ("" = all networks).
func (s *Store) Pings(ctx context.Context, network string) (map[model.MemberRef]model.Ping, error) {
	q := `SELECT network, key, origin, samples, answered_at FROM device_pings`
	var args []any
	if network != "" {
		q += ` WHERE network = ?`
		args = append(args, network)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[model.MemberRef]model.Ping{}
	for rows.Next() {
		var p model.Ping
		var raw string
		var at int64
		if err := rows.Scan(&p.Network, &p.Key, &p.Origin, &raw, &at); err != nil {
			return nil, err
		}
		var us []int64
		json.Unmarshal([]byte(raw), &us) // unreadable: no value
		if at > 0 {
			p.At = time.Unix(at, 0).UTC()
		}
		p.RTT, p.OK = smooth(us)
		out[model.MemberRef{Network: p.Network, Key: p.Key}] = p
	}
	return out, rows.Err()
}

// smooth is the median of the answered runs; ok is false after pingMaxMisses misses in a row.
func smooth(us []int64) (time.Duration, bool) {
	var answered []int64
	misses := 0
	for _, v := range us {
		if v < 0 {
			misses++
			continue
		}
		misses = 0
		answered = append(answered, v)
	}
	if len(answered) == 0 || misses >= pingMaxMisses {
		return 0, false
	}
	slices.Sort(answered)
	n := len(answered)
	return time.Duration((answered[(n-1)/2]+answered[n/2])/2) * time.Microsecond, true
}

// movePings hands the times of (network, from) to (network, to) unless to was answered later.
func movePings(ctx context.Context, tx *sql.Tx, network, from, to string) error {
	for _, q := range []string{
		`DELETE FROM device_pings WHERE network = ?1 AND key = ?3
			AND answered_at < (SELECT answered_at FROM device_pings WHERE network = ?1 AND key = ?2)`,
		`UPDATE OR IGNORE device_pings SET key = ?3 WHERE network = ?1 AND key = ?2`,
		`DELETE FROM device_pings WHERE network = ?1 AND key = ?2 AND ?3 <> ?2`,
	} {
		if _, err := tx.ExecContext(ctx, q, network, from, to); err != nil {
			return err
		}
	}
	return nil
}
