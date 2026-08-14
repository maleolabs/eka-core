package conformance

import (
	"strings"
	"testing"
)

// This file tests the graph-aware pass R13 (ADR-019 D6/D7): the cmt-
// note content contract, the discusses resolution and the work-item
// transition gates.

// noteArtifact builds a cmt- artifact of the analyzed set with the
// given role content and note-state. The content is the full valid
// per-role schema (ADR-019 D7).
func noteArtifact(id string, role string, noteState string, discusses ...string) *Artifact {
	content := map[string]any{}
	switch role {
	case NoteRoleImplementation:
		content = map[string]any{
			"role": role, "summary": "implemented", "changes": []any{"change"}, "tests": []any{"test"},
		}
	case NoteRoleReview:
		content = map[string]any{
			"role": role, "verdict": "approve", "notes": []any{"reviewed"},
		}
	case NoteRoleFix:
		content = map[string]any{
			"role": role, "addresses": []any{"ns/cmt:other"}, "detail": "fixed",
		}
	default:
		content = map[string]any{"role": role}
	}
	return &Artifact{
		RelPath:         "ns/cmt:" + id,
		Namespace:       "ns",
		Type:            "cmt",
		ID:              id,
		InstanceVersion: 1,
		Revision:        1,
		States: map[string]string{
			DomainContentState:   "draft",
			DomainExistenceState: "active",
			DomainNoteState:      noteState,
		},
		ContentFields: content,
		Relations:     map[string][]string{"discusses": discusses},
	}
}

// workItemArtifact builds a work-item artifact of the analyzed set.
func workItemArtifact(typ, id, execState string) *Artifact {
	return &Artifact{
		RelPath:         "ns/" + typ + ":" + id,
		Namespace:       "ns",
		Type:            typ,
		ID:              id,
		InstanceVersion: 1,
		Revision:        1,
		States: map[string]string{
			DomainExecutionState: execState,
			DomainExistenceState: "active",
		},
		Relations: map[string][]string{},
	}
}

func TestValidateGraphInReviewGate(t *testing.T) {
	work := workItemArtifact("sto", "12", "in-review")
	cases := []struct {
		name  string
		notes []*Artifact
		want  int // expected R13 error count
	}{
		{"no notes", nil, 1},
		{"implementation note open", []*Artifact{noteArtifact("12-impl", "implementation", "open", "ns/sto:12")}, 1},
		{"review note resolved", []*Artifact{noteArtifact("12-review", "review", "resolved", "ns/sto:12")}, 1},
		{"implementation note resolved", []*Artifact{noteArtifact("12-impl", "implementation", "resolved", "ns/sto:12")}, 0},
		{"implementation resolved plus open review", []*Artifact{
			noteArtifact("12-impl", "implementation", "resolved", "ns/sto:12"),
			noteArtifact("12-review", "review", "open", "ns/sto:12"),
		}, 0},
	}
	for _, c := range cases {
		set := append([]*Artifact{work}, c.notes...)
		results := ValidateGraph(set)
		if len(results) != c.want {
			t.Errorf("%s: ValidateGraph = %d findings, want %d (%v)", c.name, len(results), c.want, results)
		}
	}
}

func TestValidateGraphDoneGate(t *testing.T) {
	work := workItemArtifact("sto", "12", "done")
	cases := []struct {
		name  string
		notes []*Artifact
		want  int
	}{
		{"no notes", nil, 0}, // no open notes: done with zero notes passes
		{"all resolved", []*Artifact{
			noteArtifact("12-impl", "implementation", "resolved", "ns/sto:12"),
			noteArtifact("12-review", "review", "resolved", "ns/sto:12"),
		}, 0},
		{"one open note", []*Artifact{
			noteArtifact("12-impl", "implementation", "resolved", "ns/sto:12"),
			noteArtifact("12-review", "review", "open", "ns/sto:12"),
		}, 1},
		{"open implementation note", []*Artifact{
			noteArtifact("12-impl", "implementation", "open", "ns/sto:12"),
		}, 1},
	}
	for _, c := range cases {
		set := append([]*Artifact{work}, c.notes...)
		results := ValidateGraph(set)
		if len(results) != c.want {
			t.Errorf("%s: ValidateGraph = %d findings, want %d (%v)", c.name, len(results), c.want, results)
		}
	}
	// The done-gate message lists the open note identities.
	results := ValidateGraph([]*Artifact{
		work,
		noteArtifact("12-impl", "implementation", "resolved", "ns/sto:12"),
		noteArtifact("12-review", "review", "open", "ns/sto:12"),
	})
	if len(results) != 1 || !strings.Contains(results[0].Message, "ns/cmt:12-review") {
		t.Errorf("done gate must list the open note identity, got %v", results)
	}
}

func TestValidateGraphUnresolvableDiscusses(t *testing.T) {
	note := noteArtifact("x", "implementation", "resolved", "ns/sto:missing")
	results := ValidateGraph([]*Artifact{note})
	if len(results) != 1 {
		t.Fatalf("ValidateGraph = %v, want 1 unresolvable-discusses finding", results)
	}
	if results[0].Rule != Rule13 || !strings.Contains(results[0].Message, "does not resolve") {
		t.Errorf("finding = %v, want an R13 unresolvable-discusses error", results[0])
	}
}

func TestValidateGraphNoteOnNonWorkItemUngated(t *testing.T) {
	// A note discussing a knowledge artifact (adr) is valid and ungated.
	adr := &Artifact{
		RelPath: "ns/adr:001", Namespace: "ns", Type: "adr", ID: "001",
		InstanceVersion: 1, Revision: 1,
		States:    map[string]string{DomainContentState: "accepted", DomainExistenceState: "active"},
		Relations: map[string][]string{},
	}
	note := noteArtifact("001-review", "review", "resolved", "ns/adr:001")
	results := ValidateGraph([]*Artifact{adr, note})
	if len(results) != 0 {
		t.Errorf("a note on a non-work-item subject must be valid and ungated, got %v", results)
	}
}

func TestValidateGraphDeterministicOrder(t *testing.T) {
	// Two work items at in-review without notes: findings sorted by file.
	set := []*Artifact{
		workItemArtifact("sto", "b", "in-review"),
		workItemArtifact("sto", "a", "in-review"),
	}
	results := ValidateGraph(set)
	if len(results) != 2 {
		t.Fatalf("ValidateGraph = %d findings, want 2", len(results))
	}
	if results[0].File != "ns/sto:a" || results[1].File != "ns/sto:b" {
		t.Errorf("findings must be sorted by canonical form, got %v then %v", results[0].File, results[1].File)
	}
}

func TestValidateGraphSkipGates(t *testing.T) {
	// CKO-level mode (SkipGates): a single work item at done with no
	// notes must not produce gate findings.
	work := workItemArtifact("sto", "12", "done")
	results := ValidateGraph([]*Artifact{work}, GraphOptions{SkipGates: true})
	if len(results) != 0 {
		t.Errorf("SkipGates must suppress the gate findings, got %v", results)
	}
}

func TestValidateGraphNoteContract(t *testing.T) {
	work := workItemArtifact("sto", "12", "in-review")
	cases := []struct {
		name    string
		content map[string]any
		wantErr bool
	}{
		{"valid implementation", map[string]any{
			"role": "implementation", "summary": "done", "changes": []any{"a"}, "tests": []any{"t"},
		}, false},
		{"valid review", map[string]any{
			"role": "review", "verdict": "approve", "notes": []any{"ok"},
		}, false},
		{"valid fix", map[string]any{
			"role": "fix", "addresses": []any{"ns/cmt:other"}, "detail": "fixed",
		}, false},
		{"unknown role", map[string]any{"role": "nope"}, true},
		{"missing role", map[string]any{"summary": "x"}, true},
		{"missing summary", map[string]any{"role": "implementation", "changes": []any{}, "tests": []any{}}, true},
		{"invalid verdict", map[string]any{"role": "review", "verdict": "reject", "notes": []any{}}, true},
		{"unknown key", map[string]any{"role": "implementation", "summary": "x", "changes": []any{}, "tests": []any{}, "extra": 1}, true},
		{"changes not a list", map[string]any{"role": "implementation", "summary": "x", "changes": "nope", "tests": []any{}}, true},
	}
	for _, c := range cases {
		note := noteArtifact("12-impl", "", "resolved", "ns/sto:12")
		note.ContentFields = c.content
		results := ValidateGraph([]*Artifact{work, note})
		// The gate may add findings for the work item; the contract
		// findings are those reported ON the note itself.
		noteFindings := 0
		for _, r := range results {
			if r.File == note.RelPath {
				noteFindings++
			}
		}
		gotErr := noteFindings > 0
		if gotErr != c.wantErr {
			t.Errorf("%s: note contract findings = %v, wantErr %v", c.name, results, c.wantErr)
		}
	}
}

func TestValidateGraphMarkdownNoteRefused(t *testing.T) {
	// A cmt- artifact without the structured content object (Markdown
	// variant) is refused: notes are JSON-native only (ADR-019 D7).
	note := &Artifact{
		RelPath: "ns/cmt:legacy", Namespace: "ns", Type: "cmt", ID: "legacy",
		InstanceVersion: 1, Revision: 1,
		States: map[string]string{
			DomainContentState: "draft", DomainExistenceState: "active", DomainNoteState: "open",
		},
		Relations: map[string][]string{},
		BodyLines: []string{"## Description", "a markdown note"},
	}
	results := ValidateGraph([]*Artifact{note})
	if len(results) != 1 || !strings.Contains(results[0].Message, "JSON-native") {
		t.Errorf("a Markdown note must be refused with the JSON-native error, got %v", results)
	}
}

// --- cmt- JSON authoring parsing (the analyzeFile path) ---

func TestNoteJSONAuthoringParsing(t *testing.T) {
	valid := `{
  "namespace": "ns",
  "type": "cmt",
  "id": "12-impl",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Agent",
  "created": "2026-08-10",
  "updated": "2026-08-10",
  "domain": "Execution",
  "state": {"contentState": "draft", "existenceState": "active", "noteState": "resolved"},
  "relationships": {"discusses": ["ns/sto:12"]},
  "changeLog": [
    {"date": "2026-08-10", "domain": "contentState", "from": "-", "to": "draft", "by": "Agent"},
    {"date": "2026-08-10", "domain": "existenceState", "from": "-", "to": "active", "by": "Agent"},
    {"date": "2026-08-10", "domain": "noteState", "from": "-", "to": "open", "by": "Agent"},
    {"date": "2026-08-10", "domain": "noteState", "from": "open", "to": "resolved", "by": "Agent"}
  ],
  "content": {"role": "implementation", "summary": "done", "changes": ["a"], "tests": ["t"]}
}`
	a, results := analyzeFile("cmt-12-impl.json", "/x/cmt-12-impl.json", []byte(valid))
	if a == nil || len(results) > 0 {
		t.Fatalf("valid note must parse cleanly: artifact=%v results=%v", a, results)
	}
	if a.Relations["discusses"][0] != "ns/sto:12" {
		t.Errorf("discusses = %v, want the subject", a.Relations["discusses"])
	}
	if a.States[DomainNoteState] != "resolved" {
		t.Errorf("note-state = %q, want resolved", a.States[DomainNoteState])
	}
	if _, ok := a.ContentFields["role"]; !ok {
		t.Errorf("content role missing: %v", a.ContentFields)
	}
	// Array content values are accepted by the JSON adapter (the D7
	// note schemas carry lists).
	if _, ok := a.ContentFields["changes"].([]any); !ok {
		t.Errorf("changes must parse as a list, got %T", a.ContentFields["changes"])
	}
}

func TestNoteJSONAuthoringListRejectedElsewhere(t *testing.T) {
	// The note contract is enforced by the graph pass: an invalid role
	// note parses structurally but fails R13.
	bad := `{
  "namespace": "ns", "type": "cmt", "id": "x", "instanceVersion": 1, "revision": 1,
  "state": {"contentState": "draft", "existenceState": "active", "noteState": "open"},
  "changeLog": [
    {"date": "2026-08-10", "domain": "contentState", "from": "-", "to": "draft", "by": "Agent"},
    {"date": "2026-08-10", "domain": "existenceState", "from": "-", "to": "active", "by": "Agent"},
    {"date": "2026-08-10", "domain": "noteState", "from": "-", "to": "open", "by": "Agent"}
  ],
  "content": {"role": "unknown-role"}
}`
	a, results := analyzeFile("cmt-x.json", "/x/cmt-x.json", []byte(bad))
	if a == nil || len(results) > 0 {
		t.Fatalf("structurally valid note must parse; results=%v", results)
	}
	findings := ValidateGraph([]*Artifact{a})
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "unknown note role") {
		t.Errorf("ValidateGraph = %v, want the unknown-role finding", findings)
	}
}
