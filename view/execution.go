package view

import "sort"

// This file implements the execution projection: the canonical view of
// the Execution domain over the ACTIVE execution container(s) — the
// containers themselves, their tickets with the status projected from
// their work items, and their work items grouped by execution state.
// It merges the former sprint (work items by state) and wave (tickets
// and progress) projections; "sprint" and "wave" remain registered as
// aliases that resolve here, producing identical output.
//
// Derivation (see package doc for the membership rule):
//   - Target: the ACTIVE Execution Containers (container-state "active";
//     one-active-per-source_repo, dec:parallel-container-execution). No
//     active container yields an empty projection — a valid state, not
//     an error. Several active containers (the valid parallel state)
//     are all reported: Container holds the lexicographically smallest
//     canonical identity (backward-compatible pick), the Actives list
//     holds every active container, and Boards carries one per-active
//     board (with its tickets and columns) for the multi-board render.
//   - Tickets: the tickets (tkt-) whose derives-from includes the
//     container's identity line, sorted by canonical identity, each
//     carrying the status projected from its referenced work item
//     ("unresolved" when the work item does not resolve).
//   - Members: the work items referenced by those tickets,
//     deduplicated by identity line, ordered by created date
//     ascending ("" first — deterministic; tie-broken by canonical
//     identity) so the board reads oldest item first. Machine
//     retrieval (graph.WorkItemsForContainer, `eka get`) keeps
//     canonical ordering.
//   - Grouping: fixed execution-state column order planned, todo,
//     in-progress, in-review, done, canceled (ADR-019).
//
// The execution projection ignores the optional target argument.

// ExecutionProjection is the Execution domain view over the active
// container(s).
type ExecutionProjection struct {
	// Container is the active container with the lexicographically
	// smallest canonical identity, or nil when no container is active.
	Container *Container
	// MultipleActive reports whether more than one container is
	// active (the valid parallel state, dec:parallel-container-
	// execution). Container then holds the smallest identity and
	// Boards carries one board per active container.
	MultipleActive bool
	// Actives holds EVERY active container, sorted by canonical
	// identity — the multi-active projection surface (one board per
	// active container is a rendering concern; the data lives here
	// and in Boards).
	Actives []Container
	// Tickets are the tickets deriving from the primary active
	// container (Container), sorted by canonical identity, each
	// carrying its projected status.
	Tickets []Ticket
	// Columns are the fixed execution-state columns of the primary
	// active container (always the full six-column set — canceled
	// added, ADR-019 — with zero-item columns when empty).
	Columns StateColumns
	// Total is the number of work items placed in the columns of the
	// primary active container.
	Total int
	// Boards carries one per-active-container board when MultipleActive
	// (and remains empty otherwise — the single-active render reads
	// Container/Tickets/Columns directly). Each board repeats the
	// per-container derivation so the multi-active render needs no
	// graph re-query.
	Boards []ExecutionBoard
}

// ExecutionBoard is one active container's execution projection — the
// per-container slice of a multi-active projection (one board per
// active container plus a containers summary, dec:parallel-container-
// execution).
type ExecutionBoard struct {
	// Container is the board's active container.
	Container Container
	// Tickets derive from Container, sorted by canonical identity.
	Tickets []Ticket
	// Columns hold Container's work items grouped by execution state.
	Columns StateColumns
	// Total is the number of work items placed in Columns.
	Total int
}

// Name returns the registry name of the projection.
func (p *ExecutionProjection) Name() string { return "execution" }

func buildExecution(g *Graph, target string) (Projection, error) {
	actives := g.ActiveContainers()
	p := &ExecutionProjection{Actives: actives}
	p.MultipleActive = len(actives) > 1
	if len(actives) == 0 {
		// Empty projection: no active container. The columns keep the
		// fixed order so the projection stays shape-stable.
		p.Columns = groupByState(nil)
		return p, nil
	}
	p.Container = &actives[0]
	p.Tickets = g.TicketsForContainer(p.Container.Identity)
	p.Columns, p.Total = executionColumns(g, p.Container.Identity)
	if p.MultipleActive {
		// One board per active container (plus the containers summary
		// the renderer draws from Actives).
		p.Boards = make([]ExecutionBoard, 0, len(actives))
		for _, c := range actives {
			cols, total := executionColumns(g, c.Identity)
			p.Boards = append(p.Boards, ExecutionBoard{
				Container: c,
				Tickets:   g.TicketsForContainer(c.Identity),
				Columns:   cols,
				Total:     total,
			})
		}
	}
	return p, nil
}

// executionColumns derives one container's work items grouped by
// execution state (display order: created date ascending — "" first,
// deterministic — tie-broken by canonical identity; every item carries
// its published-note count).
func executionColumns(g *Graph, container string) (StateColumns, int) {
	items := g.WorkItemsForContainer(container)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Created != items[j].Created {
			return items[i].Created < items[j].Created
		}
		return items[i].Identity < items[j].Identity
	})
	for i := range items {
		items[i].NotesCount = len(g.NotesFor(items[i].Identity))
	}
	cols := groupByState(items)
	total := 0
	for _, col := range cols {
		total += len(col.WorkItems)
	}
	return cols, total
}
