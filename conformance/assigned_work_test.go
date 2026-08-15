package conformance

import (
	"strings"
	"testing"
)

// This file tests the 'Assigned Work' conformance vocabulary and the
// conditional assigned-to sub-check of the R13 gate (ADR-029): the mbr-
// member token, the assigned-to relationship field (R5 vocabulary) and
// the sub-check mechanics — typed target, resolution, single-assignee
// cardinality and repository provenance.

// memberArtifact builds an mbr- artifact of the analyzed set (the typed
// target of an assigned-to edge).
func memberArtifact(ns, id string) *Artifact {
	return &Artifact{
		RelPath:         ns + "/mbr:" + id,
		Namespace:       ns,
		Type:            "mbr",
		ID:              id,
		InstanceVersion: 1,
		Revision:        1,
		States: map[string]string{
			DomainContentState:   "approved",
			DomainExistenceState: "active",
		},
		Relations: map[string][]string{},
	}
}

// TestMBRVocabulary pins the mbr- token acceptance: the 28th type token,
// an Execution operating token (IsKnowledge false) owning content-state
// and existence-state, with the Purpose/Content required sections.
func TestMBRVocabulary(t *testing.T) {
	info := TypeInfoFor("mbr")
	if info == nil {
		t.Fatal("TypeInfoFor(mbr) = nil, want the member token")
	}
	if info.IsKnowledge {
		t.Error("mbr- must be an operating token (IsKnowledge false)")
	}
	if !sameStrings(info.Owned, []string{DomainContentState, DomainExistenceState}) {
		t.Errorf("mbr- owned set = %v, want content-state + existence-state", info.Owned)
	}
	if d, ok := DomainForToken("mbr"); !ok || d != Execution {
		t.Errorf("DomainForToken(mbr) = %q, %v; want Execution, true", d, ok)
	}
	if got := RequiredSectionsFor("mbr"); !sameStrings(got, []string{"Purpose", "Content"}) {
		t.Errorf("RequiredSectionsFor(mbr) = %v, want Purpose + Content", got)
	}
	if !IsKnownType("mbr") {
		t.Error("IsKnownType(mbr) = false, want true")
	}
	// A typed member reference parses once mbr- is a known token.
	if _, err := parseReference("mbr:alice", "ns", "sto"); err != nil {
		t.Errorf("mbr:alice must parse with mbr- registered: %v", err)
	}
	if IsWorkItemType("mbr") {
		t.Error("mbr- must not be a work item type (the R13 gates bind the six work items only)")
	}
	if got := NumberGroup("mbr"); got != "" {
		t.Errorf("NumberGroup(mbr) = %q, want \"\" (member lines carry no issue number)", got)
	}
}

// TestAssignedToRelationshipField pins the assigned-to vocabulary entry:
// the eighth canonical relationship field, accepted by R5 resolution
// (the field is validated exactly like discusses/replies-to — resolution,
// malformed-reference and draft tolerance are the shared R5 mechanics).
func TestAssignedToRelationshipField(t *testing.T) {
	fields := RelationshipFieldNames()
	found := false
	for _, f := range fields {
		if f == "assigned-to" {
			found = true
		}
	}
	if !found {
		t.Fatalf("RelationshipFieldNames = %v, want assigned-to among them", fields)
	}
	if !isRelationshipField("assigned-to") {
		t.Error("isRelationshipField(assigned-to) = false, want true")
	}
}

// TestValidateGraphAssignedToLegacyUntouched: existing data WITHOUT the
// assigned-to field produces ZERO sub-check findings — legacy artifacts
// are untouched. The note gates keep their existing behavior.
func TestValidateGraphAssignedToLegacyUntouched(t *testing.T) {
	// A work item at done with no assigned-to and no notes passes both
	// gates with zero findings.
	work := workItemArtifact("sto", "12", "done")
	if results := ValidateGraph([]*Artifact{work}); len(results) != 0 {
		t.Errorf("legacy work item without assigned-to must produce zero findings, got %v", results)
	}
	// Where the note gate fires, only the note-gate finding appears —
	// no assigned-to findings.
	legacy := workItemArtifact("sto", "12", "in-review")
	results := ValidateGraph([]*Artifact{legacy})
	if len(results) != 1 || results[0].Rule != Rule13 || !strings.Contains(results[0].Message, "in-review") {
		t.Errorf("legacy in-review item: only the note-gate finding may appear, got %v", results)
	}
}

// TestValidateGraphAssignedToValid: a work item at done/in-review with a
// single assigned-to edge resolving to a same-repository mbr- line is
// conformant — zero findings.
func TestValidateGraphAssignedToValid(t *testing.T) {
	work := workItemArtifact("sto", "12", "done")
	work.Relations["assigned-to"] = []string{"mbr:alice"}
	member := memberArtifact("ns", "alice")
	if results := ValidateGraph([]*Artifact{work, member}); len(results) != 0 {
		t.Errorf("a valid same-repository assigned-to must produce zero findings, got %v", results)
	}
	// in-review: the sub-check passes alongside the satisfied note gate.
	review := workItemArtifact("sto", "13", "in-review")
	review.Relations["assigned-to"] = []string{"mbr:bob"}
	note := noteArtifact("13-impl", "implementation", "resolved", "ns/sto:13")
	results := ValidateGraph([]*Artifact{review, memberArtifact("ns", "bob"), note})
	if len(results) != 0 {
		t.Errorf("in-review with a resolved implementation note and a valid assignment must pass, got %v", results)
	}
}

// TestValidateGraphAssignedToNonMemberTarget: an assigned-to target of
// any type other than mbr- is refused.
func TestValidateGraphAssignedToNonMemberTarget(t *testing.T) {
	work := workItemArtifact("sto", "12", "done")
	work.Relations["assigned-to"] = []string{"sto:other"}
	// The target resolves (a work item line of the set) but is not an
	// mbr- line.
	results := ValidateGraph([]*Artifact{work, workItemArtifact("sto", "other", "todo")})
	if len(results) != 1 {
		t.Fatalf("ValidateGraph = %d findings, want 1 (non-member target), got %v", len(results), results)
	}
	if !strings.Contains(results[0].Message, "not a member (mbr-)") {
		t.Errorf("finding = %q, want the non-member diagnostic", results[0].Message)
	}
}

// TestValidateGraphAssignedToUnresolvable: a dangling assigned-to target
// is refused by the sub-check (R5 reports the same dangling reference in
// the full pipeline, mirroring discusses/replies-to).
func TestValidateGraphAssignedToUnresolvable(t *testing.T) {
	work := workItemArtifact("sto", "12", "done")
	work.Relations["assigned-to"] = []string{"mbr:ghost"}
	results := ValidateGraph([]*Artifact{work})
	if len(results) != 1 {
		t.Fatalf("ValidateGraph = %d findings, want 1 (unresolvable target), got %v", len(results), results)
	}
	if !strings.Contains(results[0].Message, "does not resolve") {
		t.Errorf("finding = %q, want the does-not-resolve diagnostic", results[0].Message)
	}
}

// TestValidateGraphAssignedToCrossRepository: an assigned-to target
// originating outside the referring work item's repository is refused
// (repository-level provenance; cross-repository assignment is a
// non-goal of the model).
func TestValidateGraphAssignedToCrossRepository(t *testing.T) {
	work := workItemArtifact("sto", "12", "done")
	// Cross-namespace reference; the target IS resolvable in the set.
	work.Relations["assigned-to"] = []string{"other/mbr:alice"}
	results := ValidateGraph([]*Artifact{work, memberArtifact("other", "alice")})
	if len(results) != 1 {
		t.Fatalf("ValidateGraph = %d findings, want 1 (cross-repository), got %v", len(results), results)
	}
	if !strings.Contains(results[0].Message, "cross-repository") {
		t.Errorf("finding = %q, want the cross-repository diagnostic", results[0].Message)
	}
}

// TestValidateGraphAssignedToMultipleTargets: assigned-to is
// single-assignee — more than one target is refused.
func TestValidateGraphAssignedToMultipleTargets(t *testing.T) {
	work := workItemArtifact("sto", "12", "done")
	work.Relations["assigned-to"] = []string{"mbr:alice", "mbr:bob"}
	set := []*Artifact{work, memberArtifact("ns", "alice"), memberArtifact("ns", "bob")}
	results := ValidateGraph(set)
	if len(results) != 1 {
		t.Fatalf("ValidateGraph = %d findings, want 1 (at-most-one), got %v", len(results), results)
	}
	if !strings.Contains(results[0].Message, "exactly one member target") {
		t.Errorf("finding = %q, want the at-most-one diagnostic", results[0].Message)
	}
	// A multi-target item with one invalid target reports both: the
	// per-target finding and the cardinality finding.
	bad := workItemArtifact("sto", "13", "done")
	bad.Relations["assigned-to"] = []string{"mbr:alice", "sto:other"}
	results = ValidateGraph([]*Artifact{bad, memberArtifact("ns", "alice"), workItemArtifact("sto", "other", "todo")})
	if len(results) != 2 {
		t.Fatalf("ValidateGraph = %d findings, want 2 (non-member + at-most-one), got %v", len(results), results)
	}
}

// TestValidateGraphAssignedToGateOnly: the sub-check binds ONLY when the
// R13 gate applies (in-review/done). A work item at another execution
// state carrying a non-conformant assigned-to produces no findings.
func TestValidateGraphAssignedToGateOnly(t *testing.T) {
	for _, state := range []string{"planned", "todo", "in-progress", "canceled"} {
		work := workItemArtifact("sto", "12", state)
		work.Relations["assigned-to"] = []string{"sto:other", "mbr:ghost"}
		if results := ValidateGraph([]*Artifact{work}); len(results) != 0 {
			t.Errorf("state %s: sub-check must bind only at in-review/done, got %v", state, results)
		}
	}
}

// TestValidateGraphAssignedToDeterministic: the sub-check findings sort
// deterministically — by file (canonical form), then message — like every
// other graph finding.
func TestValidateGraphAssignedToDeterministic(t *testing.T) {
	b := workItemArtifact("sto", "b", "done")
	b.Relations["assigned-to"] = []string{"other/mbr:x"}
	a := workItemArtifact("sto", "a", "done")
	a.Relations["assigned-to"] = []string{"other/mbr:x"}
	results := ValidateGraph([]*Artifact{b, a, memberArtifact("other", "x")})
	if len(results) != 2 {
		t.Fatalf("ValidateGraph = %d findings, want 2, got %v", len(results), results)
	}
	if results[0].File != "ns/sto:a" || results[1].File != "ns/sto:b" {
		t.Errorf("findings must sort by canonical form, got %q then %q", results[0].File, results[1].File)
	}
	// Repeated evaluation is byte-identical (the result ordering is
	// canonical, never map iteration).
	if again := ValidateGraph([]*Artifact{b, a, memberArtifact("other", "x")}); len(again) != 2 ||
		again[0].Message != results[0].Message || again[1].Message != results[1].Message {
		t.Error("repeated evaluation must produce identical findings")
	}
}
