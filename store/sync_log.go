package store

import (
	"fmt"
)

// This file implements the sync log: the audit trail of every pull and
// push run per repository, in insertion order (Seq ascending). The log
// backs the idempotent-pull check (LastPullDigest) and the `eka status`
// last-sync report. All SQL is parameterized.

// SyncEntry is one recorded pull or push run.
type SyncEntry struct {
	// Seq is the insertion order (1..n, ascending).
	Seq int
	// ProjectID and Repo identify the synced repository.
	ProjectID string
	Repo      string
	// Direction is "pull" or "push".
	Direction string
	// SnapshotDigest is the package digest involved ("" when none).
	SnapshotDigest string
	// Units is the number of units involved in the run.
	Units int
	// At is the run timestamp (UTC, RFC3339).
	At string
}

// RecordSync appends one sync-log entry. The write is retried on
// SQLITE_BUSY (concurrent writers across processes).
func (s *Store) RecordSync(e SyncEntry) error {
	return retryBusy(func() error {
		if _, err := s.db.Exec(`INSERT INTO sync_log
			(project_id, repo, direction, snapshot_digest, units, at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			e.ProjectID, e.Repo, e.Direction, e.SnapshotDigest, e.Units, e.At); err != nil {
			return fmt.Errorf("store: cannot record sync: %w", err)
		}
		return nil
	})
}

// RecentSyncs returns the n most recent sync entries of one repository,
// ordered by seq descending (newest first).
func (s *Store) RecentSyncs(projectID, repo string, n int) ([]SyncEntry, error) {
	rows, err := s.db.Query(`SELECT seq, project_id, repo, direction, snapshot_digest, units, at
		FROM sync_log WHERE project_id = ? AND repo = ? ORDER BY seq DESC LIMIT ?`,
		projectID, repo, n)
	if err != nil {
		return nil, fmt.Errorf("store: cannot query sync log: %w", err)
	}
	defer rows.Close()
	var out []SyncEntry
	for rows.Next() {
		var e SyncEntry
		if err := rows.Scan(&e.Seq, &e.ProjectID, &e.Repo, &e.Direction, &e.SnapshotDigest, &e.Units, &e.At); err != nil {
			return nil, fmt.Errorf("store: cannot scan sync entry: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read sync log: %w", err)
	}
	return out, nil
}

// LastPullDigest returns the snapshot digest of the most recent pull
// entry of one repository (the idempotent-pull check: an unchanged
// snapshot digest skips the pull work).
func (s *Store) LastPullDigest(projectID, repo string) (string, bool, error) {
	var digest string
	err := s.db.QueryRow(`SELECT snapshot_digest FROM sync_log
		WHERE project_id = ? AND repo = ? AND direction = 'pull'
		ORDER BY seq DESC LIMIT 1`, projectID, repo).Scan(&digest)
	if err != nil {
		if isNoRows(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("store: cannot read last pull digest: %w", err)
	}
	return digest, true, nil
}
