package store

import (
	"context"
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// Devices returns the devices of network, or of all networks when network is "".
func (s *Store) Devices(ctx context.Context, network string) ([]model.Device, error) {
	q := `SELECT network, key, mac, ip, hostname, vendor, label, port, known, online, first_seen, last_seen, sources, seen_by FROM devices`
	var args []any
	if network != "" {
		q += ` WHERE network = ?`
		args = append(args, network)
	}
	q += ` ORDER BY network, ip`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Device
	for rows.Next() {
		var d model.Device
		var ip, sources, seenBy string
		var first, last int64
		if err := rows.Scan(&d.Network, &d.Key, &d.MAC, &ip, &d.Hostname, &d.Vendor, &d.Label, &d.Port,
			&d.Known, &d.Online, &first, &last, &sources, &seenBy); err != nil {
			return nil, err
		}
		d.IP, _ = netip.ParseAddr(ip)
		d.FirstSeen = time.Unix(first, 0).UTC()
		d.LastSeen = time.Unix(last, 0).UTC()
		if sources != "" {
			d.Sources = strings.Split(sources, ",")
		}
		if seenBy != "" {
			var m map[string]int64
			if json.Unmarshal([]byte(seenBy), &m) == nil {
				d.SeenBy = make(map[string]time.Time, len(m))
				for k, v := range m {
					d.SeenBy[k] = time.Unix(v, 0).UTC()
				}
			}
		}
		out = append(out, d)
	}
	slices.SortStableFunc(out, func(a, b model.Device) int {
		if c := strings.Compare(a.Network, b.Network); c != 0 {
			return c
		}
		return a.IP.Compare(b.IP)
	})
	return out, rows.Err()
}

// UpsertDevices inserts or updates devices. Label and Known are never overwritten (see SetDeviceMeta).
func (s *Store) UpsertDevices(ctx context.Context, devs []model.Device) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO devices
		(network, key, mac, ip, hostname, vendor, port, online, first_seen, last_seen, sources, seen_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (network, key) DO UPDATE SET
		mac = excluded.mac, ip = excluded.ip, hostname = excluded.hostname, vendor = excluded.vendor,
		port = excluded.port, online = excluded.online, first_seen = excluded.first_seen,
		last_seen = excluded.last_seen, sources = excluded.sources, seen_by = excluded.seen_by`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, d := range devs {
		if d.MergedInto != "" {
			// Move user metadata to the surviving device, then drop this row.
			if _, err := tx.ExecContext(ctx, `UPDATE devices SET
				label = CASE WHEN label = '' THEN (SELECT label FROM devices WHERE network = ?1 AND key = ?2) ELSE label END,
				known = MAX(known, (SELECT known FROM devices WHERE network = ?1 AND key = ?2))
				WHERE network = ?1 AND key = ?3 AND EXISTS (SELECT 1 FROM devices WHERE network = ?1 AND key = ?2)`,
				d.Network, d.Key, d.MergedInto); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE network = ? AND key = ?`, d.Network, d.Key); err != nil {
				return err
			}
			continue
		}
		if d.PrevKey != "" && d.PrevKey != d.Key {
			// Rename keeps label/known; if the new key already exists, drop the old row instead.
			if _, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE network = ? AND key = ?
				AND EXISTS (SELECT 1 FROM devices WHERE network = ? AND key = ?)`, d.Network, d.PrevKey, d.Network, d.Key); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE devices SET key = ? WHERE network = ? AND key = ?`, d.Key, d.Network, d.PrevKey); err != nil {
				return err
			}
		}
		ip := ""
		if d.IP.IsValid() {
			ip = d.IP.String()
		}
		seen := ""
		if len(d.SeenBy) > 0 {
			m := make(map[string]int64, len(d.SeenBy))
			for k, v := range d.SeenBy {
				m[k] = v.Unix()
			}
			b, _ := json.Marshal(m)
			seen = string(b)
		}
		if _, err := stmt.ExecContext(ctx, d.Network, d.Key, d.MAC, ip, d.Hostname, d.Vendor, d.Port, d.Online,
			d.FirstSeen.Unix(), d.LastSeen.Unix(), strings.Join(d.Sources, ","), seen); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetDeviceMeta sets the user label and/or known flag for every device row with key.
func (s *Store) SetDeviceMeta(ctx context.Context, key string, label *string, known *bool) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE key = ?`, key).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if label != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE devices SET label = ? WHERE key = ?`, *label, key); err != nil {
			return err
		}
	}
	if known != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE devices SET known = ? WHERE key = ?`, *known, key); err != nil {
			return err
		}
	}
	return nil
}

// IsKnown reports whether any device row with key is marked known.
func (s *Store) IsKnown(ctx context.Context, key string) bool {
	var known bool
	s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(known), 0) FROM devices WHERE key = ?`, key).Scan(&known)
	return known
}

// LastSuccesses returns source name → time of its last successful collection for network.
func (s *Store) LastSuccesses(ctx context.Context, network string) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, last_success FROM network_state WHERE network = ?`, network)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var src string
		var ts int64
		if err := rows.Scan(&src, &ts); err != nil {
			return nil, err
		}
		out[src] = time.Unix(ts, 0).UTC()
	}
	return out, rows.Err()
}

// SetLastSuccess records a successful collection time for (network, source).
func (s *Store) SetLastSuccess(ctx context.Context, network, source string, t time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO network_state (network, source, last_success) VALUES (?, ?, ?)
		ON CONFLICT (network, source) DO UPDATE SET last_success = excluded.last_success`, network, source, t.Unix())
	return err
}
