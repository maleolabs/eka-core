package view

import "sort"

// This file implements the execution projection: the canonical view of
// the Execution domain over the active execution container — the
// container itself, its tickets with the status projected from their
// work items, and its work items grouped by execution state. It merges
// the former sprint (work items by state) and wave (tickets and
// progress) projections; "sprint" and "wave" remain registered as
// aliases that resolve here, producing identical output.
//
// Derivation (see package doc for the membership rule):
//   - Target: the ACTIVE Execution Container (container-state "active";
//     exactly-one-active per protocol). No active container yields an
//     empty projection — a valid state, not an error. Several active
//     containers (invalid state) resolve to the lexicographically
//     smallest canonical identity, reported through MultipleActive.
//   - Tickets: the tickets (tkt-) whose derives-from includes the
//     active container's identity line, sorted by canonical identity,
//     each carrying the status projected from its referenced work item
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
// container.
type ExecutionProjection struct {
	// Container is the active container, or nil when no container is
	// active.
	Container *Container
	// MultipleActive reports the invalid state where several containers
	// are active; Container then holds the lexicographically smallest
	// identity.
	MultipleActive bool
	// Tickets are the tickets deriving from the active container,
	// sorted by canonical identity, each carrying its projected status.
	Tickets []Ticket
	// Columns are the fixed execution-state columns (always the full
	// six-column set — canceled added, ADR-019 — with zero-item
	// columns when empty).
	Columns StateColumns
	// Total is the number of work items placed in the columns.
	Total int
}

// Name returns the registry name of the projection.
func (p *ExecutionProjection) Name() string { return "execution" }

func buildExecution(g *Graph, target string) (Projection, error) {
	container, multiple := g.ActiveContainer()
	p := &ExecutionProjection{MultipleActive: multiple}
	if container == nil {
		// Empty projection: no active container. The columns keep the
		// fixed order so the projection stays shape-stable.
		p.Columns = groupByState(nil)
		return p, nil
	}
	p.Container = container
	p.Tickets = g.TicketsForContainer(container.Identity)
	items := g.WorkItemsForContainer(container.Identity)
	// Display order: created date ascending ("" first, deterministic),
	// tie-broken by canonical identity — the board reads oldest item
	// first. Machine retrieval keeps the canonical identity order.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Created != items[j].Created {
			return items[i].Created < items[j].Created
		}
		return items[i].Identity < items[j].Identity
	})
	// Every item carries its note count (the published notes
	// discussing it) — the container board shows the counts.
	for i := range items {
		items[i].NotesCount = len(g.NotesFor(items[i].Identity))
	}
	p.Columns = groupByState(items)
	for _, col := range p.Columns {
		p.Total += len(col.WorkItems)
	}
	return p, nil
}
