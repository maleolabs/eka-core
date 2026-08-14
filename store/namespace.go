package store

import (
	"database/sql"
	"fmt"
)

// This file implements the repository namespace helper: the `namespace`
// column of the repos registry table (schema v3).
//
// repos.namespace records a repository's DEFAULT namespace — the
// platform-scoped identity prefix the authoring commands resolve for
// unqualified targets inside a registered repository (spec
// reference/spec-authoring-publish.md §3.2). The registry owns the
// column's creation; the sync engine populates it (push resolution) and
// the authoring commands read it through the workspace registry.
// All SQL is parameterized.

// SetRepoNamespace records the repository's default namespace. The
// repository must already be registered (the repos row exists); the
// update is a plain UPDATE — a missing row is reported, never silently
// created. The write is retried on SQLITE_BUSY (concurrent writers
// across processes).
func (s *Store) SetRepoNamespace(projectID, name, namespace string) error {
	var res sql.Result
	err := retryBusy(func() error {
		var err error
		res, err = s.db.Exec(`UPDATE repos SET namespace = ? WHERE project_id = ? AND name = ?`,
			namespace, projectID, name)
		if err != nil {
			return fmt.Errorf("store: cannot set namespace of %s/%s: %w", projectID, name, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: cannot confirm namespace update of %s/%s: %w", projectID, name, err)
	} else if n == 0 {
		return fmt.Errorf("store: repository %s/%s is not registered; cannot set its namespace", projectID, name)
	}
	return nil
}
