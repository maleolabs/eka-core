package view

import (
	"fmt"
	"sort"

	"github.com/maleolabs/eka-core/conformance"
)

// This file implements the board projection: the canonical view of ALL
// work items in the repository — across every execution container
// (active and completed) and outside any container — on the fixed
// execution-state board. Where the execution projection answers "what
// is currently being worked on?" (one active container), the board
// answers "what is the total work in the repository?". The member-
// scoped board (BoardForMember, ADR-029) answers "what is this member
// responsible for?" — columns filtered to the member's assigned items
// plus the dedicated 'No assignee' bucket.
//
// Derivation (see package doc for the membership rule):
//   - Items: every work item line in the repository whose type owns the
//     Execution State domain (g.WorkItems), deduplicated by identity
//     line and sorted by canonical identity.
//   - Container tags: for each item, the canonical identities of the
//     containers whose tickets reference it (g.ContainersForWorkItem),
//     sorted. An item without any referencing container is unassigned.
//   - Assignee: the canonical line identity form of the member (mbr-)
//     line the item's assigned-to relationship resolves to, "" when the
//     item carries no assigned-to edge (ADR-029; relationship-only,
//     ADR-013).
//   - Ordering: within a column, items sort by the first referencing
//     container ("" for unassigned — natural ascending puts unassigned
//     items first), then created date ascending ("" first,
//     deterministic), then canonical identity. Machine retrieval
//     (graph.WorkItems, `eka get`) keeps canonical ordering.
//   - Grouping: the fixed execution-state column order planned, todo,
//     in-progress, in-review, done, canceled (ADR-019).
//     in-progress, in-review, done — identical to the execution
//     projection, so both boards read the same way.
//
// The repository-wide board projection ignores the optional target
// argument; the member-scoped board is built with BoardForMember.

// BoardItem is one work item of the board: the work item itself plus
// the containers that reference it through their tickets.
type BoardItem struct {
	WorkItem
	// Containers are the canonical identities of the containers whose
	// tickets reference this item, sorted; empty means unassigned.
	Containers []string
}

// BoardColumn is one execution-state column of the board.
type BoardColumn struct {
	// State is the execution-state value of the column.
	State string
	// WorkItems are the column's items, ordered by the board's
	// display key: first referencing container, then created date,
	// then canonical identity.
	WorkItems []BoardItem
}

// BoardColumns is the ordered execution-state column set of the board
// (always the full six-column set, with zero-item columns when empty).
type BoardColumns []BoardColumn

// BoardProjection is the repository-wide work items view.
type BoardProjection struct {
	// Columns are the fixed execution-state columns (always the full
	// six-column set, with zero-item columns when empty).
	Columns BoardColumns
	// Total is the number of work items placed in the columns (the
	// repository-wide board) or the number of surfaced items — the
	// scoped columns plus the 'No assignee' bucket — of the
	// member-scoped board.
	Total int
	// Unassigned is the number of work items not referenced by any
	// ticket container (repository-wide board only).
	Unassigned int
	// ContainerCount is the number of distinct containers that
	// reference at least one board item.
	ContainerCount int
	// Member scopes the board to one member line: the canonical line
	// identity form of the mbr- line the columns are filtered to ("" =
	// repository-wide board). Set by BoardForMember.
	Member string
	// NoAssignee lists the work items WITHOUT any assigned-to edge —
	// the dedicated bucket of the member-scoped board (ADR-029
	// Decision 3: unassigned items surface here, never silently
	// excluded). The repository-wide board keeps it empty: those items
	// already surface in their state columns.
	NoAssignee []BoardItem
}

// Name returns the registry name of the projection.
func (p *BoardProjection) Name() string { return "board" }

// buildBoard assembles the repository-wide board: every work item of
// the repository, deduplicated by identity line, tagged with its
// referencing containers and grouped by execution state. It ignores the
// optional target argument.
func buildBoard(g *Graph, _ string) (Projection, error) {
	items, unassigned, containers := g.boardItems(g.WorkItems())
	p := &BoardProjection{
		Columns:        groupByStateBoard(items),
		Total:          len(items),
		Unassigned:     unassigned,
		ContainerCount: len(containers),
	}
	return p, nil
}

// BoardForMember returns the member-scoped board: the columns hold only
// the work items whose assigned-to relationship resolves to the member
// line (the mbr- line with canonical identity form memberForm), and the
// dedicated 'No assignee' bucket lists every work item WITHOUT an
// assigned-to edge — unassigned items surface here, never silently
// excluded (ADR-029 Decision 3). Items assigned to other members are
// excluded. Membership derives from the assigned-to relationship only
// (ADR-013 — never from content). The container tags, display ordering
// and state grouping mirror the repository-wide board. An empty
// memberForm is refused: with no member line the scoped columns would
// be empty while every unassigned item fills the bucket — a degenerate
// double-count view that resolves to nothing useful.
func BoardForMember(g *Graph, memberForm string) (*BoardProjection, error) {
	if memberForm == "" {
		return nil, fmt.Errorf("member board: a member line form is required (the canonical identity form of an mbr- line, e.g. \"ns/mbr:alice\")")
	}
	scoped, _, _ := g.boardItems(g.WorkItemsForMember(memberForm))
	var noAssignee []WorkItem
	for _, wi := range g.WorkItems() {
		if wi.Assignee == "" {
			noAssignee = append(noAssignee, wi)
		}
	}
	bucket, _, _ := g.boardItems(noAssignee)
	return &BoardProjection{
		Columns:    groupByStateBoard(scoped),
		Total:      len(scoped) + len(bucket),
		Member:     memberForm,
		NoAssignee: bucket,
	}, nil
}

// boardItems tags work items with their referencing containers, counts
// the container-unassigned items and the distinct referencing
// containers, and orders the items by the board's display key: the
// first referencing container ("" for unassigned — natural ascending
// puts unassigned items first), then created date ascending, then
// canonical identity. The shared assembly of the repository-wide and
// the member-scoped board, so both read the same way.
func (g *Graph) boardItems(items []WorkItem) ([]BoardItem, int, map[string]bool) {
	containers := make(map[string]bool, 0)
	boardItems := make([]BoardItem, 0, len(items))
	unassigned := 0
	for _, wi := range items {
		// Every item carries its note count (the published notes
		// discussing it) — the board shows the counts, mirroring the
		// execution projection.
		wi.NotesCount = len(g.NotesFor(wi.Identity))
		bi := BoardItem{WorkItem: wi, Containers: g.ContainersForWorkItem(wi.Identity)}
		if len(bi.Containers) == 0 {
			unassigned++
		} else {
			for _, c := range bi.Containers {
				containers[c] = true
			}
		}
		boardItems = append(boardItems, bi)
	}
	sort.SliceStable(boardItems, func(i, j int) bool {
		c1, c2 := boardContainer(boardItems[i]), boardContainer(boardItems[j])
		if c1 != c2 {
			return c1 < c2
		}
		if boardItems[i].Created != boardItems[j].Created {
			return boardItems[i].Created < boardItems[j].Created
		}
		return boardItems[i].Identity < boardItems[j].Identity
	})
	return boardItems, unassigned, containers
}

// boardContainer returns the item's first referencing container in
// canonical order ("" when the item is unassigned) — the board's
// primary grouping key.
func boardContainer(bi BoardItem) string {
	if len(bi.Containers) > 0 {
		return bi.Containers[0]
	}
	return ""
}

// groupByStateBoard groups board items into the fixed execution-state
// column order. It mirrors groupByState for the BoardItem payload; the
// state ordering contract (conformance.DomainValues) is shared, so the
// board and the execution projection always read the same way.
func groupByStateBoard(items []BoardItem) BoardColumns {
	order := conformance.DomainValues(conformance.DomainExecutionState, "sto")
	cols := make(BoardColumns, 0, len(order))
	for _, state := range order {
		col := BoardColumn{State: state}
		for _, bi := range items {
			if bi.State == state {
				col.WorkItems = append(col.WorkItems, bi)
			}
		}
		cols = append(cols, col)
	}
	return cols
}

// Count returns the number of board items in the given state across
// the columns (0 when the state has no column). Mirrors
// StateColumns.Count so both boards read the same way.
func (c BoardColumns) Count(state string) int {
	for _, col := range c {
		if col.State == state {
			return len(col.WorkItems)
		}
	}
	return 0
}
