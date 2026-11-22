package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gtrig/netmon/internal/model"
)

// Machines returns the machine id of every device row that belongs to a machine.
func (s *Store) Machines(ctx context.Context) (map[model.MemberRef]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT network, key, machine_id FROM machine_members`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[model.MemberRef]int64{}
	for rows.Next() {
		var r model.MemberRef
		var id int64
		if err := rows.Scan(&r.Network, &r.Key, &id); err != nil {
			return nil, err
		}
		out[r] = id
	}
	return out, rows.Err()
}

// MergeMachine groups refs (at least two existing devices) into one machine. Machines the refs
// already belong to are folded into the one with the smallest id. It returns the machine id.
func (s *Store) MergeMachine(ctx context.Context, refs []model.MemberRef) (int64, error) {
	uniq := map[model.MemberRef]bool{}
	for _, r := range refs {
		uniq[r] = true
	}
	if len(uniq) < 2 {
		return 0, errors.New("a machine needs at least two devices")
	}
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var existing []int64
		for r := range uniq {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE network = ? AND key = ?`, r.Network, r.Key).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("%w: device %s in %s", ErrNotFound, r.Key, r.Network)
			}
			var m int64
			err := tx.QueryRowContext(ctx, `SELECT machine_id FROM machine_members WHERE network = ? AND key = ?`, r.Network, r.Key).Scan(&m)
			switch {
			case err == nil:
				existing = append(existing, m)
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		if len(existing) > 0 {
			id = existing[0]
			for _, m := range existing {
				id = min(id, m)
			}
		} else if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(machine_id), 0) + 1 FROM machine_members`).Scan(&id); err != nil {
			return err
		}
		for _, m := range existing {
			if _, err := tx.ExecContext(ctx, `UPDATE machine_members SET machine_id = ? WHERE machine_id = ?`, id, m); err != nil {
				return err
			}
		}
		for r := range uniq {
			if _, err := tx.ExecContext(ctx, `INSERT INTO machine_members (network, key, machine_id) VALUES (?, ?, ?)
				ON CONFLICT (network, key) DO UPDATE SET machine_id = excluded.machine_id`, r.Network, r.Key, id); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// SplitMember removes ref from its machine; a machine left with one member is dissolved.
func (s *Store) SplitMember(ctx context.Context, ref model.MemberRef) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM machine_members WHERE network = ? AND key = ?`, ref.Network, ref.Key); err != nil {
			return err
		}
		return pruneMachines(ctx, tx)
	})
}

// pruneMachines dissolves machines with fewer than two members.
func pruneMachines(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM machine_members WHERE machine_id IN
		(SELECT machine_id FROM machine_members GROUP BY machine_id HAVING COUNT(*) < 2)`)
	return err
}

// moveMembership hands the machine membership of (network, from) to (network, to), unless to
// already belongs to a machine; the from membership is removed either way.
func moveMembership(ctx context.Context, tx *sql.Tx, network, from, to string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE machine_members SET key = ? WHERE network = ? AND key = ?`, to, network, from); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM machine_members WHERE network = ? AND key = ?`, network, from); err != nil {
		return err
	}
	return pruneMachines(ctx, tx)
}
