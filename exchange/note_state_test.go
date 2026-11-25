package exchange

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// note_state_test.go guards the note-state domain across the package
// boundary. A cmt- note is the only type that owns note-state
// (conformance.TypeInfoFor), so it is the only artifact that can prove the
// domain is addressed by the import engine at all: the state-vector
// accessor and the known-domain set both have to know it, and the
// authoring-document renderer has to write it back. Any one of the three
// missing rejects the whole package (analysis runs before any write), not
// just the note: the refusal is "unknown domain", which reads like a
// malformed package and hides a plain gap in the engine.
//
// The refusal itself is pinned below (a bogus domain must still abort):
// teaching the engine a sixth domain must not turn the known-domain set
// into a permissive default.

// fixturesNotes is a repository holding one story and one cmt- note whose
// change log covers note-state (open -> resolved). Separate from
// fixtureValid on purpose: fixtureValid's unit count and manifest order are
// asserted by value in several tests.
const fixturesNotes = "testdata/notes"

// TestImportRoundTripCarriesNoteState: a package containing a cmt- note
// imports, and the re-export is byte-identical. Both halves matter — the
// import proves note-state is addressed during analysis, the byte-identical
// re-export proves the unit state vector and the written authoring document
// both carry the domain (a renderer that dropped it would produce a second
// package with an empty note-state and a phase-10 revalidation failure).
func TestImportRoundTripCarriesNoteState(t *testing.T) {
	pkgA := exportPackage(t, fixturesNotes)

	entries, _ := readZIP(t, pkgA)
	identity := Identity{Namespace: "eka-notes-fixture", Type: "cmt", ID: "note-gate-review", InstanceVersion: 1}
	unitJSON, ok := entries[UnitDirName(identity)+"/unit.json"]
	if !ok {
		t.Fatalf("package has no unit entry for %s", identity.CanonicalForm())
	}
	if !strings.Contains(string(unitJSON), `"noteState":"resolved"`) {
		t.Errorf("exported note does not carry note-state in its state vector:\n%s", truncate(unitJSON, 600))
	}

	repoB := initTestRepo(t)
	mustImport(t, pkgA, ImportOptions{Root: repoB})

	imported := filepath.Join(repoB, "docs", "operating", "notes", "cmt-note-gate-review.json")
	data, err := os.ReadFile(imported)
	if err != nil {
		t.Fatalf("imported note missing: %v", err)
	}
	if !strings.Contains(string(data), `"noteState": "resolved"`) {
		t.Errorf("imported note lost its note-state:\n%s", truncate(data, 600))
	}

	assertPackagesIdentical(t, pkgA, exportPackage(t, repoB))
}

// TestImportRefusesUnknownStateDomain: the known-domain set stays a closed
// set. A change-log entry naming a domain the engine does not model is a
// package error with the deterministic message, and the domain that is
// merely *new to the fixture* (note-state) is not a licence to accept
// spelling variants. The entry is appended next to the note's own
// note-state entries so the change-log consistency pass (which only looks
// at owned domains) cannot fire first and mask the refusal under test.
func TestImportRefusesUnknownStateDomain(t *testing.T) {
	pkg := exportPackage(t, fixturesNotes)
	mutated := mutatePackage(t, pkg, func(entries map[string][]byte) {
		name := UnitDirName(Identity{Namespace: "eka-notes-fixture", Type: "cmt", ID: "note-gate-review", InstanceVersion: 1}) + "/unit.json"
		var u map[string]any
		if err := json.Unmarshal(entries[name], &u); err != nil {
			t.Fatal(err)
		}
		log, _ := u["changeLog"].([]any)
		by := log[len(log)-1].(map[string]any)["by"]
		u["changeLog"] = append(log, map[string]any{
			"date": "2026-08-05", "domain": "note-states", "from": "-", "to": "open", "by": by,
		})
		data, err := json.Marshal(u)
		if err != nil {
			t.Fatal(err)
		}
		entries[name] = data
	})

	_, err := Import(mutated, ImportOptions{Root: initTestRepo(t)})
	if err == nil {
		t.Fatal("a change-log entry with an unknown domain must abort the import")
	}
	want := `unknown domain "note-states"`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want the refusal to name %s", err, want)
	}
}
