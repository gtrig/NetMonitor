package store

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// Devices returns the devices of network, or of all networks when network is "".
func (s *Store) Devices(ctx context.Context, network string) ([]model.Device, error) {
	q := `SELECT network, key, mac, ip, hostname, vendor, label, port, known, online, first_seen, last_seen, sources FROM devices`
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
		var ip, sources string
		var first, last int64
		if err := rows.Scan(&d.Network, &d.Key, &d.MAC, &ip, &d.Hostname, &d.Vendor, &d.Label, &d.Port,
			&d.Known, &d.Online, &first, &last, &sources); err != nil {
			return nil, err
		}
		d.IP, _ = netip.ParseAddr(ip)
		d.FirstSeen = time.Unix(first, 0).UTC()
		d.LastSeen = time.Unix(last, 0).UTC()
		if sources != "" {
			d.Sources = strings.Split(sources, ",")
		}
		out = append(out, d)
	}
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
		(network, key, mac, ip, hostname, vendor, port, online, first_seen, last_seen, sources)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (network, key) DO UPDATE SET
		mac = excluded.mac, ip = excluded.ip, hostname = excluded.hostname, vendor = excluded.vendor,
		port = excluded.port, online = excluded.online, first_seen = excluded.first_seen,
		last_seen = excluded.last_seen, sources = excluded.sources`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, d := range devs {
		ip := ""
		if d.IP.IsValid() {
			ip = d.IP.String()
		}
		if _, err := stmt.ExecContext(ctx, d.Network, d.Key, d.MAC, ip, d.Hostname, d.Vendor, d.Port, d.Online,
			d.FirstSeen.Unix(), d.LastSeen.Unix(), strings.Join(d.Sources, ",")); err != nil {
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

// LastSuccess returns the time of the network's last successful collection (zero if never).
func (s *Store) LastSuccess(ctx context.Context, network string) (time.Time, error) {
	var ts int64
	err := s.db.QueryRowContext(ctx, `SELECT last_success FROM network_state WHERE network = ?`, network).Scan(&ts)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return time.Unix(ts, 0).UTC(), nil
}

// SetLastSuccess records a successful collection time for network.
func (s *Store) SetLastSuccess(ctx context.Context, network string, t time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO network_state (network, last_success) VALUES (?, ?)
		ON CONFLICT (network) DO UPDATE SET last_success = excluded.last_success`, network, t.Unix())
	return err
}
