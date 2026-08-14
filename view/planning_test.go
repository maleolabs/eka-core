package view

import (
	"reflect"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// artifactStates returns the (identity, content-state, planning-state,
// phase) tuple of one artifact — the projection's observable state.
func artifactStates(a DomainArtifact) [4]string {
	ps, ph := "", ""
	if a.HasPlanningState {
		ps = a.PlanningState
	}
	if a.HasPhase {
		ph = a.Phase
	}
	return [4]string{a.Identity, a.ContentState, ps, ph}
}

// planIdentities returns the plan identities of the projection's
// groups, in order.
func planIdentities(p *PlanningProjection) []string {
	out := make([]string, 0, len(p.Plans))
	for _, gr := range p.Plans {
		out = append(out, gr.Plan.Identity)
	}
	return out
}

// childIdentities returns the child identities of one plan group.
func childIdentities(gr PlanGroup) []string {
	out := make([]string, 0, len(gr.Children))
	for _, c := range gr.Children {
		out = append(out, c.Identity)
	}
	return out
}

// subPlanIdentities returns the sub-plan identities of one plan group.
func subPlanIdentities(gr PlanGroup) []string {
	out := make([]string, 0, len(gr.SubPlans))
	for _, sp := range gr.SubPlans {
		out = append(out, sp.Plan.Identity)
	}
	return out
}

func TestPlanningProjection(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("planning", g, "")
	if err != nil {
		t.Fatalf("Build(planning): %v", err)
	}
	planning, ok := p.(*PlanningProjection)
	if !ok {
		t.Fatalf("Build(planning) = %T, want *PlanningProjection", p)
	}

	// The milestone roots: the plan line, ordered by created date
	// (the fixture has one plan).
	if want := []string{validForm + "plan:roadmap-2026"}; !reflect.DeepEqual(planIdentities(planning), want) {
		t.Errorf("plans = %v, want %v", planIdentities(planning), want)
	}
	// The fixture's scp/epc carry no derives-from plan reference, so
	// both are orphans, ordered by created date (tie → canonical
	// identity).
	wantOrphans := []string{
		validForm + "epc:auth",
		validForm + "scp:wave-2",
	}
	gotOrphans := make([]string, 0, len(planning.Orphans))
	for _, a := range planning.Orphans {
		gotOrphans = append(gotOrphans, a.Identity)
	}
	if !reflect.DeepEqual(gotOrphans, wantOrphans) {
		t.Errorf("orphans = %v, want %v", gotOrphans, wantOrphans)
	}
	// Traceability keeps its canonical order.
	if want := []string{validForm + "trc:spec-trace"}; !reflect.DeepEqual(
		func() (out []string) {
			for _, a := range planning.Traceability {
				out = append(out, a.Identity)
			}
			return
		}(), want) {
		t.Errorf("traceability = %v, want %v", planning.Traceability, want)
	}

	// scp- carries its phase context, plan- its planning-state and
	// phase — the per-artifact state values.
	if got := artifactStates(planning.Orphans[1]); got != [4]string{validForm + "scp:wave-2", "approved", "", "mvp"} {
		t.Errorf("scp:wave-2 = %+v, want (approved, phase mvp)", got)
	}
	if got := artifactStates(planning.Orphans[0]); got != [4]string{validForm + "epc:auth", "review", "", ""} {
		t.Errorf("epc:auth = %+v, want (review)", got)
	}
	if got := artifactStates(planning.Plans[0].Plan); got != [4]string{validForm + "plan:roadmap-2026", "approved", "approved", "release"} {
		t.Errorf("plan:roadmap-2026 = %+v, want (approved, planning-state approved, phase release)", got)
	}
	// Created dates travel with the artifacts.
	for _, a := range planning.Orphans {
		if a.Created != "2026-08-05" {
			t.Errorf("orphan %s created = %q, want 2026-08-05", a.Identity, a.Created)
		}
	}
	if planning.Plans[0].Plan.Created != "2026-08-05" {
		t.Errorf("plan created = %q, want 2026-08-05", planning.Plans[0].Plan.Created)
	}

	// Plans by planning-state: fixed value order, plan:roadmap-2026 is
	// approved.
	wantByState := []StateCount{{"draft", 0}, {"approved", 1}, {"immutable", 0}}
	if !reflect.DeepEqual(planning.PlansByState, wantByState) {
		t.Errorf("PlansByState = %+v, want %+v", planning.PlansByState, wantByState)
	}
}

// TestPlanningProjectionHierarchy: a scope/epic line belongs under the
// plan its derives-from references resolve to (exact line-form match);
// lines deriving from no plan are orphans. Plans order by created date
// ascending — oldest commitment first, NOT alphabetical.
func TestPlanningProjectionHierarchy(t *testing.T) {
	// plan:foundation (created first) and plan:feature-wave-1 (created
	// later): alphabetical order would put feature-wave-1 before
	// foundation, but the created-date order puts foundation first.
	planFoundation := unitFixture(t, "ns", "plan", "foundation", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainPlanningState:  "approved",
		conformance.DomainExistenceState: "active",
	})
	planFoundation.Created = "2026-08-01"

	scpFoundation := unitFixture(t, "ns", "scp", "foundation", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainExistenceState: "active",
	}, exchange.Relationship{Type: "derives-from", Target: "plan:foundation"})
	scpFoundation.Created = "2026-08-02"

	planFeature := unitFixture(t, "ns", "plan", "feature-wave-1", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainPlanningState:  "approved",
		conformance.DomainExistenceState: "active",
	})
	planFeature.Created = "2026-08-10"

	scpFeature := unitFixture(t, "ns", "scp", "feature-mvp", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainExistenceState: "active",
	}, exchange.Relationship{Type: "derives-from", Target: "plan:feature-wave-1"})
	scpFeature.Created = "2026-08-11"

	// An orphan scope (created after both plans) and a draft epic
	// orphan (created before it) — orphan order is created ascending.
	scpOrphan := unitFixture(t, "ns", "scp", "unplanned", map[string]string{
		conformance.DomainContentState:   "draft",
		conformance.DomainExistenceState: "active",
	})
	scpOrphan.Created = "2026-08-12"

	epcOrphan := unitFixture(t, "ns", "epc", "draft-epic", map[string]string{
		conformance.DomainContentState:   "draft",
		conformance.DomainExistenceState: "active",
	})
	epcOrphan.Created = "2026-08-03"

	g := NewGraph(".", []*exchange.Unit{
		planFoundation, scpFoundation, planFeature, scpFeature, scpOrphan, epcOrphan,
	})
	proj, err := Build("planning", g, "")
	if err != nil {
		t.Fatalf("Build(planning): %v", err)
	}
	planning := proj.(*PlanningProjection)

	// Plan roots: created ascending — foundation (08-01) before
	// feature-wave-1 (08-10), the opposite of alphabetical order.
	if want := []string{"ns/plan:foundation", "ns/plan:feature-wave-1"}; !reflect.DeepEqual(planIdentities(planning), want) {
		t.Errorf("plans = %v, want %v (created ascending)", planIdentities(planning), want)
	}
	// Children hang under the plan they derive from.
	if want := []string{"ns/scp:foundation"}; !reflect.DeepEqual(childIdentities(planning.Plans[0]), want) {
		t.Errorf("foundation children = %v, want %v", childIdentities(planning.Plans[0]), want)
	}
	if want := []string{"ns/scp:feature-mvp"}; !reflect.DeepEqual(childIdentities(planning.Plans[1]), want) {
		t.Errorf("feature-wave-1 children = %v, want %v", childIdentities(planning.Plans[1]), want)
	}
	// Orphans: draft-epic (08-03) before unplanned (08-12).
	wantOrphans := []string{"ns/epc:draft-epic", "ns/scp:unplanned"}
	gotOrphans := make([]string, 0, len(planning.Orphans))
	for _, a := range planning.Orphans {
		gotOrphans = append(gotOrphans, a.Identity)
	}
	if !reflect.DeepEqual(gotOrphans, wantOrphans) {
		t.Errorf("orphans = %v, want %v", gotOrphans, wantOrphans)
	}
	// Plans by planning-state: both plans approved.
	wantByState := []StateCount{{"draft", 0}, {"approved", 2}, {"immutable", 0}}
	if !reflect.DeepEqual(planning.PlansByState, wantByState) {
		t.Errorf("PlansByState = %+v, want %+v", planning.PlansByState, wantByState)
	}
}

// TestPlanningProjectionSubPlan: a plan deriving from another plan
// nests under it as a sub-plan (one level); its scope/epic lines hang
// under the sub-plan, not the root; the remaining plans stay roots at
// the shared level.
func TestPlanningProjectionSubPlan(t *testing.T) {
	planFoundation := unitFixture(t, "ns", "plan", "foundation", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainPlanningState:  "approved",
		conformance.DomainExistenceState: "active",
	})
	planFoundation.Created = "2026-08-01"

	scpRoot := unitFixture(t, "ns", "scp", "root-a", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainExistenceState: "active",
	}, exchange.Relationship{Type: "derives-from", Target: "plan:foundation"})
	scpRoot.Created = "2026-08-02"

	planSub := unitFixture(t, "ns", "plan", "sub", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainPlanningState:  "approved",
		conformance.DomainExistenceState: "active",
	}, exchange.Relationship{Type: "derives-from", Target: "plan:foundation"})
	planSub.Created = "2026-08-05"

	scpSub := unitFixture(t, "ns", "scp", "sub-a", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainExistenceState: "active",
	}, exchange.Relationship{Type: "derives-from", Target: "plan:sub"})
	scpSub.Created = "2026-08-06"

	planOther := unitFixture(t, "ns", "plan", "other", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainPlanningState:  "approved",
		conformance.DomainExistenceState: "active",
	})
	planOther.Created = "2026-08-10"

	g := NewGraph(".", []*exchange.Unit{
		planFoundation, scpRoot, planSub, scpSub, planOther,
	})
	proj, err := Build("planning", g, "")
	if err != nil {
		t.Fatalf("Build(planning): %v", err)
	}
	planning := proj.(*PlanningProjection)

	// Roots: sub is lifted out — foundation and other share the level.
	if want := []string{"ns/plan:foundation", "ns/plan:other"}; !reflect.DeepEqual(planIdentities(planning), want) {
		t.Errorf("plans = %v, want %v (sub-plan lifted)", planIdentities(planning), want)
	}
	root := planning.Plans[0]
	// The root's own scope/epic children.
	if want := []string{"ns/scp:root-a"}; !reflect.DeepEqual(childIdentities(root), want) {
		t.Errorf("foundation children = %v, want %v", childIdentities(root), want)
	}
	// The sub-plan nests under its parent, with its own children.
	if len(root.SubPlans) != 1 || root.SubPlans[0].Plan.Identity != "ns/plan:sub" {
		t.Fatalf("foundation sub-plans = %v, want [ns/plan:sub]", subPlanIdentities(root))
	}
	if want := []string{"ns/scp:sub-a"}; !reflect.DeepEqual(childIdentities(root.SubPlans[0]), want) {
		t.Errorf("sub children = %v, want %v (scp:sub-a under the sub-plan)", childIdentities(root.SubPlans[0]), want)
	}
	if len(planning.Plans[1].SubPlans) != 0 {
		t.Errorf("other sub-plans = %v, want none", subPlanIdentities(planning.Plans[1]))
	}
	// Plans by planning-state counts every plan line (roots + sub).
	wantByState := []StateCount{{"draft", 0}, {"approved", 3}, {"immutable", 0}}
	if !reflect.DeepEqual(planning.PlansByState, wantByState) {
		t.Errorf("PlansByState = %+v, want %+v", planning.PlansByState, wantByState)
	}
}

// TestPlanningProjectionSortEdges: created-date ordering handles the
// deterministic fallbacks — an empty created date sorts first, and
// equal dates tie-break by canonical identity.
func TestPlanningProjectionSortEdges(t *testing.T) {
	mkPlan := func(id, created string, rels ...exchange.Relationship) *exchange.Unit {
		u := unitFixture(t, "ns", "plan", id, map[string]string{
			conformance.DomainContentState:   "draft",
			conformance.DomainPlanningState:  "draft",
			conformance.DomainExistenceState: "active",
		}, rels...)
		u.Created = created
		return u
	}
	// plan:zeta and plan:alpha share a created date (tie → identity);
	// plan:null carries no created date ("" sorts first).
	g := NewGraph(".", []*exchange.Unit{
		mkPlan("zeta", "2026-08-05"),
		mkPlan("alpha", "2026-08-05"),
		mkPlan("null", ""),
	})
	proj, err := Build("planning", g, "")
	if err != nil {
		t.Fatalf("Build(planning): %v", err)
	}
	planning := proj.(*PlanningProjection)
	want := []string{"ns/plan:null", "ns/plan:alpha", "ns/plan:zeta"}
	if !reflect.DeepEqual(planIdentities(planning), want) {
		t.Errorf("plans = %v, want %v (empty date first, identity tie-break)", planIdentities(planning), want)
	}
}

// TestPlanningProjectionEmptyDomain: a repository without Planning
// artifacts yields an empty projection — no plans, no orphans, no
// traceability, zero plan counts, still exit-0-shaped.
func TestPlanningProjectionEmptyDomain(t *testing.T) {
	g := loadFixture(t, "no-active")
	p, err := Build("planning", g, "")
	if err != nil {
		t.Fatalf("Build(planning): %v", err)
	}
	planning := p.(*PlanningProjection)
	if len(planning.Plans) != 0 || len(planning.Orphans) != 0 || len(planning.Traceability) != 0 {
		t.Errorf("empty projection must carry no artifact lines, got plans %v, orphans %v, traceability %v",
			planning.Plans, planning.Orphans, planning.Traceability)
	}
	for _, sc := range planning.PlansByState {
		if sc.Count != 0 {
			t.Errorf("PlansByState %q = %d, want 0", sc.State, sc.Count)
		}
	}
}
