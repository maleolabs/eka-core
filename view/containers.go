package view

import "sort"

// This file implements the containers projection: every execution
// container line of the repository — active and completed — as a
// table-shaped summary: identity, plan, work items and tickets,
// started/ended dates and the container state. Where the execution
// projection answers "what is currently being worked on?" (one active
// container), containers answers "what containers exist, and what do
// they hold?". The CLI renders it as an aligned table and applies the
// retrieval filters and page window (the projection keeps its full
// totals).
//
// Derivation (see package doc for the membership rule): each container
// line comes from the graph's line index (deduplicated per identity
// line, highest instance); its tickets and work items are the
// membership helpers; its plan and lifecycle dates come from the
// container unit itself. Containers are ordered by started date
// ascending ("" first — deterministic, tie-broken by canonical
// identity) so the table reads oldest wave first; machine retrieval
// (`eka get containers`) keeps canonical ordering.

// ContainerSummary is one execution container line of the containers
// projection.
type ContainerSummary struct {
	// Identity is the canonical line form "<ns>/ctr:<id>".
	Identity string
	Type     string
	ID       string
	// Plan is the first depends-on target rendered in the authoring
	// reference convention ("" when the container carries none).
	Plan string
	// Items is the number of work items of the container
	// (deduplicated by identity line).
	Items int
	// Tickets is the number of tkt- units deriving from the container.
	Tickets int
	// StartedAt is the container line's created date.
	StartedAt string
	// EndedAt is the completion date (container-state
	// active -> completed); "" while active.
	EndedAt string
	// State is the container-state value ("active" or "completed").
	State string
}

// ContainersProjection is the repository-wide containers view.
type ContainersProjection struct {
	// Containers are the container lines, ordered by started date
	// ascending ("" first — deterministic; tie-broken by canonical
	// identity). The CLI narrows this slice with its filters and page
	// window; Total and Active keep the full population.
	Containers []ContainerSummary
	// Total is the number of containers before any page window.
	Total int
	// Active is the number of containers with State == "active".
	Active int
}

// Name returns the registry name of the projection.
func (p *ContainersProjection) Name() string { return "containers" }

func buildContainers(g *Graph, _ string) (Projection, error) {
	p := &ContainersProjection{}
	details := g.ContainersDetailed()
	p.Containers = make([]ContainerSummary, 0, len(details))
	for _, d := range details {
		summary := ContainerSummary{
			Identity:  d.Identity,
			Type:      d.Type,
			ID:        d.ID,
			Plan:      d.Plan,
			StartedAt: d.Created,
			EndedAt:   d.EndedAt,
			State:     d.State,
		}
		summary.Items = len(g.WorkItemsForContainer(d.Identity))
		summary.Tickets = len(g.TicketsForContainer(d.Identity))
		if d.State == "active" {
			p.Active++
		}
		p.Containers = append(p.Containers, summary)
	}
	// Display order: started date ascending ("" first, deterministic),
	// tie-broken by canonical identity — the table reads oldest wave
	// first.
	sort.SliceStable(p.Containers, func(i, j int) bool {
		if p.Containers[i].StartedAt != p.Containers[j].StartedAt {
			return p.Containers[i].StartedAt < p.Containers[j].StartedAt
		}
		return p.Containers[i].Identity < p.Containers[j].Identity
	})
	p.Total = len(p.Containers)
	return p, nil
}

// Page windows the Containers slice to [offset, offset+limit). Total
// and Active stay the full population — the renderer reads the footer
// totals from the untouched projection.
func (p *ContainersProjection) Page(offset, limit int) {
	page := NewPage(offset, limit)
	start, end := page.Window(len(p.Containers))
	p.Containers = p.Containers[start:end]
}
