package store

import (
	"fmt"
)

// This file implements the meta key/value table: schema bookkeeping and
// any future workspace metadata. All SQL is parameterized.

// Meta returns a copy of every stored key/value pair.
func (s *Store) Meta() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM meta`)
	if err != nil {
		return nil, fmt.Errorf("store: cannot read meta: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: cannot scan meta row: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read meta: %w", err)
	}
	return out, nil
}

// SetMeta upserts one key/value pair. The write is retried on
// SQLITE_BUSY (concurrent writers across processes).
func (s *Store) SetMeta(key, value string) error {
	return retryBusy(func() error {
		if _, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return fmt.Errorf("store: cannot write meta %q: %w", key, err)
		}
		return nil
	})
}

// SchemaVersion reads the recorded schema version (0 when the meta
// table is missing entirely).
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v)
	if err != nil {
		if isNoRows(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("store: cannot read schema_version: %w", err)
	}
	return v, nil
}
