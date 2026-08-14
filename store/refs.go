package store

import (
	"fmt"
)

// This file implements the reference store: the mutable resolver table
// of the Immutable Engineering Knowledge Model. A reference maps an
// RSF canonical identity form ("<namespace>/<type>:<id>:<instance-version>",
// the resolver key) to the immutable payload row it currently points
// at, within its provenance pair (project_id, source_repo). The index
// columns (namespace, type, id, instance_version, revision, dimension,
// domain, phase) are derived from the payload at insert — they are
// indexes over immutable data, never independent storage.
//
// References are the only mutable state of the object model: every
// change produces a new immutable payload and moves the reference to
// it. All SQL is parameterized.

// Ref is the current reference of one identity: the resolver key plus
// the pointer into the immutable payload archive.
type Ref struct {
	// Form is the RSF canonical identity form — the resolver key.
	Form string
	// ObjectHash is the immutable payload the form currently points
	// at (SHA-256(unit.json || content)).
	ObjectHash string
	// ProjectID and SourceRepo are the provenance pair: the project
	// and repository this reference was pulled from.
	ProjectID  string
	SourceRepo string
	// Namespace, Type, ID and InstanceVersion are the identity tuple
	// (derived index columns).
	Namespace       string
	Type            string
	ID              string
	InstanceVersion int
	// Revision is the unit revision (derived index column).
	Revision int
	// Dimension, Domain and Phase are the classification/context
	// index columns ("" when the payload carries none).
	Dimension string
	Domain    string
	Phase     string
	// UpdatedAt is the reference bookkeeping timestamp (RFC3339 UTC),
	// never inside payload bytes.
	UpdatedAt string
}

// Ref returns the current reference of one form; nil, false when the
// form has no reference.
func (s *Store) Ref(form string) (*Ref, bool, error) {
	var r Ref
	err := s.db.QueryRow(`SELECT
		form, object_hash, project_id, source_repo, namespace, type, id,
		instance_version, revision, dimension, domain, phase, updated_at
		FROM object_refs WHERE form = ?`, form).Scan(
		&r.Form, &r.ObjectHash, &r.ProjectID, &r.SourceRepo, &r.Namespace, &r.Type, &r.ID,
		&r.InstanceVersion, &r.Revision, &r.Dimension, &r.Domain, &r.Phase, &r.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("store: cannot read reference %s: %w", form, err)
	}
	return &r, true, nil
}

// Refs returns every reference pulled from the repository identified by
// the provenance pair (projectID, sourceRepo), sorted by form
// (canonical identity order — the deterministic push order).
func (s *Store) Refs(projectID, sourceRepo string) ([]*Ref, error) {
	rows, err := s.db.Query(`SELECT
		form, object_hash, project_id, source_repo, namespace, type, id,
		instance_version, revision, dimension, domain, phase, updated_at
		FROM object_refs WHERE project_id = ? AND source_repo = ? ORDER BY form`,
		projectID, sourceRepo)
	if err != nil {
		return nil, fmt.Errorf("store: cannot query references: %w", err)
	}
	defer rows.Close()
	var out []*Ref
	for rows.Next() {
		var r Ref
		if err := rows.Scan(
			&r.Form, &r.ObjectHash, &r.ProjectID, &r.SourceRepo, &r.Namespace, &r.Type, &r.ID,
			&r.InstanceVersion, &r.Revision, &r.Dimension, &r.Domain, &r.Phase, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: cannot scan reference row: %w", err)
		}
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read references: %w", err)
	}
	return out, nil
}

// RefCount returns the number of stored references.
func (s *Store) RefCount() (int, error) {
	return s.count("object_refs")
}

// count returns the row count of one table.
func (s *Store) count(table string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: cannot count %s: %w", table, err)
	}
	return n, nil
}
