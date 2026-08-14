package view

import (
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// TestBuildContainersFixture: the containers projection over the valid
// fixture — two container lines (wave-0 completed, wave-1 active), the
// per-container tickets and work items derived from the tickets'
// derives-from, sorted by canonical identity.
func TestBuildContainersFixture(t *testing.T) {
	g := loadFixture(t, "valid")
	proj, err := Build("containers", g, "")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := proj.(*ContainersProjection)
	if !ok {
		t.Fatalf("Build(containers) = %T, want *ContainersProjection", proj)
	}
	if p.Name() != "containers" {
		t.Errorf("Name() = %q, want containers", p.Name())
	}
	if p.Total != 2 || p.Active != 1 {
		t.Errorf("total/active = %d/%d, want 2/1", p.Total, p.Active)
	}
	if len(p.Containers) != 2 {
		t.Fatalf("containers = %d, want 2", len(p.Containers))
	}
	wantForms := []string{"eka-view-fixture/ctr:wave-0", "eka-view-fixture/ctr:wave-1"}
	for i, w := range wantForms {
		if p.Containers[i].Identity != w {
			t.Errorf("containers[%d].Identity = %q, want %q (sorted)", i, p.Containers[i].Identity, w)
		}
	}
	wave0 := p.Containers[0]
	if wave0.State != "completed" || wave0.Items != 1 || wave0.Tickets != 1 || wave0.StartedAt != "2026-08-05" {
		t.Errorf("wave-0 = %+v, want completed with 1 item, 1 ticket, started 2026-08-05", wave0)
	}
	wave1 := p.Containers[1]
	if wave1.State != "active" || wave1.Items != 5 || wave1.Tickets != 8 || wave1.StartedAt != "2026-08-05" {
		t.Errorf("wave-1 = %+v, want active with 5 items, 8 tickets, started 2026-08-05", wave1)
	}
	// The fixture containers carry no depends-on and no
	// active -> completed transition.
	if wave0.Plan != "" || wave0.EndedAt != "" || wave1.Plan != "" || wave1.EndedAt != "" {
		t.Errorf("plan/endedAt must be empty for the fixture, got %q/%q and %q/%q",
			wave0.Plan, wave0.EndedAt, wave1.Plan, wave1.EndedAt)
	}
}

// containersDetailedFixture builds a hand-made graph with a plan and a
// completion date: wave-7 active with a plan and two tickets, wave-0
// completed with the active -> completed transition, plus a second
// instance of the wave-7 line (the projection must deduplicate by
// line, highest instance wins — the v2 sibling carries no depends-on,
// so the line projects without a plan).
func containersDetailedFixture(t *testing.T) *Graph {
	t.Helper()
	wave7 := unitFixture(t, "acme", "ctr", "wave-7", map[string]string{
		conformance.DomainContainerState: "active",
		conformance.DomainExistenceState: "active",
	}, exchange.Relationship{Type: "depends-on", Target: "plan:roadmap-2026:1"})
	wave7.Created = "2026-08-05"
	wave7v2 := unitFixture(t, "acme", "ctr", "wave-7", map[string]string{
		conformance.DomainContainerState: "active",
		conformance.DomainExistenceState: "active",
	})
	wave7v2.Identity.InstanceVersion = 2
	wave7v2.CanonicalIdentityForm = "acme/ctr:wave-7:2"
	wave7v2.Created = "2026-08-06"
	wave0 := unitFixture(t, "acme", "ctr", "wave-0", map[string]string{
		conformance.DomainContainerState: "completed",
		conformance.DomainExistenceState: "active",
	})
	wave0.Created = "2026-08-01"
	wave0.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-01", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		{Date: "2026-08-01", Domain: "container-state", From: "-", To: "active", By: conformance.User("Engineering")},
		{Date: "2026-08-04", Domain: "container-state", From: "active", To: "completed", By: conformance.User("Engineering")},
	}
	plan := unitFixture(t, "acme", "plan", "roadmap-2026", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainPlanningState:  "approved",
		conformance.DomainExistenceState: "active",
	})
	units := []*exchange.Unit{wave7, wave7v2, wave0, plan,
		unitFixture(t, "acme", "sto", "alpha", map[string]string{conformance.DomainExecutionState: "planned"}),
		unitFixture(t, "acme", "ts", "gamma", map[string]string{conformance.DomainExecutionState: "in-progress"}),
		unitFixture(t, "acme", "tkt", "sto-alpha", nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-7"},
			exchange.Relationship{Type: "derives-from", Target: "sto:alpha"}),
		unitFixture(t, "acme", "tkt", "ts-gamma", nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-7"},
			exchange.Relationship{Type: "derives-from", Target: "ts:gamma"}),
	}
	return NewGraph(".", units)
}

// TestBuildContainersDetailed: the projection renders the plan in the
// authoring reference convention and the completion date from the
// container-state active -> completed change-log entry; multi-instance
// container lines are deduplicated (highest instance wins).
func TestBuildContainersDetailed(t *testing.T) {
	proj, err := Build("containers", containersDetailedFixture(t), "")
	if err != nil {
		t.Fatal(err)
	}
	p := proj.(*ContainersProjection)
	if p.Total != 2 || p.Active != 1 {
		t.Fatalf("total/active = %d/%d, want 2/1 (wave-7 v2 deduplicated)", p.Total, p.Active)
	}
	wave7 := p.Containers[1]
	if wave7.Identity != "acme/ctr:wave-7" || wave7.ID != "wave-7" {
		t.Errorf("wave-7 identity = %q/%q", wave7.Identity, wave7.ID)
	}
	// Plan: the highest instance (v2) carries no depends-on — the
	// relationship lives in v1, so the line projects without a plan
	// (latest-wins semantics, ADR-025).
	if wave7.Plan != "" {
		t.Errorf("wave-7 plan = %q, want \"\" (highest instance carries no depends-on)", wave7.Plan)
	}
	if wave7.Items != 2 || wave7.Tickets != 2 {
		t.Errorf("wave-7 items/tickets = %d/%d, want 2/2", wave7.Items, wave7.Tickets)
	}
	// The highest instance wins: its created date, not v1's.
	if wave7.StartedAt != "2026-08-06" {
		t.Errorf("wave-7 startedAt = %q, want 2026-08-06 (the highest instance)", wave7.StartedAt)
	}
	if wave7.EndedAt != "" || wave7.State != "active" {
		t.Errorf("wave-7 ended/state = %q/%q, want \"\"/active", wave7.EndedAt, wave7.State)
	}
	wave0 := p.Containers[0]
	if wave0.EndedAt != "2026-08-04" || wave0.State != "completed" {
		t.Errorf("wave-0 ended/state = %q/%q, want 2026-08-04/completed", wave0.EndedAt, wave0.State)
	}
	if wave0.Items != 0 || wave0.Tickets != 0 {
		t.Errorf("wave-0 items/tickets = %d/%d, want 0/0", wave0.Items, wave0.Tickets)
	}
}

// TestBuildContainersSortStartedAt: containers order by started date
// ascending ("" first — deterministic), tie-broken by canonical
// identity — the table reads oldest wave first, regardless of
// alphabetical identity order.
func TestBuildContainersSortStartedAt(t *testing.T) {
	mkCtr := func(id, state, created string) *exchange.Unit {
		u := unitFixture(t, "acme", "ctr", id, map[string]string{
			conformance.DomainContainerState: state,
			conformance.DomainExistenceState: "active",
		})
		u.Created = created
		return u
	}
	// zulu starts before alpha — identity order would say alpha first.
	zulu := mkCtr("zulu", "completed", "2026-08-01")
	alpha := mkCtr("alpha", "active", "2026-08-02")
	// No created date sorts first (deterministic fallback).
	noDate := mkCtr("nodate", "planned", "")
	g := NewGraph(".", []*exchange.Unit{zulu, alpha, noDate})
	proj, err := Build("containers", g, "")
	if err != nil {
		t.Fatal(err)
	}
	p := proj.(*ContainersProjection)
	if p.Total != 3 || p.Active != 1 {
		t.Fatalf("total/active = %d/%d, want 3/1", p.Total, p.Active)
	}
	want := []string{"acme/ctr:nodate", "acme/ctr:zulu", "acme/ctr:alpha"}
	for i, w := range want {
		if p.Containers[i].Identity != w {
			t.Errorf("containers[%d].Identity = %q, want %q (started ascending)", i, p.Containers[i].Identity, w)
		}
	}
}

// TestContainersProjectionPage: Page windows the Containers slice;
// Total (and Active) stay the full population for the footer.
func TestContainersProjectionPage(t *testing.T) {
	proj, err := Build("containers", containersDetailedFixture(t), "")
	if err != nil {
		t.Fatal(err)
	}
	p := proj.(*ContainersProjection)
	p.Page(1, 1)
	if len(p.Containers) != 1 || p.Containers[0].Identity != "acme/ctr:wave-7" {
		t.Errorf("page 1 of limit 1 = %v, want only acme/ctr:wave-7", p.Containers)
	}
	if p.Total != 2 || p.Active != 1 {
		t.Errorf("total/active after Page = %d/%d, want the full 2/1", p.Total, p.Active)
	}
	p.Page(5, 2)
	if len(p.Containers) != 0 {
		t.Errorf("offset past the end must yield an empty window, got %v", p.Containers)
	}
	if p.Total != 2 {
		t.Errorf("total after an empty window = %d, want 2", p.Total)
	}
}

// TestContainersProjectionEmpty: a graph without containers yields an
// empty projection — total 0, exit-0 shape.
func TestContainersProjectionEmpty(t *testing.T) {
	g := NewGraph(".", nil)
	proj, err := Build("containers", g, "")
	if err != nil {
		t.Fatal(err)
	}
	p := proj.(*ContainersProjection)
	if p.Total != 0 || p.Active != 0 || len(p.Containers) != 0 {
		t.Errorf("empty projection = total %d, active %d, containers %d — want all zero",
			p.Total, p.Active, len(p.Containers))
	}
}
