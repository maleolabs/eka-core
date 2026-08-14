package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the schema v1 -> v2 migration: the conversion
// of an experimental v0.2.0 workspace database (objects,
// relationships, change_log tables) onto the Immutable Engineering
// Knowledge Model.
//
// Every v1 object row is reconstructed as an exchange.Unit — the same
// construction sync/push.go unitFromObject performed, including its
// relationships rows (ordered by (rel_type, target)) and its change-log
// rows (ordered by seq) — and serialized to its canonical unit.json
// bytes via exchange.MarshalUnit. The payload is stored with
// object_hash = SHA-256(unit.json || content) RECOMPUTED from the
// reconstructed bytes (never trusted from the v1 digest column: for
// foreign packages the reconstructed bytes can differ from the
// original raw entry). prev_hash = "" (v1 carried no payload history;
// the change-log transitions are preserved inside the unit.json
// payload), created_at = migration run date.
//
// The migration runs inside store.Open's migrate() transaction: the v2
// tables are created, every v1 row is converted, and only then are the
// v1 tables dropped — a failure mid-way leaves the database at v1 and
// the migration restarts cleanly on the next Open.
//
// The v1 objects table and this migration exist only for the upgrade
// path; fresh databases create the v2 schema directly (schema.go).

// v1Object is one row of the v1 objects table.
type v1Object struct {
	Form                  string
	ProjectID             string
	Namespace             string
	Type                  string
	ID                    string
	InstanceVersion       int
	Revision              int
	Author                string
	Created               string
	Updated               string
	ContentRepresentation string
	Content               []byte
	StateContent          string
	StateExecution        string
	StatePlanning         string
	StateContainer        string
	StateExistence        string
	Phase                 string
	Dimension             string
	Domain                string
	SourceRepo            string
	DimensionsSecondary   []string
}

// v1RelationshipRow is one row of the v1 relationships table.
type v1RelationshipRow struct {
	RelType string
	Target  string
}

// v1ChangeLogRow is one row of the v1 change_log table, in seq order.
type v1ChangeLogRow struct {
	Date   string
	Domain string
	From   string
	To     string
	By     string
}

// migrateV1toV2 converts a v1 schema database to v2 in one
// transaction: create the v2 tables, reconstruct every v1 object as an
// immutable payload + reference, then drop the v1 tables.
func migrateV1toV2(tx *sql.Tx) error {
	if err := migrateToV2(tx); err != nil {
		return err
	}

	objs, err := readV1Objects(tx)
	if err != nil {
		return err
	}
	rels, err := readV1Relationships(tx)
	if err != nil {
		return err
	}
	logs, err := readV1ChangeLogs(tx)
	if err != nil {
		return err
	}

	createdAt := time.Now().UTC().Format(time.RFC3339)
	for _, o := range objs {
		u := unitFromV1(o, rels[o.Form], logs[o.Form])
		unitJSON, err := exchange.MarshalUnit(u)
		if err != nil {
			return fmt.Errorf("store: cannot serialize migrated unit %s: %w", o.Form, err)
		}
		objectHash := hashUnit(unitJSON, o.Content)
		if _, err := tx.Exec(`INSERT INTO object_payloads (object_hash, unit_json, content, prev_hash, created_at)
			VALUES (?, ?, ?, '', ?)`, objectHash, unitJSON, o.Content, createdAt); err != nil {
			return fmt.Errorf("store: cannot insert migrated payload %s: %w", objectHash, err)
		}
		if _, err := tx.Exec(`INSERT INTO object_refs (
			form, object_hash, project_id, source_repo, namespace, type, id,
			instance_version, revision, dimension, domain, phase, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			o.Form, objectHash, o.ProjectID, o.SourceRepo, o.Namespace, o.Type, o.ID,
			o.InstanceVersion, o.Revision, o.Dimension, o.Domain, o.Phase, createdAt); err != nil {
			return fmt.Errorf("store: cannot insert migrated reference %s: %w", o.Form, err)
		}
	}

	// Drop the v1 tables children-first: relationships and change_log
	// reference objects, and with foreign_keys on a DROP TABLE of a
	// referenced table runs an implicit DELETE that the FK would reject
	// while child rows remain.
	for _, table := range []string{"relationships", "change_log", "objects"} {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
			return fmt.Errorf("store: cannot drop v1 table %s: %w", table, err)
		}
	}
	return nil
}

// readV1Objects loads every v1 object row in deterministic form order.
func readV1Objects(tx *sql.Tx) ([]v1Object, error) {
	rows, err := tx.Query(`SELECT
		form, project_id, namespace, type, id, instance_version, revision,
		author, created, updated, content_representation, content,
		state_content, state_execution, state_planning, state_container,
		state_existence, phase, dimension, domain, source_repo,
		dimensions_secondary
		FROM objects ORDER BY form`)
	if err != nil {
		return nil, fmt.Errorf("store: cannot read v1 objects: %w", err)
	}
	defer rows.Close()
	var out []v1Object
	for rows.Next() {
		var o v1Object
		var secondary string
		var content []byte
		if err := rows.Scan(
			&o.Form, &o.ProjectID, &o.Namespace, &o.Type, &o.ID, &o.InstanceVersion, &o.Revision,
			&o.Author, &o.Created, &o.Updated, &o.ContentRepresentation, &content,
			&o.StateContent, &o.StateExecution, &o.StatePlanning, &o.StateContainer,
			&o.StateExistence, &o.Phase, &o.Dimension, &o.Domain, &o.SourceRepo,
			&secondary); err != nil {
			return nil, fmt.Errorf("store: cannot scan v1 object row: %w", err)
		}
		o.Content = content
		if o.DimensionsSecondary, err = decodeSecondary(secondary); err != nil {
			return nil, fmt.Errorf("store: cannot decode v1 dimensions of %s: %w", o.Form, err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read v1 objects: %w", err)
	}
	return out, nil
}

// readV1Relationships loads every v1 relationship row, grouped by form,
// each list ordered by (rel_type, target).
func readV1Relationships(tx *sql.Tx) (map[string][]v1RelationshipRow, error) {
	rows, err := tx.Query(`SELECT form, rel_type, target FROM relationships ORDER BY form, rel_type, target`)
	if err != nil {
		return nil, fmt.Errorf("store: cannot read v1 relationships: %w", err)
	}
	defer rows.Close()
	out := map[string][]v1RelationshipRow{}
	for rows.Next() {
		var form string
		var r v1RelationshipRow
		if err := rows.Scan(&form, &r.RelType, &r.Target); err != nil {
			return nil, fmt.Errorf("store: cannot scan v1 relationship row: %w", err)
		}
		out[form] = append(out[form], r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read v1 relationships: %w", err)
	}
	return out, nil
}

// readV1ChangeLogs loads every v1 change-log row, grouped by form, each
// list in occurrence order (seq ascending).
func readV1ChangeLogs(tx *sql.Tx) (map[string][]v1ChangeLogRow, error) {
	rows, err := tx.Query(`SELECT form, date, domain, from_val, to_val, by FROM change_log ORDER BY form, seq`)
	if err != nil {
		return nil, fmt.Errorf("store: cannot read v1 change log: %w", err)
	}
	defer rows.Close()
	out := map[string][]v1ChangeLogRow{}
	for rows.Next() {
		var form string
		var e v1ChangeLogRow
		if err := rows.Scan(&form, &e.Date, &e.Domain, &e.From, &e.To, &e.By); err != nil {
			return nil, fmt.Errorf("store: cannot scan v1 change-log row: %w", err)
		}
		out[form] = append(out[form], e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cannot read v1 change log: %w", err)
	}
	return out, nil
}

// unitFromV1 projects one v1 object row (plus its relationships and
// change log) onto an exchange.Unit — the same construction
// sync/push.go unitFromObject performed on the v1 store. Empty
// collections are never nil: JSON encodes [] not null (the serializer
// contract), and the reconstructed bytes must be exactly what a fresh
// v2 pull of the same repository would store.
func unitFromV1(o v1Object, rels []v1RelationshipRow, log []v1ChangeLogRow) *exchange.Unit {
	u := &exchange.Unit{
		Identity: exchange.Identity{
			Namespace:       o.Namespace,
			Type:            o.Type,
			ID:              o.ID,
			InstanceVersion: o.InstanceVersion,
		},
		Revision: o.Revision,
		Author:   conformance.AuthorIdentity{Kind: conformance.KindUser, Name: o.Author},
		Created:  o.Created,
		Updated:  o.Updated,
		StateVector: exchange.StateVector{
			ContentState:   o.StateContent,
			ExecutionState: o.StateExecution,
			PlanningState:  o.StatePlanning,
			ContainerState: o.StateContainer,
			ExistenceState: o.StateExistence,
		},
		Classification: exchange.Classification{
			Dimension:           o.Dimension,
			DimensionsSecondary: o.DimensionsSecondary,
			Domain:              o.Domain,
		},
		Phase: o.Phase,
		Content: exchange.ContentRef{
			Representation: o.ContentRepresentation,
			File:           "content",
		},
		ContentPayload: o.Content,
		ChangeLog:      []exchange.ChangeLogEntry{},
		Relationships:  []exchange.Relationship{},
	}
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()
	for _, r := range rels {
		u.Relationships = append(u.Relationships, exchange.Relationship{Type: r.RelType, Target: r.Target})
	}
	for _, e := range log {
		u.ChangeLog = append(u.ChangeLog, exchange.ChangeLogEntry{
			Date: e.Date, Domain: e.Domain, From: e.From, To: e.To, By: conformance.AuthorIdentity{Kind: conformance.KindUser, Name: e.By},
		})
	}
	return u
}

// decodeSecondary parses the persisted v1 secondary-dimension list back
// into a slice; an empty string decodes to nil.
func decodeSecondary(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("cannot decode secondary dimensions: %w", err)
	}
	return out, nil
}
