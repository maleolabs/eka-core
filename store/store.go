// Package store implements the EKA Workspace database: the local SQLite
// canonical store backing the EKA Knowledge Runtime (milestone EKA
// v0.2.0). The workspace keeps the canonical projection of every
// registered repository's knowledge: immutable Engineering Knowledge
// Objects (content-addressed payloads), their mutable references,
// attachments, and the sync log.
//
// The Immutable Engineering Knowledge Model: knowledge objects are
// IMMUTABLE and content-addressed. Every change produces a new
// immutable payload; mutable state is limited to references (object_refs)
// and indexes. SQLite is only the persistence layer — it is never the
// source of immutability (the store API has no update path for payload
// rows; hashes are content-derived SHA-256, never database-generated).
// History emerges from immutable payloads + references (prev_hash
// lineage); there is no dedicated mutable history table. Relationships
// and change logs are serialized inside the payload's unit.json.
//
// The database lives at <workspace>/eka.db. It is opened with the
// driver modernc.org/sqlite (pure Go, no cgo) and the following
// pragmas:
//
//	journal_mode(WAL)   concurrent readers + one writer
//	busy_timeout(30000) serialize writers instead of failing fast
//	foreign_keys(1)     referential integrity between tables
//
// Concurrency model (documented decision): the runtime assumes a single
// writer (the CLI is single-process), but parallel eka processes (e.g.
// agents in concurrent workspaces) share the single global database, so
// writers can collide. Concurrency is handled at three levels, all
// SQLite-only — no flock is implemented:
//
//  1. Open on a database at the current schema version is READ-ONLY
//     (migrate's fast path, schema.go): read-only commands never take
//     the WAL write lock, so they cannot contend with writers at all.
//  2. Write operations (migrate, PutUnit(s), SetMeta, RecordSync,
//     UpsertAttachment, SetRepoNamespace) retry SQLITE_BUSY with a
//     bounded backoff (busy.go): modernc's busy handler demonstrably
//     does NOT engage on the WAL write path across processes, so the
//     retry loop is the reliable serialization mechanism for writers.
//  3. busy_timeout(30000) in the DSN serializes the paths where the
//     busy handler does engage (in-process contention), as defense in
//     depth.
//
// All SQL is parameterized; values are never interpolated into
// statements.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver on import.
)

// schemaVersion is the current database schema version. Future
// migrations bump it and append migration steps (schema.go,
// migrate.go); a database at an older version is migrated forward at
// Open.
const schemaVersion = 4

// busyTimeoutMs is the SQLite busy handler timeout in the DSN. It is
// defense in depth: the primary serialization for writers is the
// SQLITE_BUSY retry (busy.go), because the busy handler does not
// engage on the WAL write path across processes.
const busyTimeoutMs = 30000

// Store is one opened EKA workspace database. It is safe for use by a
// single process; concurrent use is serialized by SQLite itself (WAL +
// busy_timeout) plus the SQLITE_BUSY retry for write operations.
type Store struct {
	db *sql.DB
}

// Open opens (creating when missing) the workspace database at
// dir/eka.db and migrates it to the current schema version. The parent
// directory must already exist. On any failure the database is closed
// and an error is returned.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("store: empty directory")
	}
	path := filepath.Join(dir, "eka.db")
	return openDSN(path, dsn(path, busyTimeoutMs))
}

// openDSN opens the database at path with the given connection string
// and migrates it. Internal seam: tests use it to exercise the
// migration write path with a shorter busy timeout.
func openDSN(path, conn string) (*Store, error) {
	db, err := sql.Open("sqlite", conn)
	if err != nil {
		return nil, fmt.Errorf("store: cannot open %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// dsn builds the connection string of one database file: WAL journal
// mode (concurrent readers + one writer), a busy timeout (defense in
// depth for writer contention), and foreign keys on.
func dsn(path string, busyTimeout int) string {
	return "file:" + url.PathEscape(path) +
		"?_pragma=journal_mode(WAL)" +
		fmt.Sprintf("&_pragma=busy_timeout(%d)", busyTimeout) +
		"&_pragma=foreign_keys(1)"
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying database handle. Internal to the Runtime
// Kernel (ADR-014): the workspace registry uses it for the
// projects/repos tables (workspace-level concerns); consumers outside
// the Kernel must use the runtime services, never the handle. All
// queries against it must be parameterized.
func (s *Store) DB() *sql.DB { return s.db }
