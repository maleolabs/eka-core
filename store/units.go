package store

import (
	"fmt"

	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the unit projection of the canonical store: the
// read side that reconstructs the Engineering Knowledge Objects of one
// repository (provenance pair) or of one whole project from the
// immutable payload archive. A unit is the decoded form of one
// reference's payload (exchange.DecodeUnit), attributed to its
// provenance pair and carrying the content-derived digest of its
// immutable bytes. The result is sorted by canonical form — the
// deterministic order the push and projection paths share.
//
// The payload archive is the single source of truth: a reference whose
// payload is missing is store corruption and errors loudly, never a
// silent skip. An empty result is an empty slice, nil error. All SQL
// is parameterized.

// Units returns the canonical units attributed to one repository
// (provenance pair), decoded from their immutable payloads, sorted by
// canonical form — the deterministic order the push and projection
// paths share.
func (s *Store) Units(projectID, sourceRepo string) ([]*exchange.Unit, error) {
	return s.unitsByQuery(`SELECT form, object_hash FROM object_refs
		WHERE project_id = ? AND source_repo = ? ORDER BY form`, projectID, sourceRepo)
}

// UnitsByProject returns the union of every repository's units of one
// project, decoded from their immutable payloads, sorted by canonical
// form. This is the complete Engineering Knowledge of a project — the
// projection source of the runtime.
func (s *Store) UnitsByProject(projectID string) ([]*exchange.Unit, error) {
	return s.unitsByQuery(`SELECT form, object_hash FROM object_refs
		WHERE project_id = ? ORDER BY form`, projectID)
}

// UnitsByLine returns every instance of one artifact line across the
// WHOLE workspace — the identity line (namespace, type, id) resolved
// across every project and repository — decoded from their immutable
// payloads, ordered by canonical form (the deterministic workspace
// order). It is the line resolution primitive of the runtime
// (Resolver.ResolveLine, Timeline.Line).
func (s *Store) UnitsByLine(ns, typeToken, id string) ([]*exchange.Unit, error) {
	return s.unitsByQuery(`SELECT form, object_hash FROM object_refs
		WHERE namespace = ? AND type = ? AND id = ? ORDER BY form`, ns, typeToken, id)
}

// MaxInstanceVersion returns the highest instance-version of one
// artifact line (namespace, type, id) across the WHOLE workspace, or 0
// when the line has no instances. It is the auto-assignment primitive of
// the publish pipeline: the next version of a line is
// MaxInstanceVersion + 1 (forward-only, P7).
func (s *Store) MaxInstanceVersion(ns, typeToken, id string) (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(instance_version), 0) FROM object_refs
		WHERE namespace = ? AND type = ? AND id = ?`, ns, typeToken, id).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("store: cannot read the highest instance version of %s/%s:%s: %w", ns, typeToken, id, err)
	}
	return v, nil
}

// Unit returns one Canonical Knowledge Object by its canonical identity
// form — the single-object resolution of the runtime ("Load/Resolve
// Knowledge"). It mirrors the form/payload-identity check of the unit
// projection: a reference whose form does not equal the payload's own
// identity is store corruption and errors loudly.
func (s *Store) Unit(form string) (*exchange.Unit, bool, error) {
	r, ok, err := s.Ref(form)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	u, err := s.decodeUnit(r.Form, r.ObjectHash)
	if err != nil {
		return nil, false, err
	}
	return u, true, nil
}

// unitsByQuery resolves the (form, object_hash) rows of one reference
// query to their decoded units, in row order (the query carries its
// ORDER BY form). A missing payload or an undecodable unit for a
// referenced form is an error: the store is corrupt, never silently
// skipped.
func (s *Store) unitsByQuery(query string, args ...any) ([]*exchange.Unit, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: cannot query unit references: %w", err)
	}
	defer rows.Close()
	var forms []string
	var hashes []string
	for rows.Next() {
		var form, hash string
		if err := rows.Scan(&form, &hash); err != nil {
			return nil, fmt.Errorf("store: cannot scan reference row: %w", err)
		}
		forms = append(forms, form)
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read references: %w", err)
	}
	out := make([]*exchange.Unit, 0, len(forms))
	for i, hash := range hashes {
		u, err := s.decodeUnit(forms[i], hash)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// decodeUnit decodes one (form, object_hash) reference pair to its
// unit: the shared per-reference decode of the unit projection and the
// single-object resolver.
//
// Reference/form integrity (mirror of VerifyIntegrity level 4): the
// reference's form must equal the payload's own identity — a mismatch
// is store corruption and errors loudly, never silently projected
// under the payload's identity.
func (s *Store) decodeUnit(form, hash string) (*exchange.Unit, error) {
	unitJSON, content, err := s.Payload(hash)
	if err != nil {
		return nil, fmt.Errorf("store: cannot read payload of %s: %w", form, err)
	}
	u, err := exchange.DecodeUnit(unitJSON, content)
	if err != nil {
		return nil, fmt.Errorf("store: cannot decode payload of %s: %w", form, err)
	}
	if want := u.Identity.CanonicalForm(); form != want {
		return nil, fmt.Errorf("store: reference %s points at a payload whose identity is %s (store corruption)", form, want)
	}
	u.Digest = hash
	return u, nil
}
