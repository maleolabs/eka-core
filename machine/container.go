package machine

import (
	"sort"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the containers collection of the machine
// interface: the "containers" query of `eka get`. A container is one
// execution container (ctr-) LINE of the project — deduplicated by
// (namespace, type, id), highest instance-version wins (mirroring the
// view.byForm line semantics — the latest knowledge version of the
// line, ADR-025) — projected with its plan, its work
// items and tickets, and its lifecycle dates.
//
// Derivation (relationship-only, never file text; membership follows
// the projection engine's resolution semantics — a derives-from target
// counts only when it RESOLVES against the unit set, a versioned target
// to its exact instance, a line target to the line's highest instance;
// targets that parse but do not resolve never match):
//   - tickets: the tkt- units whose FIRST resolvable derives-from ctr-
//     target is the container line (a ticket belongs to at most one
//     container — the first resolvable ctr- reference in stored order,
//     the same rule as the board's membership derivation);
//   - items: the work items referenced by those tickets' derives-from,
//     deduplicated by line (the first RESOLVABLE derives-from target
//     whose type owns the Execution State domain — the same work-item
//     characterization and resolution as the projection engine's
//     membership rule);
//   - plan: the first depends-on target, stored form verbatim;
//   - startedAt: the container unit's Created date;
//   - endedAt: the change-log date of the container-state
//     active -> completed transition ("" while active);
//   - containerState: the stored container-state value.
//
// Review units (rvw-) are never part of the derivation: the containers
// query is fed the Execution-domain units, which rvw- does not belong
// to.

// Container is one execution container line of the project.
type Container struct {
	// CanonicalForm is the canonical LINE identity form
	// ("<namespace>/ctr:<id>") — the line-level identity, never an
	// instance-version (the collection is deduplicated per line).
	CanonicalForm string `json:"canonicalForm"`
	// ID is the bare container id.
	ID string `json:"id"`
	// Plan is the first depends-on target of the container line, stored
	// form verbatim (e.g. "feather/plan:roadmap-v1:1"); "" when the
	// container carries no depends-on relationship.
	Plan string `json:"plan,omitempty"`
	// Items is the number of work items of the container: the work
	// items referenced by the container's tickets' derives-from,
	// deduplicated by identity line.
	Items int `json:"items"`
	// Tickets is the number of tkt- units deriving from the container
	// line.
	Tickets int `json:"tickets"`
	// StartedAt is the container unit's Created date.
	StartedAt string `json:"startedAt,omitempty"`
	// EndedAt is the change-log date of the container-state
	// active -> completed transition; "" while the container is active.
	EndedAt string `json:"endedAt,omitempty"`
	// ContainerState is the stored container-state value ("active" or
	// "completed").
	ContainerState string `json:"containerState"`
}

// ContainerCollection is the machine projection of the containers
// query: every execution container line of the project, sorted by
// canonical form.
type ContainerCollection struct {
	Schema     string      `json:"schema"`
	Collection string      `json:"collection"` // "containers"
	Count      int         `json:"count"`      // containers matching the retrieval, before any page window
	Containers []Container `json:"containers"` // sorted by canonical form
	// Pagination is nil unless a page window was applied (Page).
	Pagination *Pagination `json:"pagination,omitempty"`
}

// NewContainerCollection maps the units of the containers query (the
// project's Execution-domain units) to a machine ContainerCollection.
// The containers are sorted by canonical form regardless of the input
// order (determinism contract); an empty result is an empty container
// list, never null. Count carries the container population of the
// collection as built — the retrieval filters (FilterActive,
// FilterContainer) narrow Count to their result set, and Page keeps
// that total (the size BEFORE the window), so count always equals
// pagination.total when both are present.
func NewContainerCollection(units []*exchange.Unit) (*ContainerCollection, error) {
	// Deduplicate the container lines: the highest instance-version of
	// each (namespace, type, id) line wins (mirror of the projection
	// engine's byForm semantics — the latest knowledge version of the
	// line, ADR-025).
	highest := make(map[string]*exchange.Unit)
	for _, u := range units {
		if u.Identity.Type != "ctr" {
			continue
		}
		key := containerLineKey(u.Identity.Namespace, u.Identity.ID)
		cur, ok := highest[key]
		if !ok || u.Identity.InstanceVersion > cur.Identity.InstanceVersion {
			highest[key] = u
		}
	}
	lines := make([]*exchange.Unit, 0, len(highest))
	for _, u := range highest {
		lines = append(lines, u)
	}
	// Canonical-form order is the collection contract.
	sort.Slice(lines, func(i, j int) bool {
		return containerLineForm(lines[i]) < containerLineForm(lines[j])
	})
	// The resolution index over the full unit set: the same line/instance
	// resolution semantics as the projection engine (view.Graph.Resolve,
	// the validator's Rule 5), so membership can never drift between
	// `eka get containers` and `eka view`.
	idx := newLineIndex(units)
	containers := make([]Container, 0, len(lines))
	for _, u := range lines {
		containers = append(containers, containerFrom(u, idx))
	}
	return &ContainerCollection{
		Schema:     Schema,
		Collection: "containers",
		Count:      len(containers),
		Containers: containers,
	}, nil
}

// FilterActive keeps only the containers whose container-state is
// "active" (the --active/--current retrieval filter). Count narrows
// to the filtered population (the total BEFORE any page window) —
// consistent with the domain collection, where count is the size of
// the matching set; Page then keeps that total.
func (c *ContainerCollection) FilterActive() {
	out := c.Containers[:0]
	for _, ct := range c.Containers {
		if ct.ContainerState == "active" {
			out = append(out, ct)
		}
	}
	c.Containers = out
	c.Count = len(out)
}

// FilterContainer keeps only the container whose canonical form
// matches form (the --container retrieval filter). It returns false
// when no container matches — callers then surface a usage error
// listing the available forms. Count narrows to the filtered
// population (1 when found), like FilterActive.
func (c *ContainerCollection) FilterContainer(form string) bool {
	for _, ct := range c.Containers {
		if ct.CanonicalForm == form {
			c.Containers = []Container{ct}
			c.Count = 1
			return true
		}
	}
	return false
}

// Page windows the Containers slice to [offset, offset+limit), keeping
// Count as the TOTAL container count and setting the Pagination
// metadata (nil before the first window — the default output stays
// byte-identical to the unpaged schema). A limit of 0 (--offset given
// without --limit) windows to the end of the list.
func (c *ContainerCollection) Page(offset, limit int) {
	total := len(c.Containers)
	start := offset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := total
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	c.Containers = c.Containers[start:end]
	c.Pagination = paginationOf(offset, limit, total)
}

// Marshal serializes the ContainerCollection deterministically: the
// same formatting as Document.Marshal (two-space indent, trailing
// newline).
func (c *ContainerCollection) Marshal() ([]byte, error) {
	return marshal(c, false)
}

// MarshalCompact serializes the ContainerCollection as a single JSON
// line plus a single trailing newline — the compact form of the same
// deterministic collection.
func (c *ContainerCollection) MarshalCompact() ([]byte, error) {
	return marshal(c, true)
}

// containerLineKey is the dedup key of one container line.
func containerLineKey(ns, id string) string {
	return ns + "\x00ctr\x00" + id
}

// containerLineForm renders the canonical line identity form of a
// container unit.
func containerLineForm(u *exchange.Unit) string {
	return u.Identity.Namespace + "/" + u.Identity.Type + ":" + u.Identity.ID
}

// containerFrom projects one container line (the highest instance) over
// the resolution index: plan, tickets, items, lifecycle dates and
// state. A ticket belongs to the container iff its first RESOLVABLE
// derives-from ctr- target is the container's line; its work item is
// the first RESOLVABLE derives-from target whose type owns the
// Execution State domain.
func containerFrom(u *exchange.Unit, idx *lineIndex) Container {
	c := Container{
		CanonicalForm:  containerLineForm(u),
		ID:             u.Identity.ID,
		StartedAt:      u.Created,
		ContainerState: u.StateVector.ContainerState,
	}
	for _, r := range u.Relationships {
		if r.Type == "depends-on" {
			c.Plan = r.Target // stored form verbatim
			break
		}
	}
	seen := make(map[string]bool)
	for _, t := range idx.units {
		if t.Identity.Type != "tkt" {
			continue
		}
		container, workItem := ticketTargets(t, idx)
		if container == nil ||
			container.Identity.Namespace != u.Identity.Namespace ||
			container.Identity.ID != u.Identity.ID {
			continue // the ticket's resolved container is another line
		}
		c.Tickets++
		if workItem == nil {
			continue // tickets without a resolvable work item contribute nothing
		}
		key := workItem.Identity.Namespace + "\x00" + workItem.Identity.Type + "\x00" + workItem.Identity.ID
		if !seen[key] {
			seen[key] = true
			c.Items++
		}
	}
	for _, e := range u.ChangeLog {
		if e.Domain == conformance.DomainContainerState && e.From == "active" && e.To == "completed" {
			c.EndedAt = e.Date
			break
		}
	}
	return c
}

// lineIndex is the machine's resolution index over a unit set: units
// grouped by identity line, each bucket ordered by instance-version
// ascending. Resolution matches the projection engine (view.Graph.Resolve
// — itself the Runtime Resolver semantics): a versioned reference
// resolves to the exact instance, a line reference to the line's highest
// instance; a reference to an absent line or instance resolves to nil.
type lineIndex struct {
	units  []*exchange.Unit
	byLine map[string][]*exchange.Unit
}

// newLineIndex builds the index over the given unit set. The slice is
// kept by reference (units are immutable payloads, like the graph).
func newLineIndex(units []*exchange.Unit) *lineIndex {
	idx := &lineIndex{units: units, byLine: make(map[string][]*exchange.Unit, len(units))}
	for _, u := range units {
		key := unitLineKey(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
		idx.byLine[key] = append(idx.byLine[key], u)
	}
	for _, bucket := range idx.byLine {
		sort.Slice(bucket, func(i, j int) bool {
			return bucket[i].Identity.InstanceVersion < bucket[j].Identity.InstanceVersion
		})
	}
	return idx
}

// resolve resolves a parsed reference to its target unit, or nil when
// the reference does not resolve. A versioned reference resolves to the
// exact instance, a line reference to the line's highest instance (the
// latest knowledge version — mirroring the Runtime Resolver and the
// projection engine, ADR-025).
func (idx *lineIndex) resolve(ref conformance.Reference) *exchange.Unit {
	bucket := idx.byLine[unitLineKey(ref.Namespace, ref.Type, ref.ID)]
	if len(bucket) == 0 {
		return nil
	}
	if ref.HasVersion {
		for _, u := range bucket {
			if u.Identity.InstanceVersion == ref.Version {
				return u
			}
		}
		return nil
	}
	return bucket[len(bucket)-1]
}

// unitLineKey is the index key of one identity line. The \x00
// separators cannot collide with the identity components (namespaces,
// type tokens and ids may contain hyphens but not colons, slashes or
// NULs).
func unitLineKey(ns, typeToken, id string) string {
	return ns + "\x00" + typeToken + "\x00" + id
}

// ticketTargets resolves the container and the work item a ticket
// derives from, scanning the ticket's derives-from targets in stored
// order — the same first-resolvable-wins rule as the projection engine
// (view.Graph.ticketTargets): the first RESOLVABLE ctr- reference is
// the container, the first resolvable execution-state-owning reference
// is the work item. References that parse but do not resolve (for
// example a versioned target whose instance is absent from the set)
// never match.
func ticketTargets(t *exchange.Unit, idx *lineIndex) (container, workItem *exchange.Unit) {
	for _, r := range t.Relationships {
		if r.Type != "derives-from" {
			continue
		}
		ref, err := conformance.ParseReference(r.Target, t.Identity.Namespace, t.Identity.Type)
		if err != nil {
			continue // malformed references are reported by Rule 5
		}
		target := idx.resolve(ref)
		if target == nil {
			continue // parses but does not resolve — never a membership
		}
		switch {
		case container == nil && target.Identity.Type == "ctr":
			container = target
		case workItem == nil && ownsExecutionState(target.Identity.Type):
			workItem = target
		}
	}
	return container, workItem
}

// ownsExecutionState reports whether a type token owns the Execution
// State domain — the work-item characterization shared with the
// projection engine's membership rule.
func ownsExecutionState(token string) bool {
	for _, d := range conformance.OwnedDomains(token) {
		if d == conformance.DomainExecutionState {
			return true
		}
	}
	return false
}

// paginationOf builds the Pagination metadata of a window over a list
// of total items: 1-based page (offset/limit+1 when limit > 0, else
// 1) and page count (ceil(total/limit); 0 when total == 0).
func paginationOf(offset, limit, total int) *Pagination {
	page, pages := 1, 0
	if limit > 0 {
		page = offset/limit + 1
		pages = (total + limit - 1) / limit
	}
	return &Pagination{Offset: offset, Limit: limit, Page: page, Total: total, Pages: pages}
}
