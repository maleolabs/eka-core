package view

import (
	"reflect"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file tests the 'Assigned Work' view projections (ADR-029): the
// assignee resolution on work items, the WorkItemsForMember helper and
// the member-scoped board with its dedicated 'No assignee' bucket. All
// membership derives from the assigned-to relationship only (ADR-013).

// assignedWorkGraph builds the shared member-scoped fixture: two member
// lines (alice, bob), two assigned work items and two work items
// without any assigned-to edge.
func assignedWorkGraph(t *testing.T) *Graph {
	t.Helper()
	units := []*exchange.Unit{
		unitFixture(t, "ns", "mbr", "alice", map[string]string{
			conformance.DomainContentState:   "approved",
			conformance.DomainExistenceState: "active",
		}),
		unitFixture(t, "ns", "mbr", "bob", map[string]string{
			conformance.DomainContentState:   "approved",
			conformance.DomainExistenceState: "active",
		}),
		unitFixture(t, "ns", "sto", "one", map[string]string{
			conformance.DomainExecutionState: "todo",
			conformance.DomainExistenceState: "active",
		}, exchange.Relationship{Type: "assigned-to", Target: "mbr:alice"}),
		unitFixture(t, "ns", "sto", "two", map[string]string{
			conformance.DomainExecutionState: "done",
			conformance.DomainExistenceState: "active",
		}, exchange.Relationship{Type: "assigned-to", Target: "mbr:bob"}),
		unitFixture(t, "ns", "sto", "three", map[string]string{
			conformance.DomainExecutionState: "planned",
			conformance.DomainExistenceState: "active",
		}),
		unitFixture(t, "ns", "sto", "four", map[string]string{
			conformance.DomainExecutionState: "in-progress",
			conformance.DomainExistenceState: "active",
		}),
	}
	return NewGraph(".", units)
}

// TestWorkItemAssignee: the assignee resolves from the work item's
// assigned-to relationship to the member line's canonical identity form;
// an item without the edge carries an empty assignee (the 'No assignee'
// state).
func TestWorkItemAssignee(t *testing.T) {
	g := assignedWorkGraph(t)
	want := map[string]string{
		"ns/sto:one":   "ns/mbr:alice",
		"ns/sto:two":   "ns/mbr:bob",
		"ns/sto:three": "",
		"ns/sto:four":  "",
	}
	items := g.WorkItems()
	if len(items) != 4 {
		t.Fatalf("WorkItems = %d, want 4", len(items))
	}
	for _, wi := range items {
		if got := wi.Assignee; got != want[wi.Identity] {
			t.Errorf("%s assignee = %q, want %q", wi.Identity, got, want[wi.Identity])
		}
	}
}

// TestWorkItemsForMember: the helper returns exactly the work items
// whose assigned-to edge resolves to the member line, deduplicated and
// sorted by canonical identity. Items without an assigned-to edge are
// never matched (they surface in the member board's 'No assignee'
// bucket).
func TestWorkItemsForMember(t *testing.T) {
	g := assignedWorkGraph(t)
	cases := []struct {
		member string
		want   []string
	}{
		{"ns/mbr:alice", []string{"ns/sto:one"}},
		{"ns/mbr:bob", []string{"ns/sto:two"}},
		{"ns/mbr:nobody", []string{}},
	}
	for _, c := range cases {
		items := g.WorkItemsForMember(c.member)
		got := make([]string, 0, len(items))
		for _, wi := range items {
			got = append(got, wi.Identity)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("WorkItemsForMember(%s) = %v, want %v", c.member, got, c.want)
		}
	}
	if !reflect.DeepEqual(g.WorkItemsForMember("ns/mbr:alice"), g.WorkItemsForMember("ns/mbr:alice")) {
		t.Error("WorkItemsForMember is not deterministic across repeated calls")
	}
}

// TestBoardForMember: the member-scoped board filters the columns to the
// member's assigned items and surfaces every item without an assigned-to
// edge in the dedicated 'No assignee' bucket — never silently excluded.
// Items assigned to other members are excluded.
func TestBoardForMember(t *testing.T) {
	g := assignedWorkGraph(t)
	board := BoardForMember(g, "ns/mbr:alice")
	if board.Member != "ns/mbr:alice" {
		t.Errorf("Member = %q, want ns/mbr:alice", board.Member)
	}
	// Columns: only alice's item (sto:one, todo).
	todo := board.Columns.Count("todo")
	if todo != 1 {
		t.Errorf("todo column count = %d, want 1 (sto:one)", todo)
	}
	if board.Columns.Count("done") != 0 || board.Columns.Count("planned") != 0 {
		t.Error("the member board must exclude items assigned to other members")
	}
	if got := boardColumnIDs(board.Columns[1]); !reflect.DeepEqual(got, []string{"ns/sto:one"}) {
		t.Errorf("todo column = %v, want [ns/sto:one]", got)
	}
	// 'No assignee' bucket: the two items without the edge, sorted by
	// the board display key (container, created, identity).
	wantNoAssignee := []string{"ns/sto:four", "ns/sto:three"}
	gotNoAssignee := make([]string, 0, len(board.NoAssignee))
	for _, bi := range board.NoAssignee {
		gotNoAssignee = append(gotNoAssignee, bi.Identity)
	}
	if !reflect.DeepEqual(gotNoAssignee, wantNoAssignee) {
		t.Errorf("NoAssignee = %v, want %v", gotNoAssignee, wantNoAssignee)
	}
	// Total surfaces both the scoped columns and the bucket.
	if board.Total != 3 {
		t.Errorf("Total = %d, want 3 (1 assigned + 2 no-assignee)", board.Total)
	}
}

// TestBoardForMemberAllUnassigned: a repository without any assigned-to
// edge projects an empty member board whose entire work is surfaced in
// the 'No assignee' bucket — legacy data without the field is never
// excluded (ADR-029 Decision 3).
func TestBoardForMemberAllUnassigned(t *testing.T) {
	g := loadFixture(t, "valid")
	board := BoardForMember(g, "eka-view-fixture/mbr:nobody")
	if board.Total != 6 {
		t.Errorf("Total = %d, want 6 (every fixture item is unassigned)", board.Total)
	}
	if len(board.NoAssignee) != 6 {
		t.Errorf("NoAssignee = %d items, want 6 (all)", len(board.NoAssignee))
	}
	total := 0
	for _, col := range board.Columns {
		total += len(col.WorkItems)
	}
	if total != 0 {
		t.Errorf("columns hold %d items, want 0 (no item is assigned)", total)
	}
}

// TestBoardForMemberDeterministic: the member-scoped board is
// byte-deterministic — repeated builds produce identical models (sorted
// columns and bucket, no map iteration in output ordering).
func TestBoardForMemberDeterministic(t *testing.T) {
	g := assignedWorkGraph(t)
	first := BoardForMember(g, "ns/mbr:alice")
	second := BoardForMember(g, "ns/mbr:alice")
	if !reflect.DeepEqual(first, second) {
		t.Error("BoardForMember is not deterministic across repeated builds")
	}
	// The repository-wide board stays untouched: the assignee field is
	// additive, the 'No assignee' bucket belongs to the member scope.
	proj, err := Build("board", g, "")
	if err != nil {
		t.Fatalf("Build(board): %v", err)
	}
	board := proj.(*BoardProjection)
	if board.Member != "" || len(board.NoAssignee) != 0 {
		t.Errorf("repository-wide board must stay unscoped, got Member=%q NoAssignee=%v", board.Member, board.NoAssignee)
	}
	if board.Total != 4 {
		t.Errorf("repository-wide board Total = %d, want 4", board.Total)
	}
	// The assignee field is visible on the repository-wide board too.
	for _, col := range board.Columns {
		for _, bi := range col.WorkItems {
			if bi.Identity == "ns/sto:one" && bi.Assignee != "ns/mbr:alice" {
				t.Errorf("repository-wide board assignee = %q, want ns/mbr:alice", bi.Assignee)
			}
		}
	}
}
