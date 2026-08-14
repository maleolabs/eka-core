package store

import (
	"fmt"
)

// This file implements the issue-number accessors (RFC: per-group
// incremental numbers, GitHub-style — work items, tickets and notes
// each count independently per project). Numbers are allocated by
// PutUnit when a line first appears in the store; they are stable for
// the life of the line and never re-assigned. The accessors resolve a
// "#<n>" reference back to its line.

// NumberedLine is one store line carrying an issue number.
type NumberedLine struct {
	ProjectID string
	Namespace string
	Type      string
	ID        string
	Number    int
}

// LineForm renders the line identity ("<namespace>/<type>:<id>").
func (l NumberedLine) LineForm() string {
	return l.Namespace + "/" + l.Type + ":" + l.ID
}

// NumberForLine returns the issue number of one line (0 = the line
// carries no number — its type has no group).
func (s *Store) NumberForLine(projectID, ns, typeToken, id string) (int, error) {
	var number int
	err := s.db.QueryRow(
		`SELECT number FROM object_refs WHERE project_id = ? AND namespace = ? AND type = ? AND id = ?`,
		projectID, ns, typeToken, id).Scan(&number)
	if err != nil {
		if isNoRows(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("store: cannot read number of %s/%s:%s: %w", ns, typeToken, id, err)
	}
	return number, nil
}

// LineByNumber resolves an issue number to every matching line of the
// project (one per group — work items, tickets and notes count
// independently, so "#42" can match up to three lines). Sorted by
// (group, form). Empty when no line carries the number.
func (s *Store) LineByNumber(projectID string, number int) ([]NumberedLine, error) {
	rows, err := s.db.Query(
		`SELECT project_id, namespace, type, id, number FROM object_refs
		 WHERE project_id = ? AND number = ?
		 ORDER BY number_group, namespace, type, id`, projectID, number)
	if err != nil {
		return nil, fmt.Errorf("store: cannot resolve number %d: %w", number, err)
	}
	defer rows.Close()
	var out []NumberedLine
	for rows.Next() {
		var l NumberedLine
		if err := rows.Scan(&l.ProjectID, &l.Namespace, &l.Type, &l.ID, &l.Number); err != nil {
			return nil, fmt.Errorf("store: cannot scan number %d: %w", number, err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot resolve number %d: %w", number, err)
	}
	return out, nil
}

// LineByNumberGroup resolves an issue number within ONE group (the
// unambiguous form: `eka view ticket #42`). The second return value is
// false when no line of the group carries the number.
func (s *Store) LineByNumberGroup(projectID, group string, number int) (NumberedLine, bool, error) {
	var l NumberedLine
	err := s.db.QueryRow(
		`SELECT project_id, namespace, type, id, number FROM object_refs
		 WHERE project_id = ? AND number_group = ? AND number = ?`,
		projectID, group, number).Scan(&l.ProjectID, &l.Namespace, &l.Type, &l.ID, &l.Number)
	if err != nil {
		if isNoRows(err) {
			return NumberedLine{}, false, nil
		}
		return NumberedLine{}, false, fmt.Errorf("store: cannot resolve %s #%d: %w", group, number, err)
	}
	return l, true, nil
}

// NumbersByProject returns the line form -> issue number map of the
// project (only numbered lines). The projection renderers attach it to
// the graph so identity displays can show "#<n>".
func (s *Store) NumbersByProject(projectID string) (map[string]int, error) {
	rows, err := s.db.Query(
		`SELECT namespace, type, id, number FROM object_refs
		 WHERE project_id = ? AND number > 0`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: cannot read project numbers: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var ns, typeToken, id string
		var number int
		if err := rows.Scan(&ns, &typeToken, &id, &number); err != nil {
			return nil, fmt.Errorf("store: cannot read project numbers: %w", err)
		}
		out[ns+"/"+typeToken+":"+id] = number
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read project numbers: %w", err)
	}
	return out, nil
}
