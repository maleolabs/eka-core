package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the integrity verification of the canonical
// store: a read-only scan (no writes) that recomputes every
// content-derived value and compares it with the stored state, so
// manual database modification is DETECTED (it is never prevented —
// SQLite is a persistence layer, not a trust boundary).
//
// Verification levels:
//
//  1. payload-hash: every payload row's SHA-256(unit.json || content)
//     must equal its object_hash (the content-addressed key).
//  2. payload-decode: every unit_json must strict-decode to an
//     exchange.Unit (reject-by-default, RSF §9.5).
//  3. reference-target: every object_refs.object_hash must exist in
//     object_payloads.
//  4. reference-index: every reference's derived index columns must
//     match the payload they point at (identity tuple, revision,
//     dimension, domain, phase), and the form must equal the payload's
//     canonical identity form.
//  5. attachment-hash: every attachment's SHA-256(data) must equal its
//     digest.
//  6. registry: every repos.project_id must exist in projects.
//
// Payload rows no reference points at are NOT violations: they are the
// immutable history archive (retained lineage of superseded objects)
// and are only counted (OrphanPayloads).
//
// All SQL is parameterized; violations are sorted by (Kind, Subject)
// for deterministic output.

// IntegrityReport is the outcome of one VerifyIntegrity run.
type IntegrityReport struct {
	// PayloadsChecked/RefsChecked/AttachmentsChecked are the scanned
	// row counts.
	PayloadsChecked    int
	RefsChecked        int
	AttachmentsChecked int
	// OrphanPayloads counts unreferenced payloads — retained history
	// archive, NOT violations.
	OrphanPayloads int
	// Violations lists every detected integrity violation, sorted by
	// (Kind, Subject).
	Violations []IntegrityViolation
}

// IntegrityViolation is one detected integrity problem.
type IntegrityViolation struct {
	// Kind is one of "payload-hash", "payload-decode",
	// "reference-target", "reference-index", "attachment-hash",
	// "registry".
	Kind string
	// Subject identifies the affected row (hash, form or id).
	Subject string
	// Detail explains the mismatch deterministically.
	Detail string
}

// VerifyIntegrity scans the whole store and reports every integrity
// violation. The scan is read-only. Returns a report; the error is
// only for store read failures (the report itself carries the
// findings).
func (s *Store) VerifyIntegrity() (*IntegrityReport, error) {
	report := &IntegrityReport{}
	var violations []IntegrityViolation

	// Level 1 + 2: payload rows — hash recomputation and strict
	// decode.
	payloads, err := s.AllPayloads()
	if err != nil {
		return nil, err
	}
	report.PayloadsChecked = len(payloads)
	payloadExists := map[string]bool{}
	for _, p := range payloads {
		payloadExists[p.ObjectHash] = true
		got := hashUnit(p.UnitJSON, p.Content)
		if got != p.ObjectHash {
			violations = append(violations, IntegrityViolation{
				Kind:    "payload-hash",
				Subject: p.ObjectHash,
				Detail:  fmt.Sprintf("recomputed SHA-256(unit.json || content) is %s", got),
			})
			// A hash mismatch means the payload is untrustworthy; its
			// decode is still attempted (a decode failure is reported
			// separately when the bytes are also invalid).
		}
		if _, err := exchange.DecodeUnit(p.UnitJSON, p.Content); err != nil {
			violations = append(violations, IntegrityViolation{
				Kind:    "payload-decode",
				Subject: p.ObjectHash,
				Detail:  err.Error(),
			})
		}
	}

	// Level 3 + 4: reference rows — target existence and index-column
	// derivation.
	refs, err := s.allRefs()
	if err != nil {
		return nil, err
	}
	report.RefsChecked = len(refs)
	for _, r := range refs {
		if !payloadExists[r.ObjectHash] {
			violations = append(violations, IntegrityViolation{
				Kind:    "reference-target",
				Subject: r.Form,
				Detail:  fmt.Sprintf("object_hash %s does not exist in object_payloads", r.ObjectHash),
			})
			continue
		}
		unitJSON, content, err := s.Payload(r.ObjectHash)
		if err != nil {
			return nil, err
		}
		u, err := exchange.DecodeUnit(unitJSON, content)
		if err != nil {
			// Already reported as payload-decode; the index columns
			// cannot be derived from an undecodable payload.
			continue
		}
		detail := deriveRefMismatch(r, u)
		if detail != "" {
			violations = append(violations, IntegrityViolation{
				Kind:    "reference-index",
				Subject: r.Form,
				Detail:  detail,
			})
		}
	}

	// Orphans: payloads no reference points at (retained history).
	for hash := range payloadExists {
		referenced := false
		for _, r := range refs {
			if r.ObjectHash == hash {
				referenced = true
				break
			}
		}
		if !referenced {
			report.OrphanPayloads++
		}
	}

	// Level 5: attachment rows — digest recomputation.
	rows, err := s.db.Query(`SELECT id, digest, data FROM attachments`)
	if err != nil {
		return nil, fmt.Errorf("store: cannot query attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, digest string
		var data []byte
		if err := rows.Scan(&id, &digest, &data); err != nil {
			return nil, fmt.Errorf("store: cannot scan attachment row: %w", err)
		}
		report.AttachmentsChecked++
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if got != digest {
			violations = append(violations, IntegrityViolation{
				Kind:    "attachment-hash",
				Subject: id,
				Detail:  fmt.Sprintf("recomputed SHA-256(data) is %s", got),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read attachments: %w", err)
	}

	// Level 6: registry — every repo's project must exist.
	repoRows, err := s.db.Query(`SELECT project_id, name FROM repos`)
	if err != nil {
		return nil, fmt.Errorf("store: cannot query repos: %w", err)
	}
	defer repoRows.Close()
	for repoRows.Next() {
		var projectID, name string
		if err := repoRows.Scan(&projectID, &name); err != nil {
			return nil, fmt.Errorf("store: cannot scan repo row: %w", err)
		}
		var n int
		err := s.db.QueryRow(`SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&n)
		if err != nil {
			return nil, fmt.Errorf("store: cannot check project %s: %w", projectID, err)
		}
		if n == 0 {
			violations = append(violations, IntegrityViolation{
				Kind:    "registry",
				Subject: projectID + "/" + name,
				Detail:  fmt.Sprintf("repository %s references project %s, which does not exist in projects", name, projectID),
			})
		}
	}
	if err := repoRows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read repos: %w", err)
	}

	// Deterministic ordering: by (Kind, Subject).
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Kind != violations[j].Kind {
			return violations[i].Kind < violations[j].Kind
		}
		return violations[i].Subject < violations[j].Subject
	})
	report.Violations = violations
	return report, nil
}

// allRefs returns every reference row (unsorted; ordering is applied
// by the caller's sort).
func (s *Store) allRefs() ([]*Ref, error) {
	rows, err := s.db.Query(`SELECT
		form, object_hash, project_id, source_repo, namespace, type, id,
		instance_version, revision, dimension, domain, phase, updated_at
		FROM object_refs`)
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

// deriveRefMismatch compares one reference's derived index columns
// with the payload they point at and returns a deterministic
// description of the first mismatching field ("" when everything
// agrees).
func deriveRefMismatch(r *Ref, u *exchange.Unit) string {
	if r.Namespace != u.Identity.Namespace {
		return fmt.Sprintf("namespace is %q, payload identity carries %q", r.Namespace, u.Identity.Namespace)
	}
	if r.Type != u.Identity.Type {
		return fmt.Sprintf("type is %q, payload identity carries %q", r.Type, u.Identity.Type)
	}
	if r.ID != u.Identity.ID {
		return fmt.Sprintf("id is %q, payload identity carries %q", r.ID, u.Identity.ID)
	}
	if r.InstanceVersion != u.Identity.InstanceVersion {
		return fmt.Sprintf("instance_version is %d, payload identity carries %d", r.InstanceVersion, u.Identity.InstanceVersion)
	}
	if r.Revision != u.Revision {
		return fmt.Sprintf("revision is %d, payload carries %d", r.Revision, u.Revision)
	}
	if r.Dimension != u.Classification.Dimension {
		return fmt.Sprintf("dimension is %q, payload carries %q", r.Dimension, u.Classification.Dimension)
	}
	if r.Domain != u.Classification.Domain {
		return fmt.Sprintf("domain is %q, payload carries %q", r.Domain, u.Classification.Domain)
	}
	if r.Phase != u.Phase {
		return fmt.Sprintf("phase is %q, payload carries %q", r.Phase, u.Phase)
	}
	if r.Form != u.Identity.CanonicalForm() {
		return fmt.Sprintf("form is %q, payload identity canonical form is %s", r.Form, u.Identity.CanonicalForm())
	}
	return ""
}
