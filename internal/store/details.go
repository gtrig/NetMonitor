package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// SaveDetails replaces the stored detailed-scan result of each device.
func (s *Store) SaveDetails(ctx context.Context, ds []model.DeviceDetails) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, d := range ds {
			ports := d.Ports
			if ports == nil {
				ports = []model.Port{}
			}
			b, err := json.Marshal(ports)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO device_details (network, key, scanned_at, os, os_accuracy, ports)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (network, key) DO UPDATE SET scanned_at = excluded.scanned_at, os = excluded.os,
				os_accuracy = excluded.os_accuracy, ports = excluded.ports`,
				d.Network, d.Key, d.ScannedAt.Unix(), d.OS, d.OSAccuracy, string(b)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Details returns the stored detailed-scan results of network ("" = all networks).
func (s *Store) Details(ctx context.Context, network string) (map[model.MemberRef]model.DeviceDetails, error) {
	q := `SELECT network, key, scanned_at, os, os_accuracy, ports FROM device_details`
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
	out := map[model.MemberRef]model.DeviceDetails{}
	for rows.Next() {
		var d model.DeviceDetails
		var at int64
		var ports string
		if err := rows.Scan(&d.Network, &d.Key, &at, &d.OS, &d.OSAccuracy, &ports); err != nil {
			return nil, err
		}
		d.ScannedAt = time.Unix(at, 0).UTC()
		if err := json.Unmarshal([]byte(ports), &d.Ports); err != nil || d.Ports == nil {
			d.Ports = []model.Port{}
		}
		out[model.MemberRef{Network: d.Network, Key: d.Key}] = d
	}
	return out, rows.Err()
}

// moveDetails hands the details of (network, from) to (network, to) unless to has newer ones.
func moveDetails(ctx context.Context, tx *sql.Tx, network, from, to string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM device_details WHERE network = ?1 AND key = ?3
		AND scanned_at < (SELECT scanned_at FROM device_details WHERE network = ?1 AND key = ?2)`, network, from, to); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE device_details SET key = ? WHERE network = ? AND key = ?`, to, network, from); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM device_details WHERE network = ? AND key = ?`, network, from)
	return err
}
