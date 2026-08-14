package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/maleolabs/eka-core/conformance"
)

// This file implements the immutable payload store: the content-
// addressed archive of Engineering Knowledge Objects. Every payload
// row is keyed by its content-derived hash — SHA-256(unit.json ||
// content), the same per-unit digest as the RSF — and is written at
// most once: there is deliberately NO update path for payload rows in
// this package. History is the accumulation of payload rows, chained
// per reference through prev_hash (first-writer wins; a payload's
// lineage is fixed the moment it is inserted). SQLite is only the
// persistence layer; the source of immutability is the content hash,
// never a database-generated value.
//
// The reference (object_refs) is the only mutable state, and its
// moves are guarded forward-only: a put whose payload is already an
// ancestor of the referenced payload in the line's prev_hash lineage
// (an OLDER instance of the same line) archives the payload but never
// re-points the reference (see putUnitTx). A stale re-seed can
// therefore never silently regress a newer knowledge state.
//
// All SQL is parameterized.

// PutUnit stores one immutable unit payload and points its reference
// at it, in one transaction. See putUnitTx for the per-put semantics.
// The second return value reports whether the reference was KEPT at a
// newer instance (forward-only reference guard: the put archived the
// payload but did not re-point the reference, because the reference
// already resolves a newer instance version). The whole transaction is
// retried on SQLITE_BUSY (concurrent writers across processes): a busy
// failure rolls the transaction back and the retry starts a fresh one.
func (s *Store) PutUnit(unitJSON, content []byte, r Ref) (string, bool, error) {
	var hash string
	var keptNewer bool
	err := retryBusy(func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: cannot begin put: %w", err)
		}
		defer tx.Rollback()

		hash, keptNewer, err = putUnitTx(tx, unitJSON, content, r)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: cannot commit put of %s: %w", hash, err)
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return hash, keptNewer, nil
}

// Put is one unit payload to persist in a batch.
type Put struct {
	UnitJSON []byte
	Content  []byte
	Ref      Ref
}

// PutUnits stores several immutable unit payloads and points their
// references at them IN ONE TRANSACTION (the atomicity primitive of
// the container birth: the container unit + its locked plan land
// together or not at all). Each put follows the PutUnit semantics
// (hash = SHA-256(unitJSON||content), payload insert-once with
// prev_hash lineage from the reference's current payload, reference
// upsert subject to the forward-only reference guard — a put that
// would re-point a reference to an older instance version archives the
// payload but keeps the reference). Any error rolls the whole batch
// back — nothing is stored. Returns the stored hashes in input order.
// The whole transaction is retried on SQLITE_BUSY (concurrent writers
// across processes).
func (s *Store) PutUnits(puts []Put) ([]string, error) {
	var hashes []string
	err := retryBusy(func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: cannot begin batch put: %w", err)
		}
		defer tx.Rollback()

		hashes = make([]string, 0, len(puts))
		for _, p := range puts {
			hash, _, err := putUnitTx(tx, p.UnitJSON, p.Content, p.Ref)
			if err != nil {
				return err
			}
			hashes = append(hashes, hash)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: cannot commit batch put: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return hashes, nil
}

// putUnitTx performs one put inside an open transaction (the shared
// per-put body of PutUnit and PutUnits):
//
//  1. hash = SHA-256(unitJSON || content).
//  2. The payload row is inserted when absent (INSERT ... DO NOTHING):
//     a payload with this hash already in the archive is left
//     untouched (first-writer wins — prev_hash is never modified). For
//     a NEW payload, prev_hash is the object_hash of the reference's
//     current payload ("" when the form has no reference yet): the
//     lineage edge "this object supersedes <prev> within its
//     reference".
//  3. The object_refs row is upserted (the reference is the mutable
//     part — it may move from one immutable payload to another),
//     SUBJECT TO the forward-only reference guard: a put whose payload
//     already sits in the referenced line's history (it is an ancestor
//     of the current payload through prev_hash — an OLDER instance of
//     the same line) archives the payload but does NOT re-point the
//     reference (keptNewer = true). A stale re-seed (e.g. a sync pull
//     from an old snapshot, whose payload predates a transition or
//     publish on the same line) can therefore never silently regress a
//     newer state — transitions and publishes append NEW payloads that
//     are not ancestors, so they pass, and so do genuinely new
//     contents (cross-repository last-wins). An identical payload
//     (same hash as the referenced one) is an idempotent no-op.
//
// putUnitTx returns the stored hash and the keptNewer flag. It never
// updates an existing payload row.
func putUnitTx(tx *sql.Tx, unitJSON, content []byte, r Ref) (string, bool, error) {
	hash := hashUnit(unitJSON, content)

	// Look up the reference's current payload hash BEFORE the insert:
	// it becomes the new payload's lineage predecessor, and it anchors
	// the forward-only guard's ancestry walk.
	prevHash := ""
	var current string
	err := tx.QueryRow(`SELECT object_hash FROM object_refs WHERE form = ?`, r.Form).Scan(&current)
	switch {
	case err == nil:
		prevHash = current
	case isNoRows(err):
		// No reference yet: a root payload ("" lineage).
	default:
		return "", false, fmt.Errorf("store: cannot read reference %s: %w", r.Form, err)
	}

	// Immutable insert: a payload with this hash already exists -> no-op
	// (first-writer wins; prev_hash and bytes stay untouched).
	if _, err := tx.Exec(`INSERT INTO object_payloads (object_hash, unit_json, content, prev_hash, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(object_hash) DO NOTHING`,
		hash, unitJSON, content, prevHash, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return "", false, fmt.Errorf("store: cannot insert payload %s: %w", hash, err)
	}

	// Forward-only reference guard: the reference must never move
	// backward along its own lineage. The payload above was archived
	// (older instances are history); the reference stays where it is
	// when the put is an idempotent re-put (same hash) or a re-seed of
	// an ancestor payload (an older instance of the line).
	if prevHash != "" {
		if hash == current {
			// Identical payload: the reference already points at it —
			// an idempotent re-put; nothing moves.
			return hash, false, nil
		}
		if isAncestor(tx, hash, current) {
			// The payload is already in the referenced line's history:
			// re-pointing would regress the line to an older instance.
			return hash, true, nil
		}
	}

	// The reference is the only mutable part: upsert all index columns
	// from the caller's ref (derived from the payload at insert). A
	// NEW reference line of a numbered group receives its issue number
	// here — the line's first store appearance allocates the next
	// number of its (project, group) counter; later payload updates
	// keep the allocated number.
	newLine := prevHash == ""
	number := 0
	group := conformance.NumberGroup(r.Type)
	if newLine && group != "" {
		if err := tx.QueryRow(`SELECT COALESCE(MAX(number), 0) + 1 FROM object_refs
			WHERE project_id = ? AND number_group = ?`, r.ProjectID, group).Scan(&number); err != nil {
			return "", false, fmt.Errorf("store: cannot allocate issue number for %s: %w", r.Form, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO object_refs (
		form, object_hash, project_id, source_repo, namespace, type, id,
		instance_version, revision, dimension, domain, phase, updated_at,
		number, number_group
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(form) DO UPDATE SET
		object_hash = excluded.object_hash,
		project_id = excluded.project_id,
		source_repo = excluded.source_repo,
		namespace = excluded.namespace,
		type = excluded.type,
		id = excluded.id,
		instance_version = excluded.instance_version,
		revision = excluded.revision,
		dimension = excluded.dimension,
		domain = excluded.domain,
		phase = excluded.phase,
		updated_at = excluded.updated_at`,
		r.Form, hash, r.ProjectID, r.SourceRepo, r.Namespace, r.Type, r.ID,
		r.InstanceVersion, r.Revision, r.Dimension, r.Domain, r.Phase, r.UpdatedAt,
		number, group); err != nil {
		return "", false, fmt.Errorf("store: cannot upsert reference %s: %w", r.Form, err)
	}
	return hash, false, nil
}

// isAncestor reports whether target appears in the payload lineage
// chain of current (walking prev_hash backward). The chain is
// per-reference: every payload ever referenced by a form is linked
// through prev_hash (first-writer wins), so an ancestor payload is an
// OLDER instance of the same line. visited guards against a corrupt
// cycle; a failed lookup is permissive (the move is allowed — the
// integrity check flags missing payload rows separately).
func isAncestor(tx *sql.Tx, target, current string) bool {
	visited := map[string]bool{}
	next := current
	for next != "" && !visited[next] {
		if next == target {
			return true
		}
		visited[next] = true
		var prev string
		if err := tx.QueryRow(`SELECT prev_hash FROM object_payloads WHERE object_hash = ?`, next).Scan(&prev); err != nil {
			return false
		}
		next = prev
	}
	return false
}

// hashUnit computes the content address of one unit: SHA-256(unit.json
// bytes || content bytes) — the same byte concatenation and digest as
// the RSF per-unit digest (exchange/serialize.go). The concatenation is
// built into an explicit fresh buffer: append() against the caller's
// unitJSON slice could alias its spare capacity.
func hashUnit(unitJSON, content []byte) string {
	buf := make([]byte, 0, len(unitJSON)+len(content))
	buf = append(buf, unitJSON...)
	buf = append(buf, content...)
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// Payload returns the immutable unit_json and content bytes of one
// payload; an error when no payload carries this hash.
func (s *Store) Payload(hash string) (unitJSON, content []byte, err error) {
	var unit, body []byte
	err = s.db.QueryRow(`SELECT unit_json, content FROM object_payloads WHERE object_hash = ?`, hash).
		Scan(&unit, &body)
	if err != nil {
		if isNoRows(err) {
			return nil, nil, fmt.Errorf("store: payload %s not found", hash)
		}
		return nil, nil, fmt.Errorf("store: cannot read payload %s: %w", hash, err)
	}
	return unit, body, nil
}

// PayloadCount returns the number of stored immutable payloads.
func (s *Store) PayloadCount() (int, error) {
	return s.count("object_payloads")
}

// PayloadRow is one immutable payload of the archive.
type PayloadRow struct {
	// ObjectHash is the content-derived key (SHA-256(unit.json ||
	// content)).
	ObjectHash string
	// UnitJSON is the exact canonical RSF unit entry bytes.
	UnitJSON []byte
	// Content is the representation payload bytes (unit content).
	Content []byte
}

// AllPayloads returns every stored payload, sorted by object_hash
// (deterministic order for integrity scans).
func (s *Store) AllPayloads() ([]PayloadRow, error) {
	rows, err := s.db.Query(`SELECT object_hash, unit_json, content FROM object_payloads ORDER BY object_hash`)
	if err != nil {
		return nil, fmt.Errorf("store: cannot query payloads: %w", err)
	}
	defer rows.Close()
	var out []PayloadRow
	for rows.Next() {
		var p PayloadRow
		if err := rows.Scan(&p.ObjectHash, &p.UnitJSON, &p.Content); err != nil {
			return nil, fmt.Errorf("store: cannot scan payload row: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read payloads: %w", err)
	}
	return out, nil
}
