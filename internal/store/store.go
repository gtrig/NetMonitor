// Package store persists devices, events and links in SQLite.
package store

import (
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Store is a SQLite-backed store.
type Store struct{ db *sql.DB }

var migrations = []string{
	`CREATE TABLE devices (
		network TEXT NOT NULL,
		key TEXT NOT NULL,
		mac TEXT NOT NULL DEFAULT '',
		ip TEXT NOT NULL DEFAULT '',
		hostname TEXT NOT NULL DEFAULT '',
		vendor TEXT NOT NULL DEFAULT '',
		label TEXT NOT NULL DEFAULT '',
		port TEXT NOT NULL DEFAULT '',
		known INTEGER NOT NULL DEFAULT 0,
		online INTEGER NOT NULL DEFAULT 0,
		first_seen INTEGER NOT NULL,
		last_seen INTEGER NOT NULL,
		sources TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (network, key)
	);
	CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL,
		network TEXT NOT NULL,
		device_key TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT '',
		old TEXT NOT NULL DEFAULT '',
		new TEXT NOT NULL DEFAULT '',
		at INTEGER NOT NULL
	);
	CREATE INDEX events_network ON events(network, id);
	CREATE TABLE links (
		network TEXT NOT NULL,
		source TEXT NOT NULL,
		a_kind TEXT NOT NULL, a_id TEXT NOT NULL,
		b_kind TEXT NOT NULL, b_id TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX links_network ON links(network, source);
	CREATE TABLE network_state (
		network TEXT NOT NULL,
		source TEXT NOT NULL,
		last_success INTEGER NOT NULL,
		PRIMARY KEY (network, source)
	);`,
	`ALTER TABLE devices ADD COLUMN seen_by TEXT NOT NULL DEFAULT '';`,
	`CREATE TABLE machine_members (
		network TEXT NOT NULL,
		key TEXT NOT NULL,
		machine_id INTEGER NOT NULL,
		PRIMARY KEY (network, key)
	);
	CREATE INDEX machine_members_id ON machine_members(machine_id);`,
	`CREATE TABLE device_details (
		network TEXT NOT NULL,
		key TEXT NOT NULL,
		scanned_at INTEGER NOT NULL,
		os TEXT NOT NULL DEFAULT '',
		os_accuracy INTEGER NOT NULL DEFAULT 0,
		ports TEXT NOT NULL DEFAULT '[]',
		PRIMARY KEY (network, key)
	);`,
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	var v int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database is usable.
func (s *Store) Ping() error { return s.db.Ping() }
