package view

import (
	"sort"

	"github.com/maleolabs/eka-core/conformance"
)

// This file implements the shared model of the domain projections
// (discovery, architecture, planning, operations): unit lines
// grouped by type in a fixed, projection-specific order. The model is
// plain, deterministically ordered data; rendering conventions (state
// coloring) are the caller's concern.

// Group is one unit group of a domain projection: the type tokens
// it aggregates, its display name, and its unit lines sorted by
// canonical identity.
type Group struct {
	// Name is the group's display name ("Scope Definitions",
	// "Decisions", ...).
	Name string
	// Artifacts are the group's unit lines, sorted by canonical
	// identity (line-level: instance-versions collapse to the line).
	Artifacts []DomainArtifact
}

// DomainArtifact is one unit line of a domain projection: its
// canonical identity plus the state values relevant to its group —
// content-state on every knowledge unit; planning-state and phase
// on Planning units. Presence flags distinguish an absent field
// from an empty value (a CKO omits absent fields).
type DomainArtifact struct {
	Identity         string
	Type             string
	ID               string
	ContentState     string
	HasContentState  bool
	PlanningState    string
	HasPlanningState bool
	Phase            string
	HasPhase         bool
	// Created is the artifact line's created date (frontmatter), ""
	// when absent. It is the display ordering key of the planning
	// projection: plans (and their scope/epic children) sort by
	// created date ascending, oldest commitment first.
	Created string
	// DerivesFrom are the canonical line identity forms of the
	// artifact's derives-from relationship targets, resolved at the
	// line level ("<ns>/<type>:<id>", no instance version) so they
	// compare directly with identity lines. The planning projection
	// uses them to group scope definitions and epics under the plan
	// they derive from (exact line-form match).
	DerivesFrom []string
}

// StateCount is one (state, count) pair of a summary, in the fixed
// value order of its domain (e.g. plans by planning-state: draft,
// approved, immutable).
type StateCount struct {
	State string
	Count int
}

// groupDef defines one artifact group: the type tokens it aggregates
// and its display name.
type groupDef struct {
	tokens []string
	name   string
}

// typeIn reports whether tokens contains token.
func typeIn(tokens []string, token string) bool {
	for _, t := range tokens {
		if t == token {
			return true
		}
	}
	return false
}

// domainGroups collects the unit lines of each group in the fixed
// definition order. Within a group the lines are sorted by canonical
// identity and collapsed to line level — the highest instance, the
// latest knowledge version of the line (ADR-025): iterating the graph's
// line index (byForm) yields exactly one unit per identity line, never
// a superseded revision. Units whose home domain differs from the
// projection's domain are skipped (defensive: the validator already
// guarantees the mapping).
func domainGroups(g *Graph, domain conformance.Domain, defs []groupDef) []Group {
	groups := make([]Group, 0, len(defs))
	for _, def := range defs {
		group := Group{Name: def.name}
		// The line index is a map, so its iteration order is
		// nondeterministic; the artifacts are sorted below, keeping the
		// projection deterministic.
		for _, u := range g.byForm {
			if !typeIn(def.tokens, u.Identity.Type) {
				continue
			}
			home, ok := conformance.DomainForToken(u.Identity.Type)
			if !ok || home != domain {
				continue
			}
			group.Artifacts = append(group.Artifacts, DomainArtifact{
				Identity:         LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID),
				Type:             u.Identity.Type,
				ID:               u.Identity.ID,
				ContentState:     u.StateVector.ContentState,
				HasContentState:  u.StateVector.ContentState != "",
				PlanningState:    u.StateVector.PlanningState,
				HasPlanningState: u.StateVector.PlanningState != "",
				Phase:            u.Phase,
				HasPhase:         u.Phase != "",
				Created:          u.Created,
				DerivesFrom:      g.derivesFromForms(u),
			})
		}
		sort.Slice(group.Artifacts, func(i, j int) bool {
			return group.Artifacts[i].Identity < group.Artifacts[j].Identity
		})
		groups = append(groups, group)
	}
	return groups
}

// GroupTotal returns the number of artifact lines across all groups —
// the domain's artifact count.
func GroupTotal(groups []Group) int {
	total := 0
	for _, gr := range groups {
		total += len(gr.Artifacts)
	}
	return total
}
