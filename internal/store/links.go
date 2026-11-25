package store

import (
	"context"

	"github.com/gtrig/netmon/internal/model"
)

// ReplaceLinks replaces all links of (network, source) with links.
func (s *Store) ReplaceLinks(ctx context.Context, network, source string, links []model.Link) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM links WHERE network = ? AND source = ?`, network, source); err != nil {
		return err
	}
	for _, l := range links {
		if _, err := tx.ExecContext(ctx, `INSERT INTO links (network, source, a_kind, a_id, b_kind, b_id, label)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, network, source, l.A.Kind, l.A.ID, l.B.Kind, l.B.ID, l.Label); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Links returns all links of network ("" = all networks).
func (s *Store) Links(ctx context.Context, network string) ([]model.Link, error) {
	q := `SELECT network, source, a_kind, a_id, b_kind, b_id, label FROM links`
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
	var out []model.Link
	for rows.Next() {
		var l model.Link
		if err := rows.Scan(&l.Network, &l.Source, &l.A.Kind, &l.A.ID, &l.B.Kind, &l.B.ID, &l.Label); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
