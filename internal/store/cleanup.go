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

// RenameNetwork moves everything stored under network from to network to. Where to already has
// a device with the same key, the existing row wins and an empty label (and the known flag) is
// filled from the moved row. Existing state of to is kept; links of from are dropped (the next
// collection recreates them).
func (s *Store) RenameNetwork(ctx context.Context, from, to string) error {
	if from == to {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		stmts := []string{
			`UPDATE devices AS t SET
				label = CASE WHEN t.label = '' THEN f.label ELSE t.label END,
				known = MAX(t.known, f.known)
			FROM devices AS f WHERE f.network = ?1 AND t.network = ?2 AND f.key = t.key`,
			`DELETE FROM devices WHERE network = ?1 AND key IN (SELECT key FROM devices WHERE network = ?2)`,
			`UPDATE devices SET network = ?2 WHERE network = ?1`,
			`UPDATE events SET network = ?2 WHERE network = ?1`,
			`DELETE FROM links WHERE network = ?1`,
			`UPDATE OR IGNORE network_state SET network = ?2 WHERE network = ?1`,
			`DELETE FROM network_state WHERE network = ?1`,
			`UPDATE OR IGNORE machine_members SET network = ?2 WHERE network = ?1`,
			`DELETE FROM machine_members WHERE network = ?1`,
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM machine_members WHERE network = ? AND key = ?`, network, key); err != nil {
			return err
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
