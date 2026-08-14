package view

import (
	"reflect"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// boardColumnIDs returns the identities of one board column.
func boardColumnIDs(col BoardColumn) []string {
	out := make([]string, 0, len(col.WorkItems))
	for _, bi := range col.WorkItems {
		out = append(out, bi.Identity)
	}
	return out
}

// boardColumnContainers returns the container tags of one board column.
func boardColumnContainers(col BoardColumn) [][]string {
	out := make([][]string, 0, len(col.WorkItems))
	for _, bi := range col.WorkItems {
		out = append(out, bi.Containers)
	}
	return out
}

func TestBoardProjectionValid(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("board", g, "")
	if err != nil {
		t.Fatalf("Build(board): %v", err)
	}
	board, ok := p.(*BoardProjection)
	if !ok {
		t.Fatalf("Build(board) = %T, want *BoardProjection", p)
	}
	if board.Name() != "board" {
		t.Errorf("Name() = %q, want board", board.Name())
	}
	// All six work items of the fixture — the active container's five
	// plus the completed container's legacy item.
	if board.Total != 6 {
		t.Errorf("Total = %d, want 6", board.Total)
	}
	if board.Unassigned != 0 {
		t.Errorf("Unassigned = %d, want 0", board.Unassigned)
	}
	if board.ContainerCount != 2 {
		t.Errorf("ContainerCount = %d, want 2 (wave-0, wave-1)", board.ContainerCount)
	}

	// Fixed column order (six values since ADR-019: canceled added).
	wantOrder := []string{"planned", "todo", "in-progress", "in-review", "done", "canceled"}
	gotOrder := make([]string, len(board.Columns))
	for i, col := range board.Columns {
		gotOrder[i] = col.State
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("column order = %v, want %v", gotOrder, wantOrder)
	}

	// Per-column membership and container tags.
	cases := map[string]struct {
		items      []string
		containers [][]string
	}{
		"planned": {
			items:      []string{validForm + "sto:alpha"},
			containers: [][]string{{validForm + "ctr:wave-1"}},
		},
		"todo": {
			items:      []string{validForm + "sto:beta"},
			containers: [][]string{{validForm + "ctr:wave-1"}},
		},
		"in-progress": {
			items:      []string{validForm + "ts:gamma"},
			containers: [][]string{{validForm + "ctr:wave-1"}},
		},
		"in-review": {
			items:      []string{validForm + "bug:delta"},
			containers: [][]string{{validForm + "ctr:wave-1"}},
		},
		"done": {
			// Ordered by the first referencing container (wave-0
			// before wave-1), then created date, then identity.
			items:      []string{validForm + "sto:legacy", validForm + "ch:epsilon"},
			containers: [][]string{{validForm + "ctr:wave-0"}, {validForm + "ctr:wave-1"}},
		},
		"canceled": {
			items:      []string{},
			containers: [][]string{},
		},
	}
	for _, col := range board.Columns {
		want := cases[col.State]
		if got := boardColumnIDs(col); !reflect.DeepEqual(got, want.items) {
			t.Errorf("%s column items = %v, want %v", col.State, got, want.items)
		}
		if got := boardColumnContainers(col); !reflect.DeepEqual(got, want.containers) {
			t.Errorf("%s column containers = %v, want %v", col.State, got, want.containers)
		}
	}

	// Count mirrors the columns.
	if board.Columns.Count("done") != 2 || board.Columns.Count("in-progress") != 1 {
		t.Errorf("Count(done/in-progress) = %d/%d, want 2/1",
			board.Columns.Count("done"), board.Columns.Count("in-progress"))
	}
	if board.Columns.Count("nonexistent") != 0 {
		t.Error("Count of an unknown state must be 0")
	}

	// Every item carries its note count (the published notes
	// discussing it) — the fixture's delta, epsilon and legacy each
	// have one cmt- note (cmt-delta-implementation,
	// cmt-epsilon-review, cmt-legacy-review).
	wantNotes := map[string]int{
		validForm + "sto:alpha":  0,
		validForm + "sto:beta":   0,
		validForm + "ts:gamma":   0,
		validForm + "bug:delta":  1,
		validForm + "ch:epsilon": 1,
		validForm + "sto:legacy": 1,
	}
	for _, col := range board.Columns {
		for _, bi := range col.WorkItems {
			if got := bi.WorkItem.NotesCount; got != wantNotes[bi.Identity] {
				t.Errorf("%s NotesCount = %d, want %d", bi.Identity, got, wantNotes[bi.Identity])
			}
		}
	}
}

// TestBoardProjectionUnassigned: a work item that no ticket references
// is still projected, tagged as unassigned.
func TestBoardProjectionUnassigned(t *testing.T) {
	g := loadFixture(t, "no-active")
	p, err := Build("board", g, "")
	if err != nil {
		t.Fatalf("Build(board): %v", err)
	}
	board := p.(*BoardProjection)
	if board.Total != 1 {
		t.Errorf("Total = %d, want 1 (sto:legacy)", board.Total)
	}
	if board.Unassigned != 0 {
		t.Errorf("Unassigned = %d, want 0 (legacy is referenced by tkt-sto-legacy)", board.Unassigned)
	}
	if board.ContainerCount != 1 {
		t.Errorf("ContainerCount = %d, want 1", board.ContainerCount)
	}
	// Container tags resolve regardless of container-state: wave-0 is
	// completed, not active.
	col := board.Columns.Count("done")
	if col != 1 {
		t.Fatalf("done column count = %d, want 1", col)
	}
}

// TestBoardProjectionOrphan: a work item referenced by no ticket at
// all is unassigned.
func TestBoardProjectionOrphan(t *testing.T) {
	g := NewGraph(".", []*exchange.Unit{
		unitFixture(t, "ns", "sto", "orphan",
			map[string]string{conformance.DomainExecutionState: "planned"}),
	})
	p, err := Build("board", g, "")
	if err != nil {
		t.Fatalf("Build(board): %v", err)
	}
	board := p.(*BoardProjection)
	if board.Total != 1 || board.Unassigned != 1 {
		t.Errorf("Total/Unassigned = %d/%d, want 1/1", board.Total, board.Unassigned)
	}
	if board.ContainerCount != 0 {
		t.Errorf("ContainerCount = %d, want 0", board.ContainerCount)
	}
	if got := boardColumnContainers(board.Columns[0]); len(got) != 1 || len(got[0]) != 0 {
		t.Errorf("orphan containers = %v, want empty tag", got)
	}
}

// TestBoardProjectionMultiContainer: a work item referenced by tickets
// of two different containers carries both container tags, sorted.
func TestBoardProjectionMultiContainer(t *testing.T) {
	units := []*exchange.Unit{
		unitFixture(t, "ns", "ctr", "wave-b",
			map[string]string{conformance.DomainContainerState: "active"}),
		unitFixture(t, "ns", "ctr", "wave-a",
			map[string]string{conformance.DomainContainerState: "completed"}),
		unitFixture(t, "ns", "sto", "shared",
			map[string]string{conformance.DomainExecutionState: "todo"}),
		unitFixture(t, "ns", "tkt", "one", nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-b"},
			exchange.Relationship{Type: "derives-from", Target: "sto:shared"}),
		unitFixture(t, "ns", "tkt", "two", nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-a"},
			exchange.Relationship{Type: "derives-from", Target: "sto:shared"}),
	}
	g := NewGraph(".", units)
	p, err := Build("board", g, "")
	if err != nil {
		t.Fatalf("Build(board): %v", err)
	}
	board := p.(*BoardProjection)
	if board.Total != 1 || board.Unassigned != 0 {
		t.Errorf("Total/Unassigned = %d/%d, want 1/0", board.Total, board.Unassigned)
	}
	if board.ContainerCount != 2 {
		t.Errorf("ContainerCount = %d, want 2", board.ContainerCount)
	}
	want := []string{"ns/ctr:wave-a", "ns/ctr:wave-b"}
	if got := boardColumnContainers(board.Columns[1]); !reflect.DeepEqual(got[0], want) {
		t.Errorf("shared containers = %v, want %v (sorted)", got[0], want)
	}
}

// TestBoardProjectionSortContainerCreated: board items order by the
// first referencing container ("" for unassigned — natural ascending
// puts unassigned items first), then created date ascending, then
// canonical identity — within each state column.
func TestBoardProjectionSortContainerCreated(t *testing.T) {
	mkItem := func(id, created string) *exchange.Unit {
		u := unitFixture(t, "ns", "sto", id, map[string]string{
			conformance.DomainExecutionState: "done",
		})
		u.Created = created
		return u
	}
	units := []*exchange.Unit{
		unitFixture(t, "ns", "ctr", "wave-b", map[string]string{
			conformance.DomainContainerState: "active",
			conformance.DomainExistenceState: "active",
		}),
		unitFixture(t, "ns", "ctr", "wave-a", map[string]string{
			conformance.DomainContainerState: "completed",
			conformance.DomainExistenceState: "active",
		}),
		// Unassigned items (no ticket): orphan-new (08-05) and
		// orphan-old (08-01) — created ascending puts orphan-old first.
		mkItem("orphan-new", "2026-08-05"),
		mkItem("orphan-old", "2026-08-01"),
		// Assigned items: assigned-old lives in wave-a (created
		// 08-01), assigned-new in wave-b (created 08-05).
		mkItem("assigned-old", "2026-08-01"),
		mkItem("assigned-new", "2026-08-05"),
	}
	link := func(id, tktID, container string) {
		units = append(units, unitFixture(t, "ns", "tkt", tktID, nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:" + container},
			exchange.Relationship{Type: "derives-from", Target: "sto:" + id}))
	}
	link("assigned-old", "t-old", "wave-a")
	link("assigned-new", "t-new", "wave-b")

	g := NewGraph(".", units)
	proj, err := Build("board", g, "")
	if err != nil {
		t.Fatalf("Build(board): %v", err)
	}
	board := proj.(*BoardProjection)
	if board.Total != 4 || board.Unassigned != 2 {
		t.Fatalf("total/unassigned = %d/%d, want 4/2", board.Total, board.Unassigned)
	}
	// done column: unassigned first (orphan-old, orphan-new), then
	// wave-a (assigned-old), then wave-b (assigned-new).
	want := []string{"ns/sto:orphan-old", "ns/sto:orphan-new", "ns/sto:assigned-old", "ns/sto:assigned-new"}
	if got := boardColumnIDs(board.Columns[4]); !reflect.DeepEqual(got, want) {
		t.Errorf("done column = %v, want %v (container, created, identity)", got, want)
	}
}

// TestBoardProjectionEmpty: a repository without work items projects an
// empty board — a valid state, not an error.
func TestBoardProjectionEmpty(t *testing.T) {
	g := NewGraph(".", nil)
	p, err := Build("board", g, "")
	if err != nil {
		t.Fatalf("Build(board): %v", err)
	}
	board := p.(*BoardProjection)
	if board.Total != 0 || board.Unassigned != 0 || board.ContainerCount != 0 {
		t.Errorf("empty board = %+v, want all-zero", board)
	}
	if len(board.Columns) != 6 {
		t.Errorf("empty board columns = %d, want the fixed six (canceled added, ADR-019)", len(board.Columns))
	}
}
