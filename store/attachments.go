package store

import (
	"fmt"
)

// This file implements the attachment store: supporting resources
// carried verbatim, keyed by their Attachment ID (repo-relative path
// with forward slashes) within their provenance pair (project_id,
// source_repo). Attachments are attributed to the repository they were
// pulled from, mirroring objects — a push never leaks another
// repository's attachments into a snapshot. All SQL is parameterized.

// Attachment is one stored attachment: its provenance pair, ID, digest
// and raw bytes.
type Attachment struct {
	ProjectID  string
	SourceRepo string
	ID         string
	Digest     string
	Data       []byte
}

// UpsertAttachment stores one attachment, replacing any existing row
// with the same provenance pair and ID. The write is retried on
// SQLITE_BUSY (concurrent writers across processes).
func (s *Store) UpsertAttachment(projectID, sourceRepo, id, digest string, data []byte) error {
	return retryBusy(func() error {
		if _, err := s.db.Exec(`INSERT INTO attachments (project_id, source_repo, id, digest, data)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(project_id, source_repo, id) DO UPDATE SET digest = excluded.digest, data = excluded.data`,
			projectID, sourceRepo, id, digest, data); err != nil {
			return fmt.Errorf("store: cannot upsert attachment %s: %w", id, err)
		}
		return nil
	})
}

// AttachmentCount returns the number of stored attachments.
func (s *Store) AttachmentCount() (int, error) {
	return s.count("attachments")
}

// Attachments returns every attachment of the repository identified by
// the provenance pair (projectID, sourceRepo), sorted by ID
// (deterministic push order).
func (s *Store) Attachments(projectID, sourceRepo string) ([]Attachment, error) {
	rows, err := s.db.Query(`SELECT project_id, source_repo, id, digest, data FROM attachments
		WHERE project_id = ? AND source_repo = ? ORDER BY id`, projectID, sourceRepo)
	if err != nil {
		return nil, fmt.Errorf("store: cannot query attachments: %w", err)
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ProjectID, &a.SourceRepo, &a.ID, &a.Digest, &a.Data); err != nil {
			return nil, fmt.Errorf("store: cannot scan attachment row: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read attachments: %w", err)
	}
	return out, nil
}
