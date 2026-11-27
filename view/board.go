package view

import (
	"sort"

	"github.com/maleolabs/eka-core/v2/conformance"
)

// This file implements the board projection: the canonical view of ALL
// work items in the repository — across every execution container
// (active and completed) and outside any container — on the fixed
// execution-state board. Where the execution projection answers "what
// is currently being worked on?" (one active container), the board
// answers "what is the total work in the repository?".
//
// Derivation (see package doc for the membership rule):
//   - Items: every work item line in the repository whose type owns the
//     Execution State domain (g.WorkItems), deduplicated by identity
//     line and sorted by canonical identity.
//   - Container tags: for each item, the canonical identities of the
//     containers whose tickets reference it (g.ContainersForWorkItem),
//     sorted. An item without any referencing container is unassigned.
//   - Ordering: within a column, items sort by the first referencing
//     container ("" for unassigned — natural ascending puts unassigned
//     items first), then created date ascending ("" first,
//     deterministic), then canonical identity. Machine retrieval
//     (graph.WorkItems, `eka get`) keeps canonical ordering.
//   - Grouping: the fixed execution-state column order planned, todo,
//     in-progress, in-review, done, canceled (ADR-019) — identical to
//     the execution projection.
//
// The board projection ignores the optional target argument.

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
	// Total is the number of work items placed in the columns.
	Total int
	// Unassigned is the number of work items not referenced by any
	// ticket container.
	Unassigned int
	// ContainerCount is the number of distinct containers that
	// reference at least one board item.
	ContainerCount int
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

// boardItems tags work items with their referencing containers, counts
// the container-unassigned items and the distinct referencing
// containers, and orders the items by the board's display key: the
// first referencing container ("" for unassigned — natural ascending
// puts unassigned items first), then created date ascending, then
// canonical identity.
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
// StateColumns.Count.
func (c BoardColumns) Count(state string) int {
	for _, col := range c {
		if col.State == state {
			return len(col.WorkItems)
		}
	}
	return 0
}
