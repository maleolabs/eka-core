package view

import (
	"sort"

	"github.com/maleolabs/eka-core/conformance"
)

// This file implements the planning projection: the Planning domain
// (scp-, epc-, plan-, trc-) as a roadmap tree. Plans are the
// milestone roots at one shared level, ordered by created date
// ascending (oldest commitment first); a plan deriving from another
// plan nests under it as a sub-plan (one level). Each plan carries
// the scope definitions and epics that derive from it (exact
// derives-from line-form match), also ordered by created date;
// scope definitions and epics deriving from no plan are orphans,
// listed after the plan groups. Every artifact carries its
// content-state; scope definitions and plans also carry their phase
// context, and plans their planning-state. The summary counts
// artifacts per group and plans per planning-state. Machine
// retrieval (`eka get`) keeps the canonical alphabetical ordering —
// this created-date display order is a projection concern only.
//
// The planning projection ignores the optional target argument.

// PlanGroup is one plan of the planning projection: the milestone
// root plus the scope definitions and epics deriving from it, and
// any plans deriving from it (sub-plans).
type PlanGroup struct {
	// Plan is the milestone root line.
	Plan DomainArtifact
	// Children are the scope/epic lines whose derives-from contains
	// the plan's canonical identity, ordered by created date
	// ascending (tie-broken by canonical identity).
	Children []DomainArtifact
	// SubPlans are the plans whose derives-from contains this plan's
	// canonical identity, ordered by created date ascending
	// (tie-broken by canonical identity) — one nesting level.
	SubPlans []PlanGroup
}

// PlanningProjection is the Planning domain view.
type PlanningProjection struct {
	// Plans are the milestone roots, ordered by created date
	// ascending (tie-broken by canonical identity). Each group
	// carries the scope/epic lines deriving from its plan.
	Plans []PlanGroup
	// Orphans are the scope/epic lines deriving from no plan,
	// ordered by created date ascending (tie-broken by canonical
	// identity).
	Orphans []DomainArtifact
	// Traceability keeps the canonical (alphabetical) order — usually
	// one or two artifacts.
	Traceability []DomainArtifact
	// PlansByState counts plans per planning-state in the fixed value
	// order draft, approved, immutable.
	PlansByState []StateCount
}

// Name returns the registry name of the projection.
func (p *PlanningProjection) Name() string { return "planning" }

// Total returns the number of artifact lines of the projection: the
// plan roots, their scope/epic children, the sub-plans and their
// children, the orphans and the traceability artifacts.
func (p *PlanningProjection) Total() int {
	n := len(p.Plans) + len(p.Orphans) + len(p.Traceability)
	for _, group := range p.Plans {
		n += len(group.Children) + len(group.SubPlans)
		for _, sub := range group.SubPlans {
			n += len(sub.Children)
		}
	}
	return n
}

func buildPlanning(g *Graph, target string) (Projection, error) {
	groups := domainGroups(g, conformance.Planning, []groupDef{
		{[]string{"scp"}, "Scope Definitions"},
		{[]string{"epc"}, "Epics"},
		{[]string{"plan"}, "Plans"},
		{[]string{"trc"}, "Traceability"},
	})
	byName := make(map[string][]DomainArtifact, len(groups))
	for _, gr := range groups {
		byName[gr.Name] = gr.Artifacts
	}

	// Plans are the milestone roots, ordered by created date
	// ascending ("" first, deterministic), tie-broken by canonical
	// identity — the roadmap reads oldest commitment first.
	plans := append([]DomainArtifact{}, byName["Plans"]...)
	sort.SliceStable(plans, func(i, j int) bool { return byCreatedAsc(plans[i], plans[j]) })

	// Scope definitions and epics are the plan's children: a scope or
	// epic belongs under the plan it derives from (exact line-form
	// match against the plan's identity). Lines deriving from no plan
	// are orphans. Both keep the created-date order.
	scpEpc := make([]DomainArtifact, 0, len(byName["Scope Definitions"])+len(byName["Epics"]))
	scpEpc = append(scpEpc, byName["Scope Definitions"]...)
	scpEpc = append(scpEpc, byName["Epics"]...)
	sort.SliceStable(scpEpc, func(i, j int) bool { return byCreatedAsc(scpEpc[i], scpEpc[j]) })

	p := &PlanningProjection{}

	// A plan deriving from another plan nests under it as a sub-plan
	// (first match in created-date order — deterministic). The
	// remaining plans are the root milestones.
	parentOf := make(map[string]string, len(plans))
	var roots []DomainArtifact
	for _, plan := range plans {
		parent := ""
		for _, other := range plans {
			if other.Identity != plan.Identity && derivesFromPlan(plan, other.Identity) {
				parent = other.Identity
				break
			}
		}
		if parent == "" {
			roots = append(roots, plan)
		} else {
			parentOf[plan.Identity] = parent
		}
	}

	// Scope definitions and epics are the plan's children: a scope or
	// epic belongs under the plan it derives from (exact line-form
	// match against the plan's identity), the sub-plans' lines under
	// their own sub-plan. Lines deriving from no plan are orphans.
	// All keep the created-date order.
	assigned := make(map[string]bool, len(scpEpc))
	buildGroup := func(plan DomainArtifact) PlanGroup {
		group := PlanGroup{Plan: plan}
		for _, a := range scpEpc {
			if assigned[a.Identity] || !derivesFromPlan(a, plan.Identity) {
				continue
			}
			assigned[a.Identity] = true
			group.Children = append(group.Children, a)
		}
		return group
	}
	for _, root := range roots {
		group := buildGroup(root)
		for _, plan := range plans {
			if parentOf[plan.Identity] != root.Identity {
				continue
			}
			group.SubPlans = append(group.SubPlans, buildGroup(plan))
		}
		p.Plans = append(p.Plans, group)
	}
	for _, a := range scpEpc {
		if !assigned[a.Identity] {
			p.Orphans = append(p.Orphans, a)
		}
	}
	p.Traceability = append([]DomainArtifact{}, byName["Traceability"]...)

	// Plans by planning-state: fixed value order draft, approved,
	// immutable — counted over every plan line (roots and sub-plans).
	for _, state := range conformance.DomainValues(conformance.DomainPlanningState, "plan") {
		p.PlansByState = append(p.PlansByState, StateCount{State: state})
	}
	countState := func(plan DomainArtifact) {
		for i := range p.PlansByState {
			if p.PlansByState[i].State == plan.PlanningState {
				p.PlansByState[i].Count++
			}
		}
	}
	for _, group := range p.Plans {
		countState(group.Plan)
		for _, sub := range group.SubPlans {
			countState(sub.Plan)
		}
	}
	return p, nil
}

// byCreatedAsc orders two artifact lines by created date ascending
// (the frontmatter date format is zero-padded, so lexicographic
// comparison is chronological; an empty date sorts first —
// deterministic), then by canonical identity ascending.
func byCreatedAsc(a, b DomainArtifact) bool {
	if a.Created != b.Created {
		return a.Created < b.Created
	}
	return a.Identity < b.Identity
}

// derivesFromPlan reports whether the artifact's derives-from
// references contain the plan's canonical identity line (exact
// line-form match).
func derivesFromPlan(a DomainArtifact, planIdentity string) bool {
	for _, ref := range a.DerivesFrom {
		if ref == planIdentity {
			return true
		}
	}
	return false
}
