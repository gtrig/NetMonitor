package store

import (
	"context"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// JobRunFilter selects job runs. Zero values mean "no filter"; Limit defaults to 100.
type JobRunFilter struct {
	Network, Kind, Outcome string
	Limit                  int
	Before                 int64 // only runs with ID < Before
}

// AddJobRun stores a finished run.
func (s *Store) AddJobRun(ctx context.Context, r model.JobRun) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_runs (network, kind, source, key, trigger, at, duration_ms, outcome, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.Network, r.Kind, r.Source, r.Key, r.Trigger, r.At.Unix(), r.Duration.Milliseconds(), r.Outcome, r.Message)
	return err
}

// JobRuns returns finished runs, newest first.
func (s *Store) JobRuns(ctx context.Context, f JobRunFilter) ([]model.JobRun, error) {
	q := `SELECT id, network, kind, source, key, trigger, at, duration_ms, outcome, message FROM job_runs WHERE 1=1`
	var args []any
	for _, c := range [][2]string{{"network", f.Network}, {"kind", f.Kind}, {"outcome", f.Outcome}} {
		if c[1] != "" {
			q += ` AND ` + c[0] + ` = ?`
			args = append(args, c[1])
		}
	}
	if f.Before > 0 {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY id DESC LIMIT ?`, append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.JobRun
	for rows.Next() {
		var r model.JobRun
		var at, ms int64
		if err := rows.Scan(&r.ID, &r.Network, &r.Kind, &r.Source, &r.Key, &r.Trigger, &at, &ms, &r.Outcome, &r.Message); err != nil {
			return nil, err
		}
		r.At, r.Duration = time.Unix(at, 0).UTC(), time.Duration(ms)*time.Millisecond
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneJobRuns deletes the runs before a time.
func (s *Store) PruneJobRuns(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM job_runs WHERE at < ?`, before.Unix())
	return err
}
