// Package view implements the EKA Knowledge Projection Engine: the
// application-layer views over the Engineering Knowledge Model that the
// `eka view` command renders.
//
// The engine is pure data in, pure data out. It contains no terminal
// knowledge, no command framework, and no output: it reads one
// Canonical Knowledge Object set (exchange.Units — the decoded units
// of the EKA workspace canonical store, store.UnitsByProject) and
// produces projection models. Every projection is derived exclusively
// from the Engineering Knowledge Model (unit identity, state vector,
// relationships) — never from markdown content. The engine is
// synchronous and stateless: one graph build per read, one projection
// build per view, so a loading state can wrap the whole call without
// restructuring.
//
// The engine is representation-independent by construction: it consumes
// CKOs (exchange.Units), never authoring syntax. The Markdown adapter
// (conformance) lives upstream of the Knowledge Compiler (compile),
// which runs inside the sync engine only; ontology helpers shared with
// the authoring layer (ParseReference, DomainValues, OwnedDomains,
// DomainForToken, Stratum) are representation-independent model
// functions and stay in use here.
//
// Determinism contract: all ordering is canonical. Units are ordered
// by their canonical line identity form (<namespace>/<type>:<id>) and
// instance-version; execution-state columns always follow the fixed
// value order planned, todo, in-progress, in-review, done; tickets keep
// their derives-from references in stored (type, target) order. There
// are no maps in output ordering and no time-dependent values.
//
// Membership derivation (the single source of membership — relationships
// only, never file text):
//
//	A work item is a member of an execution container C iff a ticket
//	(tkt-) whose derives-from resolves to C's identity line also
//	resolves to the work item's identity line. A ticket belongs to C iff
//	one of its derives-from references resolves to C. Work items are
//	identified by their type owning the Execution State domain
//	(conformance.OwnedDomains); the ticket itself is never parsed beyond
//	its relationships.
//
// Assignment derivation (ADR-029): a work item's assignee is the member
// (mbr-) line its assigned-to relationship resolves to; member-scoped
// views (WorkItemsForMember, the member board) derive membership from
// the assigned-to edge only, and work items without the edge surface in
// the dedicated 'No assignee' bucket — never silently excluded.
package view

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// LineForm renders the canonical line identity form of a unit:
// "<namespace>/<type>:<id>". It identifies a unit line (all
// instance-versions) and is the ordering key used throughout the
// package.
func LineForm(ns, typeToken, id string) string {
	return ns + "/" + typeToken + ":" + id
}

// lineKey is the internal index key for an identity line. The \x00
// separators cannot collide with the identity components (namespaces,
// type tokens and ids may contain hyphens but not colons, slashes or
// NULs).
func lineKey(ns, typeToken, id string) string {
	return ns + "\x00" + typeToken + "\x00" + id
}

// Graph is the Knowledge Graph built from one compiled CKO set: an
// identity index (canonical line form -> unit), instance lines
// (grouped by identity, ordered by instance-version), relationship
// resolution (reference -> unit), and the membership helpers used by
// the projections. It is immutable after construction.
type Graph struct {
	root  string
	units []*exchange.Unit
	// byLine indexes units by identity line; each bucket holds every
	// instance sorted by instance-version (line resolution returns the
	// highest instance — the latest knowledge version; the validator's
	// Rule 5 stays an existence check and never selects an instance).
	byLine map[string][]*exchange.Unit
	// byForm maps the canonical line identity form to the line's highest
	// instance (the line-level resolution target).
	byForm map[string]*exchange.Unit
	// byType maps type tokens to their unit lines, sorted by
	// canonical identity form (then instance-version, for robustness on
	// non-conformant input).
	byType  map[string][]*exchange.Unit
	numbers map[string]int
}

// NewGraph builds the Knowledge Graph from a compiled CKO set. The
// unit slice is copied; the graph never aliases caller-owned memory
// beyond the unit pointers themselves (units are immutable payloads).
func NewGraph(root string, units []*exchange.Unit) *Graph {
	g := &Graph{
		root:    root,
		units:   units,
		byLine:  make(map[string][]*exchange.Unit, len(units)),
		byForm:  make(map[string]*exchange.Unit, len(units)),
		byType:  make(map[string][]*exchange.Unit),
		numbers: map[string]int{},
	}
	for _, u := range g.units {
		key := lineKey(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
		g.byLine[key] = append(g.byLine[key], u)
		g.byType[u.Identity.Type] = append(g.byType[u.Identity.Type], u)
	}
	for _, bucket := range g.byLine {
		sort.Slice(bucket, func(i, j int) bool {
			return bucket[i].Identity.InstanceVersion < bucket[j].Identity.InstanceVersion
		})
		last := bucket[len(bucket)-1]
		g.byForm[LineForm(last.Identity.Namespace, last.Identity.Type, last.Identity.ID)] = last
	}
	for _, bucket := range g.byType {
		sort.Slice(bucket, func(i, j int) bool {
			a, b := LineForm(bucket[i].Identity.Namespace, bucket[i].Identity.Type, bucket[i].Identity.ID),
				LineForm(bucket[j].Identity.Namespace, bucket[j].Identity.Type, bucket[j].Identity.ID)
			if a != b {
				return a < b
			}
			return bucket[i].Identity.InstanceVersion < bucket[j].Identity.InstanceVersion
		})
	}
	return g
}

// Root returns the compile root the graph was built from.
func (g *Graph) Root() string { return g.root }

// Units returns every unit of the graph, sorted by canonical identity
// form (then instance-version).
func (g *Graph) Units() []*exchange.Unit {
	out := make([]*exchange.Unit, len(g.units))
	copy(out, g.units)
	sort.Slice(out, func(i, j int) bool {
		a, b := LineForm(out[i].Identity.Namespace, out[i].Identity.Type, out[i].Identity.ID),
			LineForm(out[j].Identity.Namespace, out[j].Identity.Type, out[j].Identity.ID)
		if a != b {
			return a < b
		}
		return out[i].Identity.InstanceVersion < out[j].Identity.InstanceVersion
	})
	return out
}

// ByLineForm resolves a canonical line identity form to the line's
// highest instance, or nil when the line is not in the graph.
func (g *Graph) ByLineForm(form string) *exchange.Unit {
	return g.byForm[form]
}

// unitsOfType returns the LINE-LEVEL units of one type token: the
// highest (latest) instance of every identity line of that type,
// sorted by canonical identity form (ADR-025 — one unit per line, the
// latest knowledge version, never a superseded revision). This is the
// deterministic line index the byType buckets exist to serve; direct
// byType iteration would hand out every instance (duplicates per line)
// and its first element would be the line's OLDEST instance.
func (g *Graph) unitsOfType(typeToken string) []*exchange.Unit {
	out := make([]*exchange.Unit, 0, len(g.byForm))
	for _, u := range g.byForm {
		if u.Identity.Type == typeToken {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return LineForm(out[i].Identity.Namespace, out[i].Identity.Type, out[i].Identity.ID) <
			LineForm(out[j].Identity.Namespace, out[j].Identity.Type, out[j].Identity.ID)
	})
	return out
}

// Resolve resolves a parsed reference to its target unit: a versioned
// reference resolves to the exact instance, a line reference to the
// highest instance (the latest knowledge version). It returns nil when
// the reference does not resolve. Line-form semantics mirror the
// Runtime Resolver; the validator's Rule 5 conformance check remains
// an existence check on the line and never selects an instance.
func (g *Graph) Resolve(ref conformance.Reference) *exchange.Unit {
	bucket := g.byLine[lineKey(ref.Namespace, ref.Type, ref.ID)]
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

// relsOf returns the targets of a unit's relationships of one type, in
// stored (type, target) order — the CKO replacement of the authoring
// layer's per-field relationship map. Targets are rendered in the
// authoring reference convention: same-namespace targets as line
// references ("<type>:<id>"), with the instance version appended
// exactly when the target is NOT the highest instance of its line —
// omitting it then would change resolution (an unversioned reference
// resolves to the highest instance; a versioned one to the exact
// instance), so the rendering is lossless for resolution by
// construction. Cross-namespace targets render in the full canonical
// form.
func (g *Graph) relsOf(u *exchange.Unit, relType string) []string {
	var out []string
	for _, r := range u.Relationships {
		if r.Type != relType {
			continue
		}
		out = append(out, g.referenceForm(u, r.Target))
	}
	return out
}

// Rels is the exported form of relsOf for the CLI renderer layer: the
// projections and the renderers share the same reference-form
// rendering, so relationship presentation is consistent across the
// whole view stack.
func (g *Graph) Rels(u *exchange.Unit, relType string) []string { return g.relsOf(u, relType) }

// derivesFromForms returns the canonical LINE identity forms of the
// unit's derives-from relationship targets — the same reference
// resolution relsOf renders, projected to the line level
// ("<ns>/<type>:<id>", no instance version) so each entry compares
// directly against identity lines. The planning projection uses them
// to group scope definitions and epics under the plan they derive
// from: a child line's derives-from contains its plan's identity.
// Unparsable targets (impossible behind the validation gate) are
// skipped defensively.
func (g *Graph) derivesFromForms(u *exchange.Unit) []string {
	var out []string
	for _, r := range u.Relationships {
		if r.Type != "derives-from" {
			continue
		}
		ref, err := conformance.ParseReference(r.Target, u.Identity.Namespace, u.Identity.Type)
		if err != nil {
			continue
		}
		out = append(out, LineForm(ref.Namespace, ref.Type, ref.ID))
	}
	return out
}

// referenceForm renders one relationship target in the authoring
// reference convention of the unit's namespace: same-namespace targets
// render as "<type>:<id>" line forms, appending the instance version
// ("<type>:<id>:<v>") exactly when the target is not the line's highest
// instance (see relsOf); cross-namespace targets render in the full
// canonical form (namespace included).
func (g *Graph) referenceForm(u *exchange.Unit, target string) string {
	ref, err := conformance.ParseReference(target, u.Identity.Namespace, u.Identity.Type)
	if err != nil {
		return target // Defensive: canonical targets always parse.
	}
	if ref.Namespace != u.Identity.Namespace {
		return target
	}
	form := ref.Type + ":" + ref.ID
	// The version suffix is appended ONLY to explicit versioned
	// targets that are not the line's highest instance. An unversioned
	// target must never gain a suffix: ParseReference reports
	// Version = 0 for it, and a naive comparison against the line's
	// highest instance would render "type:id:0" — a versioned
	// reference that can never resolve (instance 0 does not exist).
	if ref.HasVersion && g.highestInstance(ref) != ref.Version {
		form += ":" + strconv.Itoa(ref.Version)
	}
	return form
}

// highestInstance returns the instance version of the line's highest
// instance (0 when the line has no units — the caller then never
// reaches the version comparison with a meaningful target).
func (g *Graph) highestInstance(ref conformance.Reference) int {
	bucket := g.byLine[lineKey(ref.Namespace, ref.Type, ref.ID)]
	if len(bucket) == 0 {
		return 0
	}
	return bucket[len(bucket)-1].Identity.InstanceVersion
}

// Container is one execution container (ctr-) line in the graph.
type Container struct {
	// Identity is the canonical line identity form.
	Identity string
	Type     string
	ID       string
	// State is the container-state value ("active" or "completed").
	State string
}

func containerFor(u *exchange.Unit) *Container {
	return &Container{
		Identity: LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID),
		Type:     u.Identity.Type,
		ID:       u.Identity.ID,
		State:    u.StateVector.ContainerState,
	}
}

// ActiveContainer returns the single active execution container per the
// exactly-one-active protocol. It returns nil when no container is
// active (a valid, empty projection state). When the repository is in an
// invalid state — several containers with container-state "active" — it
// returns the lexicographically smallest canonical identity and reports
// the anomaly through multiple.
func (g *Graph) ActiveContainer() (*Container, bool) {
	// Line-level scan: the highest instance of every ctr- line decides
	// the current container-state (ADR-025) — a completed revision
	// supersedes an earlier active one.
	var active []*exchange.Unit
	for _, u := range g.byForm {
		if u.Identity.Type != "ctr" || u.StateVector.ContainerState != "active" {
			continue
		}
		active = append(active, u)
	}
	if len(active) == 0 {
		return nil, false
	}
	// Deterministic pick: the lexicographically smallest canonical
	// identity (the byForm iteration order of a map is not
	// deterministic).
	sort.Slice(active, func(i, j int) bool {
		return LineForm(active[i].Identity.Namespace, active[i].Identity.Type, active[i].Identity.ID) <
			LineForm(active[j].Identity.Namespace, active[j].Identity.Type, active[j].Identity.ID)
	})
	return containerFor(active[0]), len(active) > 1
}

// Containers returns every execution container LINE — the highest
// instance of each (namespace, type, id) line, deduplicated by
// canonical identity (ADR-025) — sorted by canonical identity form.
func (g *Graph) Containers() []Container {
	out := make([]Container, 0, len(g.byForm))
	for _, u := range g.byForm {
		if u.Identity.Type != "ctr" {
			continue
		}
		out = append(out, *containerFor(u))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out
}

// ContainerDetail is one execution container line with the lifecycle
// and dependency detail the containers projection renders: the
// canonical identity, the first depends-on target (rendered in the
// authoring reference convention), the created/updated dates and the
// container completion date.
type ContainerDetail struct {
	Identity string // canonical line identity form
	Type     string
	ID       string
	// State is the container-state value ("active" or "completed").
	State string
	// Plan is the first depends-on target rendered in the authoring
	// reference convention ("" when the container carries none).
	Plan string
	// Created and Updated are the container line's frontmatter dates.
	Created string
	Updated string
	// EndedAt is the change-log date of the container-state
	// active -> completed transition ("" while active).
	EndedAt string
}

// ContainersDetailed returns every execution container LINE — the
// highest instance of each (namespace, type, id) line, deduplicated by
// canonical identity — with the plan, lifecycle dates and completion
// date, sorted by canonical identity. This is the derivation source of
// the containers projection (Containers() carries the same line-level
// deduplication without the detail).
func (g *Graph) ContainersDetailed() []ContainerDetail {
	var out []ContainerDetail
	for _, u := range g.byForm {
		if u.Identity.Type != "ctr" {
			continue
		}
		d := ContainerDetail{
			Identity: LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID),
			Type:     u.Identity.Type,
			ID:       u.Identity.ID,
			State:    u.StateVector.ContainerState,
			Created:  u.Created,
			Updated:  u.Updated,
		}
		if rels := g.relsOf(u, "depends-on"); len(rels) > 0 {
			d.Plan = rels[0]
		}
		for _, e := range u.ChangeLog {
			if e.Domain == conformance.DomainContainerState && e.From == "active" && e.To == "completed" {
				d.EndedAt = e.Date
				break
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out
}

// WorkItems returns every work item line in the repository — all unit
// lines whose type owns the Execution State domain, in every container
// and outside any container — deduplicated by identity line and sorted
// by canonical identity. This is the membership source of the board
// projection.
func (g *Graph) WorkItems() []WorkItem {
	var out []WorkItem
	for _, u := range g.byForm {
		if isWorkItemType(u.Identity.Type) {
			out = append(out, *g.workItemFor(u))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out
}

// WorkItemsForMember returns the work items assigned to the member line
// — the mbr- line whose canonical identity form is form — deduplicated
// by identity line and sorted by canonical identity. Membership derives
// from the assigned-to relationship only (ADR-013): an item belongs to
// a member's scope iff its assigned-to edge resolves to the member
// line. Items without any assigned-to edge are never matched here; the
// member board's dedicated 'No assignee' bucket surfaces them instead
// (ADR-029 Decision 3 — never silently excluded).
func (g *Graph) WorkItemsForMember(form string) []WorkItem {
	var out []WorkItem
	for _, u := range g.byForm {
		if !isWorkItemType(u.Identity.Type) {
			continue
		}
		if g.assigneeOf(u) != form {
			continue
		}
		out = append(out, *g.workItemFor(u))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out
}

// ContainersForWorkItem returns the canonical identities of the
// containers whose tickets reference the work item line, deduplicated
// and sorted. Membership follows the same relationship-only rule as
// the execution projection: a container references a work item iff one
// of its tickets derives from the work item's identity line. An empty
// result means the work item is not referenced by any ticket container
// (unassigned).
func (g *Graph) ContainersForWorkItem(form string) []string {
	seen := make(map[string]bool)
	var out []string
	// Line-level tickets: the highest instance of each tkt- line is the
	// ticket's current knowledge (ADR-025) — a revised ticket decides
	// membership by its latest derives-from set.
	for _, t := range g.unitsOfType("tkt") {
		container, workItem := g.ticketTargets(t)
		if container == nil || workItem == nil || workItem.Identity != form {
			continue
		}
		if !seen[container.Identity] {
			seen[container.Identity] = true
			out = append(out, container.Identity)
		}
	}
	sort.Strings(out)
	return out
}

// Ticket is one ticket (tkt-) line in the graph, with the status
// projected from its referenced work item.
type Ticket struct {
	Identity string
	Type     string
	ID       string
	// Projected is the execution-state of the ticket's referenced work
	// item, or "unresolved" when the ticket has no resolvable work item
	// reference. It is derived from the owner unit's state — the
	// ticket's own body content is never read.
	Projected string
}

// WorkItem is one work item (sto-/ts-/bug-/td-/ch-/spk-) line in the
// graph.
type WorkItem struct {
	Identity string
	Type     string
	ID       string
	// State is the execution-state value.
	State string
	// Dimension is the informational dimension field, when present.
	Dimension    string
	HasDimension bool
	// Created is the work item line's created date (frontmatter), ""
	// when absent. It is the display ordering key of the execution
	// and board projections (items sort by created date ascending).
	Created string
	// Assignee is the canonical line identity form of the member
	// (mbr-) line the item's assigned-to relationship resolves to,
	// "" when the item carries no resolvable assigned-to edge.
	// Assignment derives from the relationship only (ADR-013) — never
	// from content; the 'No assignee' bucket of the member-scoped
	// board holds items with an empty assignee.
	Assignee string
	// NotesCount is the number of cmt- notes discussing the item
	// (published notes of the store; filled by the execution
	// projection).
	NotesCount int
}

// isWorkItemType reports whether a type token owns the Execution State
// domain — the six work item types, characterized without duplicating
// the type table.
func isWorkItemType(token string) bool {
	for _, d := range conformance.OwnedDomains(token) {
		if d == conformance.DomainExecutionState {
			return true
		}
	}
	return false
}

// workItemFor builds the WorkItem model of a work item unit, resolving
// its assignee from the assigned-to relationship ("" when the item
// carries no resolvable assigned-to edge).
func (g *Graph) workItemFor(u *exchange.Unit) *WorkItem {
	return &WorkItem{
		Identity:     LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID),
		Type:         u.Identity.Type,
		ID:           u.Identity.ID,
		State:        u.StateVector.ExecutionState,
		Dimension:    u.Classification.Dimension,
		HasDimension: u.Classification.Dimension != "",
		Created:      u.Created,
		Assignee:     g.assigneeOf(u),
	}
}

// assigneeOf resolves the unit's assigned-to target to the canonical
// line identity form of its member (mbr-) line, or "" when the unit
// carries no resolvable assigned-to edge. Assignment derives from the
// assigned-to relationship only (ADR-013 — never from content); the
// target must resolve to an mbr- line of the graph, and the first
// resolvable target in stored order wins (conformant data carries at
// most one — single-assignee, ADR-029).
func (g *Graph) assigneeOf(u *exchange.Unit) string {
	for _, r := range u.Relationships {
		if r.Type != "assigned-to" {
			continue
		}
		ref, err := conformance.ParseReference(r.Target, u.Identity.Namespace, u.Identity.Type)
		if err != nil {
			continue // Canonical targets always parse; defensive.
		}
		target := g.Resolve(ref)
		if target == nil || target.Identity.Type != "mbr" {
			continue
		}
		return LineForm(target.Identity.Namespace, target.Identity.Type, target.Identity.ID)
	}
	return ""
}

// ticketTargets resolves the container and the work item a ticket
// derives from, scanning the ticket's derives-from targets in stored
// order: the first resolvable ctr- reference is the container, the
// first resolvable execution-state-owning reference is the work item.
func (g *Graph) ticketTargets(t *exchange.Unit) (*Container, *WorkItem) {
	if t == nil {
		return nil, nil
	}
	var container *Container
	var workItem *WorkItem
	for _, raw := range g.relsOf(t, "derives-from") {
		ref, err := conformance.ParseReference(raw, t.Identity.Namespace, t.Identity.Type)
		if err != nil {
			continue // malformed references are reported by Rule 5.
		}
		target := g.Resolve(ref)
		if target == nil {
			continue
		}
		switch {
		case container == nil && target.Identity.Type == "ctr":
			container = containerFor(target)
		case workItem == nil && isWorkItemType(target.Identity.Type):
			workItem = g.workItemFor(target)
		}
	}
	return container, workItem
}

// ticketsFor returns the ticket lines deriving from the container
// identity form, sorted by canonical identity — one ticket per tkt-
// line, the line's highest instance (ADR-025).
func (g *Graph) ticketsFor(form string) []*exchange.Unit {
	var out []*exchange.Unit
	for _, u := range g.unitsOfType("tkt") {
		container, _ := g.ticketTargets(u)
		if container != nil && container.Identity == form {
			out = append(out, u)
		}
	}
	return out
}

// TicketsForContainer returns the tickets deriving from the container
// identity form (canonical line identity "<ns>/<type>:<id>"), sorted by
// canonical identity. Membership is relationship-only: a ticket belongs
// to the container iff one of its derives-from references resolves to
// the container's identity line.
func (g *Graph) TicketsForContainer(form string) []Ticket {
	out := make([]Ticket, 0, len(g.byType["tkt"]))
	for _, u := range g.ticketsFor(form) {
		_, workItem := g.ticketTargets(u)
		projected := "unresolved"
		if workItem != nil {
			projected = workItem.State
		}
		out = append(out, Ticket{
			Identity:  LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID),
			Type:      u.Identity.Type,
			ID:        u.Identity.ID,
			Projected: projected,
		})
	}
	return out
}

// WorkItemsForContainer returns the work items of the container's
// tickets — the members of the container — deduplicated by identity
// line and sorted by canonical identity. A work item is a member iff a
// ticket deriving from the container also derives from the work item;
// tickets without a resolvable work item contribute nothing.
func (g *Graph) WorkItemsForContainer(form string) []WorkItem {
	seen := make(map[string]bool)
	var out []WorkItem
	for _, u := range g.ticketsFor(form) {
		_, workItem := g.ticketTargets(u)
		if workItem == nil || seen[workItem.Identity] {
			continue
		}
		seen[workItem.Identity] = true
		out = append(out, *workItem)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out
}

// ContainerForTicket resolves the container a ticket derives from, or
// nil when the ticket has no resolvable ctr- reference.
func (g *Graph) ContainerForTicket(t *exchange.Unit) *Container {
	container, _ := g.ticketTargets(t)
	return container
}

// WorkItemForTicket resolves the work item a ticket derives from, or
// nil when the ticket has no resolvable work item reference.
func (g *Graph) WorkItemForTicket(t *exchange.Unit) *WorkItem {
	_, workItem := g.ticketTargets(t)
	return workItem
}

// TicketByTarget resolves an execution item from a user-supplied
// target string: a ticket (tkt-) first, then any WORK ITEM
// (sto-/ts-/bug-/td-/ch-/spk-), so board items are reachable through
// the ticket projection too.
//
// Resolution priority (deterministic):
//  1. EXACT ID match — the raw target with the tkt-/tkt: prefixes
//     stripped, matched across tickets first, then work items (the
//     historical bare-id semantics: "ts-gamma" resolves the ticket
//     tkt:ts-gamma, "draft-autosave" resolves the story sto:draft-
//     autosave);
//  2. TYPED forms — "<type>:<id>" or "<type>-<id>" (a known type),
//     and the qualified "<ns>/<type>:<id>" (the namespace must match).
//
// It returns nil when nothing matches. When several namespaces hold an
// item with the same id, the lexicographically smallest canonical
// identity wins.
func (g *Graph) TicketByTarget(target string) *exchange.Unit {
	// Priority 1: exact id match (tickets first, then work items).
	rawID := strings.TrimPrefix(strings.TrimPrefix(target, "tkt-"), "tkt:")
	if !strings.Contains(rawID, "/") && !strings.Contains(rawID, ":") {
		for _, u := range g.unitsOfType("tkt") {
			if u.Identity.ID == rawID {
				return u
			}
		}
		// Work items, line-level: the highest instance of each line
		// (ADR-025), sorted by canonical identity.
		var items []*exchange.Unit
		for _, u := range g.byForm {
			if isWorkItemType(u.Identity.Type) {
				items = append(items, u)
			}
		}
		sort.Slice(items, func(i, j int) bool {
			return LineForm(items[i].Identity.Namespace, items[i].Identity.Type, items[i].Identity.ID) <
				LineForm(items[j].Identity.Namespace, items[j].Identity.Type, items[j].Identity.ID)
		})
		for _, u := range items {
			if u.Identity.ID == rawID {
				return u
			}
		}
	}
	// Priority 2: typed forms (colon or dash, qualified or not).
	ns, t, id := parseDocumentTarget(target)
	if id == "" {
		return nil
	}
	if t != "" && t != "tkt" && !isWorkItemType(t) {
		return nil // the target names a non-execution type
	}
	types := []string{"tkt"}
	for _, u := range g.byForm {
		if isWorkItemType(u.Identity.Type) {
			types = append(types, u.Identity.Type)
		}
	}
	for _, typeToken := range types {
		if t != "" && t != typeToken {
			continue
		}
		for _, u := range g.unitsOfType(typeToken) {
			if u.Identity.ID != id {
				continue
			}
			if ns != "" && u.Identity.Namespace != ns {
				continue
			}
			return u
		}
	}
	return nil
}

// TicketIDs returns the ids of every ticket LINE — one per tkt- line,
// deduplicated by canonical identity (ADR-025) — sorted by canonical
// identity: the available targets of the ticket projection.
func (g *Graph) TicketIDs() []string {
	out := make([]string, 0, len(g.byForm))
	for _, u := range g.unitsOfType("tkt") {
		out = append(out, u.Identity.ID)
	}
	return out
}

// StateColumn is one execution-state column of a projection: the fixed
// value plus its work items, sorted by canonical identity.
type StateColumn struct {
	State     string
	WorkItems []WorkItem
}

// StateColumns is the ordered execution-state column set. The order is
// the fixed value order planned, todo, in-progress, in-review, done,
// canceled (ADR-019) — never map iteration.
type StateColumns []StateColumn

// Count returns the number of work items in the column for state, or 0
// when the column is absent.
func (c StateColumns) Count(state string) int {
	for _, col := range c {
		if col.State == state {
			return len(col.WorkItems)
		}
	}
	return 0
}

// groupByState groups work items into the fixed execution-state column
// order (six values since ADR-019: canceled added). Work items whose state is not a valid execution-state value
// (impossible behind the validation gate) are not placed in any column.
func groupByState(items []WorkItem) StateColumns {
	order := conformance.DomainValues(conformance.DomainExecutionState, "sto")
	cols := make(StateColumns, 0, len(order))
	for _, state := range order {
		col := StateColumn{State: state}
		for _, wi := range items {
			if wi.State == state {
				col.WorkItems = append(col.WorkItems, wi)
			}
		}
		cols = append(cols, col)
	}
	return cols
}

// --- Issue numbers (RFC: per-group incremental numbers) ---

// AttachNumbers binds the line-form -> issue-number map of the project
// to the graph (the store accessor NumbersByProject; projections stay
// store-backed). Optional: without it Number returns 0.
func (g *Graph) AttachNumbers(numbers map[string]int) {
	g.numbers = numbers
}

// Number returns the issue number of one line form (0 = none).
func (g *Graph) Number(form string) int {
	if g.numbers == nil {
		return 0
	}
	return g.numbers[form]
}

// NumberLabel renders the display label of a line's issue number:
// "#<n>" when numbered, "" otherwise.
func (g *Graph) NumberLabel(form string) string {
	if n := g.Number(form); n > 0 {
		return fmt.Sprintf("#%d", n)
	}
	return ""
}
