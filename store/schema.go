package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// This file implements the schema migration driver: the meta table
// (schema bookkeeping) plus the schema version steps of the EKA
// workspace database.
//
// Migration contract: Open runs the current schemaVersion's steps
// against a database in a transaction; every step is idempotent in
// practice so re-opening an up-to-date database is a no-op. Future
// schema versions append steps and bump schemaVersion; a database
// newer than this implementation is refused (downgrade protection).
//
// Schema v2 (the Immutable Engineering Knowledge Model): Engineering
// Knowledge Objects are immutable and content-addressed. The store
// keeps two tables:
//
//   - object_payloads: the immutable payload archive, keyed by the
//     content-derived hash SHA-256(unit.json || content) (== the RSF
//     per-unit digest). Payload rows are written once and never
//     updated; history is the accumulation of payload rows, chained
//     per reference through prev_hash. SQLite is only the persistence
//     layer — the source of immutability is the content hash, never a
//     database-generated value.
//   - object_refs: the mutable reference table (resolver key = RSF
//     canonical identity form), with derived index columns filled from
//     the payload at insert.
//
// Relationships and change logs are serialized inside unit.json (the
// payload); the v1 objects/relationships/change_log tables are gone.
// The remaining v1 tables (projects, repos, attachments, sync_log,
// meta) are unchanged. The v1 -> v2 migration (migrate.go)
// reconstructs each v1 object's unit payload, recomputes the content
// hash, and drops the v1 tables.
//
// Schema v3 (platform-scoped namespaces): repos gains the `namespace`
// column — the repository's default namespace (populated at push time
// by the sync engine; the resolution rule the authoring commands use
// inside a registered repository). The column is NOT NULL DEFAULT ''
// so the v2 -> v3 migration is a single additive ALTER with no data
// rewrite.

// migrate opens the schema and checks/advances the recorded version.
//
// Read-only fast path: a database already at the current schema
// version is NOT written at open — no CREATE, no INSERT. This is the
// concurrency fix for read-only commands (view, get, context, status):
// in WAL mode readers never contend with writers, so opening a
// current-version database can never fail with SQLITE_BUSY while
// another eka process holds the write lock. Only when a migration is
// actually needed (fresh database, missing meta table, or a recorded
// version below the current one) does migrate run the transactional
// write below — wrapped in the SQLITE_BUSY retry (busy.go) because a
// fresh/old database must still be initialized even when another
// process is mid-write.
func (s *Store) migrate() error {
	if s.atCurrentSchema() {
		return nil
	}
	return retryBusy(func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: cannot begin migration: %w", err)
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`); err != nil {
			return fmt.Errorf("store: cannot create meta table: %w", err)
		}

		var current int
		err = tx.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&current)
		switch {
		case err == nil:
			if current > schemaVersion {
				return fmt.Errorf("store: database schema version %d is newer than this implementation (%d); upgrade the CLI", current, schemaVersion)
			}
		case isNoRows(err):
			current = 0
		default:
			return fmt.Errorf("store: cannot read schema_version: %w", err)
		}

		// Migration steps run in order; each step advances `current`. A
		// fresh database (current == 0, no v1 tables) goes straight to v2.
		// A database at schema v1 (or a partially migrated v1 database
		// whose tables exist but whose version was never recorded) runs the
		// v1 -> v2 conversion. Every database at v2 or below then advances
		// to the current schema version.
		switch {
		case current == 0 && !s.hasTable(tx, "objects"):
			if err := migrateToV2(tx); err != nil {
				return err
			}
			current = 2
		case current <= 1:
			if err := migrateV1toV2(tx); err != nil {
				return err
			}
			current = 2
		}
		if current < 3 {
			if err := migrateV2toV3(tx); err != nil {
				return err
			}
			current = 3
		}
		if current < schemaVersion {
			if err := migrateV3toV4(tx); err != nil {
				return err
			}
			current = schemaVersion
		}

		if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(current)); err != nil {
			return fmt.Errorf("store: cannot record schema_version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: cannot commit migration: %w", err)
		}
		return nil
	})
}

// atCurrentSchema reports whether the database is already at the
// current schema version, READ-ONLY (no transaction, no write). A
// missing meta table, a missing version row, or an unreadable version
// reports false and the caller runs the full migration path, which
// makes the same determinations transactionally.
func (s *Store) atCurrentSchema() bool {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&n)
	if err != nil || n == 0 {
		return false
	}
	var current int
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&current); err != nil {
		return false
	}
	return current == schemaVersion
}

// hasTable reports whether a table exists in the database (schema
// detection for the fresh-vs-v1 decision).
func (s *Store) hasTable(tx *sql.Tx, name string) bool {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return err == nil && n > 0
}

// migrateToV2 creates the v2 schema on a fresh database: the shared
// registry tables (unchanged from v1) plus the immutable payload and
// reference tables. Steps run unconditionally (IF NOT EXISTS) so a
// partial database completes.
func migrateToV2(tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id      TEXT PRIMARY KEY,
			name    TEXT NOT NULL,
			created TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS repos (
			project_id TEXT NOT NULL REFERENCES projects(id),
			name       TEXT NOT NULL,
			path       TEXT NOT NULL,
			created    TEXT NOT NULL,
			namespace  TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (project_id, name)
		)`,
		// A repository path is registered in exactly one project: the
		// first project to register the path owns it (registry
		// determinism; see workspace.RegisterRepo).
		`CREATE UNIQUE INDEX IF NOT EXISTS repos_path_uniq ON repos(path)`,
		// The immutable payload archive: one row per content-addressed
		// Engineering Knowledge Object. object_hash is SHA-256(unit.json
		// || content) — the RSF per-unit digest — and is never
		// database-generated. Payload rows are INSERT-only (no update
		// path exists in this package; the store API has no payload
		// mutation beyond first insert).
		`CREATE TABLE IF NOT EXISTS object_payloads (
			object_hash TEXT PRIMARY KEY,
			unit_json   BLOB NOT NULL,
			content     BLOB NOT NULL,
			prev_hash   TEXT NOT NULL,
			created_at  TEXT NOT NULL
		)`,
		// The mutable reference table: the resolver key is the RSF
		// canonical identity form; the row points at the current
		// immutable payload of that identity within its provenance
		// (project_id, source_repo). The index columns are derived from
		// the payload at insert (never stored independently in v1
		// columns). The FK keeps every reference pointing at a real
		// payload (foreign_keys pragma is on).
		`CREATE TABLE IF NOT EXISTS object_refs (
			form             TEXT PRIMARY KEY,
			object_hash      TEXT NOT NULL REFERENCES object_payloads(object_hash),
			project_id       TEXT NOT NULL,
			source_repo      TEXT NOT NULL,
			namespace        TEXT NOT NULL,
			type             TEXT NOT NULL,
			id               TEXT NOT NULL,
			instance_version INTEGER NOT NULL,
			revision         INTEGER NOT NULL,
			dimension        TEXT NOT NULL,
			domain           TEXT NOT NULL,
			phase            TEXT NOT NULL,
			updated_at       TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_refs_identity ON object_refs (namespace, type, id)`,
		`CREATE INDEX IF NOT EXISTS idx_refs_dimension ON object_refs (dimension)`,
		`CREATE INDEX IF NOT EXISTS idx_refs_domain ON object_refs (domain)`,
		`CREATE INDEX IF NOT EXISTS idx_refs_source ON object_refs (project_id, source_repo)`,
		// Reverse hash lookup (replication, future GC of the history
		// archive): which references point at a payload.
		`CREATE INDEX IF NOT EXISTS idx_refs_hash ON object_refs (object_hash)`,
		`CREATE TABLE IF NOT EXISTS attachments (
			project_id  TEXT NOT NULL,
			source_repo TEXT NOT NULL,
			id          TEXT NOT NULL,
			digest      TEXT NOT NULL,
			data        BLOB NOT NULL,
			PRIMARY KEY (project_id, source_repo, id)
		)`,
		`CREATE TABLE IF NOT EXISTS sync_log (
			seq             INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id      TEXT NOT NULL,
			repo            TEXT NOT NULL,
			direction       TEXT NOT NULL,
			snapshot_digest TEXT NOT NULL,
			units           INTEGER NOT NULL,
			at              TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS sync_log_repo_idx ON sync_log(project_id, repo, seq)`,
	}
	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: cannot create table: %w", err)
		}
	}
	return nil
}

// isNoRows reports whether err is sql.ErrNoRows (driver-agnostic).
func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// migrateV2toV3 advances a v2 database to v3: repos gains the
// `namespace` column (NOT NULL DEFAULT ” — the repository's default
// namespace, populated by the sync push). The step is idempotent in
// practice like every migration step: the column is added only when the
// pragma table_info scan reports it missing, so re-running over a v3
// database (or over a fresh database that already created the v3 shape)
// is a no-op.
func migrateV2toV3(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_info(repos)`)
	if err != nil {
		return fmt.Errorf("store: cannot inspect repos table: %w", err)
	}
	defer rows.Close()
	hasNamespace := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("store: cannot read repos column info: %w", err)
		}
		if name == "namespace" {
			hasNamespace = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: cannot read repos column info: %w", err)
	}
	if hasNamespace {
		return nil // Already v3.
	}
	if _, err := tx.Exec(`ALTER TABLE repos ADD COLUMN namespace TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("store: cannot add repos.namespace: %w", err)
	}
	return nil
}

// migrateV3toV4 adds the issue-number columns to object_refs (RFC:
// per-group incremental numbers — work items, tickets, notes each
// count independently per project). Additive ALTERs and one partial
// unique index; existing rows get number 0 (no number) and are
// numbered on their next insert.
func migrateV3toV4(tx *sql.Tx) error {
	for _, stmt := range []string{
		`ALTER TABLE object_refs ADD COLUMN number INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE object_refs ADD COLUMN number_group TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_refs_number ON object_refs (project_id, number_group, number) WHERE number > 0`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: cannot migrate to schema v4: %w", err)
		}
	}
	return nil
}
