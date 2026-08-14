package contexts

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/runtime"
)

// This file implements the Context Engine: the deterministic
// construction of the Context Object around one knowledge subject.
// The engine composes runtime service calls only (Resolver,
// Relations, Timeline, Knowledge) — it never touches the store, never
// parses Markdown, and never walks the graph itself beyond the
// runtime traversals.

// Depth is one context depth: how far the engine looks around the
// focus.
type Depth string

const (
	// DepthLocal is the local depth: the focus itself (full detail)
	// plus its instance-line history. No relationships are collected.
	DepthLocal Depth = "local"
	// DepthDependency is the dependency depth: the one-hop
	// neighborhood — upstream, downstream and depends-on /
	// derives-from targets — classified into sections and strata.
	DepthDependency Depth = "dependency"
	// DepthEngineering is the engineering depth: everything of the
	// dependency depth PLUS a bounded constraint closure — higher-
	// authority units (strata above the focus) reachable through the
	// collected units' own outgoing relationships (max 2 hops, at
	// most 64 units).
	DepthEngineering Depth = "engineering"
)

// ParseDepth resolves a user depth token ("local", "dependency",
// "engineering") to its Depth. The second return value is false for
// unknown tokens — the deterministic usage-error path of the CLI.
func ParseDepth(s string) (Depth, bool) {
	switch Depth(s) {
	case DepthLocal, DepthDependency, DepthEngineering:
		return Depth(s), true
	default:
		return "", false
	}
}

// Depths lists the three depth tokens in ascending reach order — the
// deterministic "local | dependency | engineering" list of the usage
// errors and help text.
func Depths() []string {
	return []string{string(DepthLocal), string(DepthDependency), string(DepthEngineering)}
}

// Options carries the build options of one context construction.
type Options struct {
	// NoContent strips the focus content payload: the "content" key is
	// absent from the serialized object. Token-saving for consumers
	// that only need identity, state and relationships.
	NoContent bool
}

// Engine constructs Context Objects. It is a thin Runtime consumer:
// one Engine per Runtime, stateless per Build call (concurrent-safe —
// Build shares no mutable state).
type Engine struct{ rt *runtime.Runtime }

// New wires the Context Engine to one Runtime.
func New(rt *runtime.Runtime) *Engine {
	return &Engine{rt: rt}
}

// maxConstraintClosure is the deterministic cap of the engineering
// closure: at most 64 new higher-authority units are added through
// the hop-2 expansion. Bounded by design — a context stays a bounded
// lens, never a full graph crawl.
const maxConstraintClosure = 64

// Build constructs the Context Object for subject at depth.
//
// Subject grammar (the Resolver grammar): the canonical form
// "<ns>/<type>:<id>:<v>" (the exact instance) or the qualified line
// form "<ns>/<type>:<id>" (the lowest instance). The namespace is
// required — the Runtime resolves globally. projectID is used only for
// the issue-number lookup ("" => Number omitted everywhere); lookup
// failures are ignored, Number stays 0 (the `eka get` contract).
//
// Determinism: every section is sorted by canonical form, every list
// is deduplicated by line form (first role encountered wins — the
// edge iteration order is deterministic: stored (type, target) order
// for outgoing edges, the Relations.To sorted scan order for incoming
// edges), and the collected units are iterated in sorted canonical-
// form order.
//
// Depth local: the focus plus its history; no sections, one stratum
// (the focus's own). Depth dependency: the one-hop neighborhood
// classified into sections and strata. Depth engineering: the
// dependency context plus the bounded constraint closure (at most 2
// hops, at most 64 units, higher-authority strata only).
func (e *Engine) Build(subject, projectID string, depth Depth, opts Options) (*Object, error) {
	// Focus resolution: the Resolver contract — errors and not-found
	// both propagate as deterministic errors (the CLI maps them to
	// exit 2).
	u, ok, err := e.rt.Resolver.Resolve(subject)
	if err != nil {
		return nil, fmt.Errorf("contexts: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("contexts: no knowledge object matches %q", subject)
	}
	focus, err := e.buildFocus(u, projectID, opts)
	if err != nil {
		return nil, err
	}
	history, err := e.buildHistory(u)
	if err != nil {
		return nil, err
	}

	// The shared object spine: schema, kind, focus, depth, summary and
	// history are fixed; the strata and sections differ per depth.
	obj := &Object{
		Schema:   Schema,
		Kind:     "context",
		Focus:    *focus,
		Depth:    string(depth),
		Summary:  Summary{Focus: 1, History: len(history)},
		Sections: Sections{History: history},
	}

	switch depth {
	case DepthLocal:
		// Local: the focus's own stratum carries the focus as its
		// single Entry (Role "") — the object plus its history.
		obj.Strata = []Stratum{{
			Stratum: focus.Stratum,
			Domain:  focus.EngineeringDomain,
			Units:   []Entry{entryFor(u, "", focus.Number)},
		}}
		obj.Summary.Units = 1
		return obj, nil

	case DepthDependency, DepthEngineering:
		// Dependency: the one-hop collection, classified into sections
		// and strata. Engineering: the same collection plus the bounded
		// constraint closure (hop-2 higher-authority units).
		dc, err := e.collectDependency(focus)
		if err != nil {
			return nil, err
		}
		if depth == DepthEngineering {
			if err := e.expandConstraints(dc.col, focus); err != nil {
				return nil, err
			}
		}
		numbers := dc.col.numbers(projectID, e)
		obj.Strata = groupStrata(dc.col.sorted(), numbers, dc.col.roles)
		obj.Sections.Upstream, obj.Sections.Downstream, obj.Sections.Dependencies,
			obj.Sections.Constraints, obj.Sections.Decisions, obj.Sections.Planning,
			obj.Sections.Review = classify(dc, numbers, focus)
		obj.Summary.Units = len(dc.col.sorted())
		obj.Summary.Sections = countSections(obj.Sections)
		return obj, nil

	default:
		return nil, fmt.Errorf("contexts: unknown depth %q", depth)
	}
}

// buildFocus maps the resolved focus unit to the Focus detail of the
// Context Object: the machine document shape minus schema/author/
// created/updated, plus the qualified line form.
func (e *Engine) buildFocus(u *exchange.Unit, projectID string, opts Options) (*Focus, error) {
	domain := u.Classification.Domain
	if domain == "" {
		d, ok := conformance.DomainForToken(u.Identity.Type)
		if !ok {
			return nil, errUnknownType(u.Identity.Type)
		}
		domain = string(d)
	}
	focus := &Focus{
		Identity: Identity{
			Namespace:       u.Identity.Namespace,
			Type:            u.Identity.Type,
			ID:              u.Identity.ID,
			InstanceVersion: u.Identity.InstanceVersion,
		},
		CanonicalForm:     u.CanonicalIdentityForm,
		LineForm:          lineFormOf(u),
		EngineeringDomain: domain,
		Stratum:           conformance.Stratum(conformance.Domain(domain)),
		Revision:          u.Revision,
		StateVector: StateVector{
			ContentState:   u.StateVector.ContentState,
			ExecutionState: u.StateVector.ExecutionState,
			PlanningState:  u.StateVector.PlanningState,
			ContainerState: u.StateVector.ContainerState,
			ExistenceState: u.StateVector.ExistenceState,
			NoteState:      u.StateVector.NoteState,
		},
		Phase:      u.Phase,
		ObjectHash: u.Digest,
	}
	// The line's issue number (RFC): additive "number" field — 0 (no
	// number) omits it; the lookup failure is ignored exactly like the
	// `eka get` contract (the number is a display nicety, never a
	// failure).
	if projectID != "" {
		if number, nerr := e.rt.Knowledge.NumberForLine(projectID,
			u.Identity.Namespace, u.Identity.Type, u.Identity.ID); nerr == nil {
			focus.Number = number
		}
	}
	if u.Classification.Dimension != "" || len(u.Classification.DimensionsSecondary) > 0 || u.Classification.Domain != "" {
		c := Classification{
			Dimension:           u.Classification.Dimension,
			DimensionsSecondary: u.Classification.DimensionsSecondary,
			Domain:              u.Classification.Domain,
		}
		focus.Classification = &c
	}
	for _, r := range u.Relationships {
		focus.Relationships = append(focus.Relationships, Relationship{Type: r.Type, Target: r.Target})
	}
	// The content shape mirrors the machine document: structured-json
	// payloads travel as `fields` (the canonical payload bytes
	// verbatim), legacy structured-text payloads as `text`. Exactly
	// one of the two is present. NoContent strips the payload: the
	// pointer plus omitempty keeps the "content" key absent.
	if !opts.NoContent {
		content := &Content{Representation: u.Content.Representation}
		if u.Content.Representation == exchange.StructuredJSON && json.Valid(u.ContentPayload) {
			content.Fields = u.ContentPayload
		} else {
			content.Text = string(u.ContentPayload)
		}
		focus.Content = content
	}
	return focus, nil
}

// buildHistory maps the focus line's timeline (the Runtime Timeline:
// every instance of the line across the workspace, ascending
// instance-version — the service contract) to the History entries of
// the Context Object.
func (e *Engine) buildHistory(u *exchange.Unit) ([]HistoryEntry, error) {
	entries, err := e.rt.Timeline.Line(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
	if err != nil {
		return nil, fmt.Errorf("contexts: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]HistoryEntry, 0, len(entries))
	for _, t := range entries {
		out = append(out, HistoryEntry{
			CanonicalForm:   t.Form,
			InstanceVersion: t.InstanceVersion,
			Revision:        t.Revision,
			ObjectHash:      t.ObjectHash,
			ChangeLog:       contextChangeLog(t.ChangeLog),
		})
	}
	return out, nil
}

// contextChangeLog maps runtime change-log entries (exchange payload
// order) to the context change-log entries of the history projection.
func contextChangeLog(entries []exchange.ChangeLogEntry) []ChangeLogEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]ChangeLogEntry, 0, len(entries))
	for _, c := range entries {
		out = append(out, ChangeLogEntry{Date: c.Date, Domain: c.Domain, From: c.From, To: c.To, By: c.By})
	}
	return out
}

// collector is the deterministic unit accumulator of one context
// build: the unique collected set keyed by line form, the first-wins
// collected role of every line, and the sorted iteration order.
type collector struct {
	units map[string]*exchange.Unit
	roles map[string]string
}

// newCollector starts an empty collector.
func newCollector() *collector {
	return &collector{units: map[string]*exchange.Unit{}, roles: map[string]string{}}
}

// add inserts one unit with its role. It reports false when the line
// is already collected — the deduplication contract of the context
// (a unit appears in exactly one stratum); the first role wins.
func (c *collector) add(u *exchange.Unit, role string) bool {
	line := lineFormOf(u)
	if _, ok := c.units[line]; ok {
		return false
	}
	c.units[line] = u
	c.roles[line] = role
	return true
}

// contains reports whether the line is already collected.
func (c *collector) contains(line string) bool {
	_, ok := c.units[line]
	return ok
}

// sorted returns the collected units sorted by canonical form — the
// deterministic iteration order of the context build.
func (c *collector) sorted() []*exchange.Unit {
	out := make([]*exchange.Unit, 0, len(c.units))
	for _, u := range c.units {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CanonicalIdentityForm < out[j].CanonicalIdentityForm
	})
	return out
}

// numbers resolves the issue number of every collected line of the
// project (NumberForLine failures are ignored — Number stays 0, the
// `eka get` contract). Returns nil when projectID is "" — the "no
// numbers anywhere" contract.
func (c *collector) numbers(projectID string, e *Engine) map[string]int {
	if projectID == "" {
		return nil
	}
	out := make(map[string]int, len(c.units))
	for line, u := range c.units {
		if number, err := e.rt.Knowledge.NumberForLine(projectID,
			u.Identity.Namespace, u.Identity.Type, u.Identity.ID); err == nil {
			out[line] = number
		}
	}
	return out
}

// dependencyContext is the result of the one-hop collection: the
// collected unit set plus the three first-wins role lookups that
// classify the sections (the edge iteration order is deterministic:
// stored (type, target) order for outgoing edges, the Relations.To
// sorted scan order for incoming edges).
type dependencyContext struct {
	col *collector
	// upstreamRole maps every outgoing edge target line to its FIRST
	// outgoing edge type.
	upstreamRole map[string]string
	// downstreamRole maps every referring unit line to its FIRST
	// incoming edge type.
	downstreamRole map[string]string
	// depRole maps every resolved depends-on / derives-from target
	// line to its FIRST dependency edge type.
	depRole map[string]string
}

// collectDependency runs the one-hop collection of the dependency
// depth: the upstream units (the focus's relationship targets), the
// downstream units (the units that reference the focus), and the
// depends-on / derives-from outgoing targets — deduplicated by line
// form into one collected set, with the first-wins edge role of every
// unit. Runtime failures propagate as deterministic errors.
func (e *Engine) collectDependency(focus *Focus) (*dependencyContext, error) {
	dc := &dependencyContext{col: newCollector()}

	// Upstream: the units the focus's relationships point at (the
	// Relations.Upstream traversal, sorted by canonical form). The
	// role is the FIRST outgoing edge type in stored (type, target)
	// order — the From scan order.
	upstreamUnits, err := e.rt.Relations.Upstream(focus.CanonicalForm)
	if err != nil {
		return nil, fmt.Errorf("contexts: %w", err)
	}
	fromRels, err := e.rt.Relations.From(focus.CanonicalForm)
	if err != nil {
		return nil, fmt.Errorf("contexts: %w", err)
	}
	dc.upstreamRole = edgeRoleMap(fromRels)
	for _, unit := range upstreamUnits {
		line := lineFormOf(unit)
		if line == focus.LineForm {
			continue // Self-reference: the focus is never its own neighbor.
		}
		dc.col.add(unit, dc.upstreamRole[line])
	}

	// Downstream: the units that reference the focus (the
	// Relations.Downstream traversal, sorted by canonical form). The
	// role is the FIRST incoming edge type in the Relations.To sorted
	// scan order.
	downstreamUnits, err := e.rt.Relations.Downstream(focus.CanonicalForm)
	if err != nil {
		return nil, fmt.Errorf("contexts: %w", err)
	}
	toRels, err := e.rt.Relations.To(focus.CanonicalForm)
	if err != nil {
		return nil, fmt.Errorf("contexts: %w", err)
	}
	dc.downstreamRole = edgeRoleMap(toRels)
	for _, unit := range downstreamUnits {
		line := lineFormOf(unit)
		if line == focus.LineForm {
			continue // Self-reference: the focus is never its own neighbor.
		}
		dc.col.add(unit, dc.downstreamRole[line])
	}

	// Dependencies: the depends-on / derives-from outgoing targets in
	// the From scan order, resolved individually — unresolvable
	// targets are skipped (draft tolerance), the role is the edge
	// type.
	dc.depRole = map[string]string{}
	for _, rel := range fromRels {
		if rel.Type != "depends-on" && rel.Type != "derives-from" {
			continue
		}
		target, ok, rerr := e.rt.Resolver.Resolve(rel.Target)
		if rerr != nil {
			return nil, fmt.Errorf("contexts: %w", rerr)
		}
		if !ok {
			continue // Draft tolerance: an unresolved target is skipped.
		}
		line := lineFormOf(target)
		if line == focus.LineForm {
			continue // Self-dependency: the focus never depends on itself.
		}
		if _, seen := dc.depRole[line]; !seen {
			dc.depRole[line] = rel.Type
		}
		dc.col.add(target, rel.Type)
	}
	return dc, nil
}

// edgeRoleMap builds the first-wins line-form -> edge-type lookup of
// an edge list, in the list's given deterministic scan order: the
// first occurrence of a line form records its role, later occurrences
// are ignored.
func edgeRoleMap(rels []runtime.Relation) map[string]string {
	out := make(map[string]string, len(rels))
	for _, rel := range rels {
		line := lineFormOfForm(rel.Target)
		if _, seen := out[line]; !seen {
			out[line] = rel.Type
		}
	}
	return out
}

// expandConstraints runs the bounded constraint closure of the
// engineering depth: every dependency-collected unit (in sorted
// canonical-form order) contributes its own upstream units; a hop-2
// unit joins the collection as a constraint — Role "constraint" —
// when its stratum is strictly higher than the focus's (a strictly
// smaller stratum number), its line differs from the focus line, and
// its line is not already collected. At most 64 units are added
// (maxConstraintClosure — the expansion stops as soon as the cap is
// reached); hop-2 units are never expanded further (max traversal
// depth = 2 hops).
func (e *Engine) expandConstraints(col *collector, focus *Focus) error {
	added := 0
	for _, u := range col.sorted() {
		upstream, err := e.rt.Relations.Upstream(u.CanonicalIdentityForm)
		if err != nil {
			return fmt.Errorf("contexts: %w", err)
		}
		for _, h := range upstream {
			domain, err := domainOf(h)
			if err != nil {
				// Unreachable for store units (conformance-validated):
				// defensive skip keeps the closure deterministic.
				continue
			}
			if conformance.Stratum(conformance.Domain(domain)) >= focus.Stratum {
				continue // Not a higher-authority unit (stratum >= focus).
			}
			line := lineFormOf(h)
			if line == focus.LineForm {
				continue // The focus line itself is never a constraint.
			}
			if col.contains(line) {
				continue // Already collected: one stratum, one role.
			}
			col.add(h, "constraint")
			added++
			if added == maxConstraintClosure {
				return nil // The deterministic cap: stop the whole expansion.
			}
		}
	}
	return nil
}

// classify builds the classified sections of the context from the
// collected set: upstream, downstream and dependencies by edge
// membership (each with its own first-wins role); constraints by
// stratum (strictly higher authority than the focus — a strictly
// smaller stratum number, the collected role); decisions (adr-/dec-),
// planning (scp-/epc-/plan-/trc-) and review (rvw-/cmt-) by type
// token (the collected role). Every section is sorted by canonical
// form (the collected iteration order) and deduplicated by line form
// (the collector contract); empty sections stay nil — absent from the
// JSON.
func classify(dc *dependencyContext, numbers map[string]int, focus *Focus) (upstream, downstream, dependencies, constraints, decisions, planning, review []Entry) {
	for _, u := range dc.col.sorted() {
		line := lineFormOf(u)
		entry := entryFor(u, dc.col.roles[line], numbers[line])
		if role, ok := dc.upstreamRole[line]; ok {
			upstream = append(upstream, entryFor(u, role, numbers[line]))
		}
		if role, ok := dc.downstreamRole[line]; ok {
			downstream = append(downstream, entryFor(u, role, numbers[line]))
		}
		if role, ok := dc.depRole[line]; ok {
			dependencies = append(dependencies, entryFor(u, role, numbers[line]))
		}
		switch u.Identity.Type {
		case "adr", "dec":
			decisions = append(decisions, entry)
		case "scp", "epc", "plan", "trc":
			planning = append(planning, entry)
		case "rvw", "cmt":
			review = append(review, entry)
		}
		if domain, err := domainOf(u); err == nil &&
			conformance.Stratum(conformance.Domain(domain)) < focus.Stratum {
			constraints = append(constraints, entry)
		}
	}
	return upstream, downstream, dependencies, constraints, decisions, planning, review
}

// countSections counts the non-empty sections of the context —
// upstream, downstream, dependencies, constraints, decisions,
// planning and review (history excluded — the Summary contract).
func countSections(s Sections) int {
	n := 0
	for _, list := range [][]Entry{
		s.Upstream, s.Downstream, s.Dependencies,
		s.Constraints, s.Decisions, s.Planning, s.Review,
	} {
		if len(list) > 0 {
			n++
		}
	}
	return n
}
