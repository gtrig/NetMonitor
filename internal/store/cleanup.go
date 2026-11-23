package store

import (
	"context"
	"database/sql"
	"sort"
)

// NetworkData summarizes what is stored under one network name.
type NetworkData struct {
	Name    string `json:"name"`
	Devices int    `json:"devices"`
	Events  int    `json:"events"`
}

// RenameNetwork moves everything stored under network from to network to. Where both hold a
// device with the same key, the more recently seen row wins and an empty label (and the known flag) is
// filled from the moved row. Existing state of to is kept; links of from are dropped (the next
// collection recreates them).
func (s *Store) RenameNetwork(ctx context.Context, from, to string) error {
	if from == to {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		stmts := []string{
			// Where both names hold the same key, each row fills the other's empty label and the
			// known flag; then the row seen most recently survives.
			`UPDATE devices AS t SET
				label = CASE WHEN t.label = '' THEN f.label ELSE t.label END,
				known = MAX(t.known, f.known)
			FROM devices AS f WHERE f.network = ?1 AND t.network = ?2 AND f.key = t.key`,
			`UPDATE devices AS f SET
				label = CASE WHEN f.label = '' THEN t.label ELSE f.label END,
				known = MAX(t.known, f.known)
			FROM devices AS t WHERE f.network = ?1 AND t.network = ?2 AND f.key = t.key`,
			`DELETE FROM devices WHERE network = ?2 AND key IN
				(SELECT f.key FROM devices AS f WHERE f.network = ?1 AND f.last_seen > devices.last_seen)`,
			`DELETE FROM devices WHERE network = ?1 AND key IN (SELECT key FROM devices WHERE network = ?2)`,
			`UPDATE devices SET network = ?2 WHERE network = ?1`,
			`UPDATE events SET network = ?2 WHERE network = ?1`,
			`DELETE FROM links WHERE network = ?1`,
			`UPDATE OR IGNORE network_state SET network = ?2 WHERE network = ?1`,
			`DELETE FROM network_state WHERE network = ?1`,
			`UPDATE OR IGNORE machine_members SET network = ?2 WHERE network = ?1`,
			`DELETE FROM machine_members WHERE network = ?1`,
			`DELETE FROM device_details WHERE network = ?2 AND key IN
				(SELECT f.key FROM device_details AS f WHERE f.network = ?1 AND f.scanned_at > device_details.scanned_at)`,
			`UPDATE OR IGNORE device_details SET network = ?2 WHERE network = ?1`,
			`DELETE FROM device_details WHERE network = ?1`,
			`DELETE FROM device_pings WHERE network = ?2 AND key IN
				(SELECT f.key FROM device_pings AS f WHERE f.network = ?1 AND f.answered_at > device_pings.answered_at)`,
			`UPDATE OR IGNORE device_pings SET network = ?2 WHERE network = ?1`,
			`DELETE FROM device_pings WHERE network = ?1`,
			`UPDATE ping_history SET network = ?2 WHERE network = ?1`,
		}
		for _, q := range stmts {
			if _, err := tx.ExecContext(ctx, q, from, to); err != nil {
				return err
			}
		}
		return pruneMachines(ctx, tx)
	})
}

// DataNetworks lists every network name that has stored devices, events or state.
func (s *Store) DataNetworks(ctx context.Context) ([]NetworkData, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.network,
			(SELECT COUNT(*) FROM devices d WHERE d.network = n.network),
			(SELECT COUNT(*) FROM events e WHERE e.network = n.network)
		FROM (SELECT network FROM devices UNION SELECT network FROM events UNION SELECT network FROM network_state) AS n`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetworkData
	for rows.Next() {
		var n NetworkData
		if err := rows.Scan(&n.Name, &n.Devices, &n.Events); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, rows.Err()
}

// DeleteNetworkData removes the devices, links, state and machine membership of a network.
// Its events are kept.
func (s *Store) DeleteNetworkData(ctx context.Context, name string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM devices WHERE network = ?`,
			`DELETE FROM links WHERE network = ?`,
			`DELETE FROM network_state WHERE network = ?`,
			`DELETE FROM machine_members WHERE network = ?`,
			`DELETE FROM device_details WHERE network = ?`,
			`DELETE FROM device_pings WHERE network = ?`,
			`DELETE FROM ping_history WHERE network = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, name); err != nil {
				return err
			}
		}
		return pruneMachines(ctx, tx)
	})
}

// DeleteDevice removes one device row (it reappears if a source sees it again).
func (s *Store) DeleteDevice(ctx context.Context, network, key string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE network = ? AND key = ?`, network, key)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		for _, q := range []string{`DELETE FROM machine_members WHERE network = ? AND key = ?`,
			`DELETE FROM device_details WHERE network = ? AND key = ?`,
			`DELETE FROM device_pings WHERE network = ? AND key = ?`,
			`DELETE FROM ping_history WHERE network = ? AND key = ?`} {
			if _, err := tx.ExecContext(ctx, q, network, key); err != nil {
				return err
			}
		}
		return pruneMachines(ctx, tx)
	})
}

func (s *Store) tx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// MergeStaleNetwork moves the data of a network that is no longer collected into network to.
// Moved rows are marked offline: nothing has checked them since collection stopped.
func (s *Store) MergeStaleNetwork(ctx context.Context, from, to string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE devices SET online = 0 WHERE network = ?`, from); err != nil {
		return err
	}
	return s.RenameNetwork(ctx, from, to)
}
