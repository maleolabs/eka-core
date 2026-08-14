package view

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// validForm is the canonical identity form used across the valid
// fixture assertions.
const validForm = "eka-view-fixture/"

func TestGraphBuild(t *testing.T) {
	g := loadFixture(t, "valid")

	if g.Root() != "." {
		t.Errorf("Root() = %q, want %q", g.Root(), ".")
	}

	// The identity index resolves canonical line forms.
	if a := g.ByLineForm(validForm + "ctr:wave-1"); a == nil {
		t.Error("ByLineForm(ctr:wave-1) must resolve")
	} else if a.Identity.Type != "ctr" || a.Identity.ID != "wave-1" {
		t.Errorf("ByLineForm(ctr:wave-1) = %s/%s:%s", a.Identity.Namespace, a.Identity.Type, a.Identity.ID)
	}
	if a := g.ByLineForm(validForm + "tkt:ts-gamma"); a == nil || a.Identity.Type != "tkt" {
		t.Error("ByLineForm(tkt:ts-gamma) must resolve to the ticket line")
	}
	if a := g.ByLineForm(validForm + "sto:ghost"); a != nil {
		t.Error("ByLineForm of an unknown line must be nil")
	}

	// Relationship resolution: a reference parses and resolves within
	// the referrer's namespace.
	ref, err := conformance.ParseReference("ts:gamma", "eka-view-fixture", "tkt")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if a := g.Resolve(ref); a == nil || a.Identity.Type != "ts" || a.Identity.ID != "gamma" {
		t.Errorf("Resolve(ts:gamma) = %+v, want ts:gamma", a)
	}
	versioned, err := conformance.ParseReference("ctr:wave-1:1", "eka-view-fixture", "tkt")
	if err != nil {
		t.Fatalf("ParseReference versioned: %v", err)
	}
	if a := g.Resolve(versioned); a == nil || a.Identity.InstanceVersion != 1 {
		t.Errorf("Resolve(ctr:wave-1:1) must resolve the exact instance")
	}

	// Active container: wave-1, exactly one.
	container, multiple := g.ActiveContainer()
	if container == nil || container.ID != "wave-1" || container.State != "active" {
		t.Fatalf("ActiveContainer() = %+v, want wave-1/active", container)
	}
	if multiple {
		t.Error("ActiveContainer() must not report multiple active containers")
	}

	// Containers, sorted by canonical identity.
	wantContainers := []string{validForm + "ctr:wave-0", validForm + "ctr:wave-1"}
	gotContainers := make([]string, 0, len(g.Containers()))
	for _, c := range g.Containers() {
		gotContainers = append(gotContainers, c.Identity)
	}
	if !reflect.DeepEqual(gotContainers, wantContainers) {
		t.Errorf("Containers() = %v, want %v", gotContainers, wantContainers)
	}

	// Tickets deriving from the active container, sorted. Eight tickets:
	// the five regular ones plus tkt:sto-alpha-dup and tkt:sto-beta-multi
	// (dedup / first-resolvable-wins fixtures) and tkt:unresolved.
	wantTickets := []string{
		validForm + "tkt:bug-delta",
		validForm + "tkt:ch-epsilon",
		validForm + "tkt:sto-alpha",
		validForm + "tkt:sto-alpha-dup",
		validForm + "tkt:sto-beta",
		validForm + "tkt:sto-beta-multi",
		validForm + "tkt:ts-gamma",
		validForm + "tkt:unresolved",
	}
	gotTickets := make([]string, 0, 8)
	for _, tkt := range g.TicketsForContainer(validForm + "ctr:wave-1") {
		gotTickets = append(gotTickets, tkt.Identity)
	}
	if !reflect.DeepEqual(gotTickets, wantTickets) {
		t.Errorf("TicketsForContainer(wave-1) = %v, want %v", gotTickets, wantTickets)
	}

	// Work items of the active container, deduplicated and sorted: the
	// duplicate ticket for sto:alpha must not double the member.
	wantItems := []string{
		validForm + "bug:delta",
		validForm + "ch:epsilon",
		validForm + "sto:alpha",
		validForm + "sto:beta",
		validForm + "ts:gamma",
	}
	gotItems := make([]string, 0, 5)
	for _, wi := range g.WorkItemsForContainer(validForm + "ctr:wave-1") {
		gotItems = append(gotItems, wi.Identity)
	}
	if !reflect.DeepEqual(gotItems, wantItems) {
		t.Errorf("WorkItemsForContainer(wave-1) = %v, want %v", gotItems, wantItems)
	}

	// Ticket -> work item and ticket -> container resolution.
	ticket := g.ByLineForm(validForm + "tkt:ts-gamma")
	wi := g.WorkItemForTicket(ticket)
	if wi == nil || wi.Identity != validForm+"ts:gamma" || wi.State != "in-progress" {
		t.Errorf("WorkItemForTicket(ts-gamma) = %+v, want ts:gamma/in-progress", wi)
	}
	c := g.ContainerForTicket(ticket)
	if c == nil || c.Identity != validForm+"ctr:wave-1" {
		t.Errorf("ContainerForTicket(ts-gamma) = %+v, want ctr:wave-1", c)
	}

	// Target resolution forms.
	for target, wantID := range map[string]string{
		"tkt-ts-gamma":  "ts-gamma",
		"ts-gamma":      "ts-gamma",
		"tkt:bug-delta": "bug-delta",
	} {
		if a := g.TicketByTarget(target); a == nil || a.Identity.ID != wantID {
			t.Errorf("TicketByTarget(%q) = %+v, want id %q", target, a, wantID)
		}
	}
	if a := g.TicketByTarget("tkt-ghost"); a != nil {
		t.Errorf("TicketByTarget(tkt-ghost) = %+v, want nil", a)
	}

	// Ticket ids are the available targets, sorted by identity.
	wantIDs := []string{"bug-delta", "ch-epsilon", "sto-alpha", "sto-alpha-dup", "sto-beta", "sto-beta-multi", "sto-legacy", "ts-gamma", "unresolved"}
	if got := g.TicketIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("TicketIDs() = %v, want %v", got, wantIDs)
	}
}

// TestGraphDedupByIdentity: two tickets deriving from the same container
// may reference the same work item; membership deduplicates by identity
// line so the work item appears exactly once. Regression: the original
// fixture carried one ticket per work item, so dedup was never exercised.
func TestGraphDedupByIdentity(t *testing.T) {
	g := loadFixture(t, "valid")
	alpha := validForm + "sto:alpha"

	a := g.ByLineForm(validForm + "tkt:sto-alpha")
	dup := g.ByLineForm(validForm + "tkt:sto-alpha-dup")
	if a == nil || dup == nil {
		t.Fatal("fixture must carry tkt:sto-alpha and tkt:sto-alpha-dup")
	}
	for name, tkt := range map[string]*exchange.Unit{"tkt:sto-alpha": a, "tkt:sto-alpha-dup": dup} {
		if _, wi := g.ticketTargets(tkt); wi == nil || wi.Identity != alpha {
			t.Errorf("%s must resolve to sto:alpha, got %+v", name, wi)
		}
	}

	items := g.WorkItemsForContainer(validForm + "ctr:wave-1")
	n := 0
	for _, wi := range items {
		if wi.Identity == alpha {
			n++
		}
	}
	if n != 1 {
		t.Errorf("sto:alpha appears %d times, want exactly 1 (dedup by identity)", n)
	}
	if !reflect.DeepEqual(items, g.WorkItemsForContainer(validForm+"ctr:wave-1")) {
		t.Error("WorkItemsForContainer is not deterministic across repeated calls")
	}
}

// TestTicketTargetsFirstResolvableWins: a ticket with several work item
// references resolves deterministically to the FIRST resolvable one in
// file order. Regression: the original fixture's tickets each referenced
// a single work item.
func TestTicketTargetsFirstResolvableWins(t *testing.T) {
	g := loadFixture(t, "valid")
	tkt := g.ByLineForm(validForm + "tkt:sto-beta-multi")
	if tkt == nil {
		t.Fatal("fixture must carry tkt:sto-beta-multi")
	}
	container, workItem := g.ticketTargets(tkt)
	if container == nil || container.Identity != validForm+"ctr:wave-1" {
		t.Errorf("container = %+v, want ctr:wave-1", container)
	}
	if workItem == nil || workItem.Identity != validForm+"sto:beta" || workItem.State != "todo" {
		t.Errorf("work item = %+v, want sto:beta/todo (first resolvable reference wins)", workItem)
	}
	// Deterministic: repeated resolution picks the same targets.
	c2, w2 := g.ticketTargets(tkt)
	if !reflect.DeepEqual(container, c2) || !reflect.DeepEqual(workItem, w2) {
		t.Error("ticketTargets is not deterministic")
	}
	// The execution projection surfaces the same deterministic pick.
	p, err := Build("execution", g, "")
	if err != nil {
		t.Fatalf("Build(execution): %v", err)
	}
	exec := p.(*ExecutionProjection)
	for _, tkt := range exec.Tickets {
		if tkt.ID == "sto-beta-multi" && tkt.Projected != "todo" {
			t.Errorf("tkt:sto-beta-multi projects %q, want todo (sto:beta wins over ts:gamma)", tkt.Projected)
		}
	}
}

// TestFixtureConforms verifies every view fixture passes the conformance
// rules: the CLI gate refuses non-conformant repositories, so the
// fixtures must stay compliant.
func TestFixtureConforms(t *testing.T) {
	for _, name := range []string{"valid", "no-active", "multi-active"} {
		report, err := conformance.Validate(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("validate fixture %s: %v", name, err)
		}
		if !report.Pass() {
			t.Errorf("fixture %s must be conformant, got %d errors:", name, report.ErrorCount())
			for _, res := range report.SortedResults() {
				t.Errorf("  [%s] %s %s: %s", res.Severity, res.Rule, res.File, res.Message)
			}
		}
	}
}

// TestGraphBuildDeterministic verifies that identical graphs produce
// identical model slices.
func TestGraphBuildDeterministic(t *testing.T) {
	a, b := loadFixture(t, "valid"), loadFixture(t, "valid")
	if !reflect.DeepEqual(a.Units(), b.Units()) {
		t.Error("Units() differs between identical graphs")
	}
	if !reflect.DeepEqual(a.TicketsForContainer(validForm+"ctr:wave-1"), b.TicketsForContainer(validForm+"ctr:wave-1")) {
		t.Error("TicketsForContainer differs between identical graphs")
	}
	if !reflect.DeepEqual(a.WorkItemsForContainer(validForm+"ctr:wave-1"), b.WorkItemsForContainer(validForm+"ctr:wave-1")) {
		t.Error("WorkItemsForContainer differs between identical graphs")
	}
}

func TestGraphNoActiveContainer(t *testing.T) {
	g := loadFixture(t, "no-active")
	container, multiple := g.ActiveContainer()
	if container != nil {
		t.Errorf("ActiveContainer() = %+v, want nil", container)
	}
	if multiple {
		t.Error("no active container must not report multiple")
	}
	if got := g.TicketsForContainer(validForm + "ctr:wave-0"); len(got) != 1 {
		t.Errorf("completed container keeps its ticket: got %d, want 1", len(got))
	}
}

func TestGraphMultipleActiveContainers(t *testing.T) {
	g := loadFixture(t, "multi-active")
	container, multiple := g.ActiveContainer()
	if container == nil {
		t.Fatal("ActiveContainer() must pick a container")
	}
	if !multiple {
		t.Error("two active containers must report multiple")
	}
	// Lexicographically smallest canonical identity wins: wave-1 < wave-2.
	if container.ID != "wave-1" {
		t.Errorf("ActiveContainer() = %q, want the lexicographically smallest (wave-1)", container.ID)
	}
}

// TestActiveContainerLatestInstance: the active-container decision
// reads the line's HIGHEST instance — a container revised from active
// to completed is no longer active (ADR-025); the stale first instance
// must never decide.
func TestActiveContainerLatestInstance(t *testing.T) {
	// wave-1: v1 active, v2 completed — the line is completed now.
	v1 := unitFixture(t, "probe", "ctr", "wave-1", map[string]string{
		conformance.DomainContainerState: "active",
		conformance.DomainExistenceState: "active",
	})
	v1.Identity.InstanceVersion = 1
	v1.CanonicalIdentityForm = "probe/ctr:wave-1:1"
	v2 := unitFixture(t, "probe", "ctr", "wave-1", map[string]string{
		conformance.DomainContainerState: "completed",
		conformance.DomainExistenceState: "active",
	})
	v2.Identity.InstanceVersion = 2
	v2.CanonicalIdentityForm = "probe/ctr:wave-1:2"

	g := NewGraph(".", []*exchange.Unit{v1, v2})
	if container, multiple := g.ActiveContainer(); container != nil || multiple {
		t.Errorf("ActiveContainer() = %+v/%v, want nil/false (the latest instance completed the line)", container, multiple)
	}

	// A second single-instance active container resolves.
	wave2 := unitFixture(t, "probe", "ctr", "wave-2", map[string]string{
		conformance.DomainContainerState: "active",
		conformance.DomainExistenceState: "active",
	})
	g = NewGraph(".", []*exchange.Unit{v1, v2, wave2})
	if container, multiple := g.ActiveContainer(); container == nil || container.ID != "wave-2" || multiple {
		t.Errorf("ActiveContainer() = %+v/%v, want wave-2/false", container, multiple)
	}
}

// TestContainersLineLevel: Containers() deduplicates by identity line —
// one entry per container line, the highest instance (ADR-025), never
// one entry per revision.
func TestContainersLineLevel(t *testing.T) {
	v1 := unitFixture(t, "probe", "ctr", "wave-1", map[string]string{
		conformance.DomainContainerState: "active",
		conformance.DomainExistenceState: "active",
	})
	v1.Identity.InstanceVersion = 1
	v1.CanonicalIdentityForm = "probe/ctr:wave-1:1"
	v2 := unitFixture(t, "probe", "ctr", "wave-1", map[string]string{
		conformance.DomainContainerState: "completed",
		conformance.DomainExistenceState: "active",
	})
	v2.Identity.InstanceVersion = 2
	v2.CanonicalIdentityForm = "probe/ctr:wave-1:2"

	g := NewGraph(".", []*exchange.Unit{v1, v2})
	cs := g.Containers()
	if len(cs) != 1 {
		t.Fatalf("Containers() = %d entries, want 1 line (v1 and v2 collapse)", len(cs))
	}
	if cs[0].Identity != "probe/ctr:wave-1" || cs[0].State != "completed" {
		t.Errorf("Containers()[0] = %+v, want probe/ctr:wave-1/completed (the highest instance)", cs[0])
	}
}

// TestTicketsForContainerLineLevel: tickets are deduplicated by
// identity line — a revised ticket appears once, the highest instance
// (ADR-025).
func TestTicketsForContainerLineLevel(t *testing.T) {
	ctr := unitFixture(t, "probe", "ctr", "wave-1", map[string]string{
		conformance.DomainContainerState: "active",
		conformance.DomainExistenceState: "active",
	})
	sto := unitFixture(t, "probe", "sto", "alpha", map[string]string{
		conformance.DomainExecutionState: "planned",
	})
	tktV1 := unitFixture(t, "probe", "tkt", "ts-1", nil,
		exchange.Relationship{Type: "derives-from", Target: "ctr:wave-1"},
		exchange.Relationship{Type: "derives-from", Target: "sto:alpha"})
	tktV1.Identity.InstanceVersion = 1
	tktV1.CanonicalIdentityForm = "probe/tkt:ts-1:1"
	tktV2 := unitFixture(t, "probe", "tkt", "ts-1", nil,
		exchange.Relationship{Type: "derives-from", Target: "ctr:wave-1"},
		exchange.Relationship{Type: "derives-from", Target: "sto:alpha"})
	tktV2.Identity.InstanceVersion = 2
	tktV2.CanonicalIdentityForm = "probe/tkt:ts-1:2"

	g := NewGraph(".", []*exchange.Unit{ctr, sto, tktV1, tktV2})
	ts := g.TicketsForContainer("probe/ctr:wave-1")
	if len(ts) != 1 {
		t.Fatalf("TicketsForContainer() = %d tickets, want 1 line (v1 and v2 collapse)", len(ts))
	}
	if ts[0].Identity != "probe/tkt:ts-1" {
		t.Errorf("ticket identity = %q, want probe/tkt:ts-1", ts[0].Identity)
	}
}

// TestTicketByTargetLatestInstance: the ticket projection resolves a
// target to the line's HIGHEST instance — the current knowledge, not
// the original revision (ADR-025).
func TestTicketByTargetLatestInstance(t *testing.T) {
	ctr := unitFixture(t, "probe", "ctr", "wave-1", map[string]string{
		conformance.DomainContainerState: "active",
		conformance.DomainExistenceState: "active",
	})
	sto := unitFixture(t, "probe", "sto", "alpha", map[string]string{
		conformance.DomainExecutionState: "planned",
	})
	tktV1 := unitFixture(t, "probe", "tkt", "ts-1", nil,
		exchange.Relationship{Type: "derives-from", Target: "ctr:wave-1"},
		exchange.Relationship{Type: "derives-from", Target: "sto:alpha"})
	tktV1.Identity.InstanceVersion = 1
	tktV1.CanonicalIdentityForm = "probe/tkt:ts-1:1"
	tktV2 := unitFixture(t, "probe", "tkt", "ts-1", nil,
		exchange.Relationship{Type: "derives-from", Target: "ctr:wave-1"},
		exchange.Relationship{Type: "derives-from", Target: "sto:alpha"})
	tktV2.Identity.InstanceVersion = 2
	tktV2.CanonicalIdentityForm = "probe/tkt:ts-1:2"

	g := NewGraph(".", []*exchange.Unit{ctr, sto, tktV1, tktV2})
	u := g.TicketByTarget("ts-1")
	if u == nil {
		t.Fatal("TicketByTarget(ts-1) must resolve")
	}
	if u.Identity.InstanceVersion != 2 {
		t.Errorf("TicketByTarget(ts-1) instance = %d, want 2 (the highest instance, ADR-025)", u.Identity.InstanceVersion)
	}
	if got := g.TicketIDs(); !reflect.DeepEqual(got, []string{"ts-1"}) {
		t.Errorf("TicketIDs() = %v, want [ts-1] (one id per line)", got)
	}
}

// TestReferenceForm pins the relationship-target presentation
// convention: same-namespace targets render in the authoring line form,
// with the instance version appended EXACTLY when the target is not the
// line's highest instance (omitting it would change resolution);
// cross-namespace targets render in the full canonical form.
func TestReferenceForm(t *testing.T) {
	// Multi-instance line plan:roadmap-v1 (v1, v2) + single-instance
	// lines ctr:wave-1 and the cross-namespace sto:y.
	g := NewGraph(".", []*exchange.Unit{
		{Identity: exchange.Identity{Namespace: "feather", Type: "plan", ID: "roadmap-v1", InstanceVersion: 1}},
		{Identity: exchange.Identity{Namespace: "feather", Type: "plan", ID: "roadmap-v1", InstanceVersion: 2}},
		{Identity: exchange.Identity{Namespace: "feather", Type: "ctr", ID: "wave-1", InstanceVersion: 1}},
	})
	u := &exchange.Unit{Identity: exchange.Identity{Namespace: "feather", Type: "sto", ID: "x", InstanceVersion: 1}}
	cases := []struct {
		name   string
		target string
		want   string
	}{
		{"single-instance line", "feather/ctr:wave-1:1", "ctr:wave-1"},
		{"unversioned target", "feather/plan:roadmap-v1", "plan:roadmap-v1"},
		{"oldest instance of multi line", "feather/plan:roadmap-v1:1", "plan:roadmap-v1:1"},
		{"highest instance of multi line", "feather/plan:roadmap-v1:2", "plan:roadmap-v1"},
		{"cross-ns", "other/sto:y:1", "other/sto:y:1"},
	}
	for _, tc := range cases {
		if got := g.referenceForm(u, tc.target); got != tc.want {
			t.Errorf("%s: referenceForm(%q) = %q, want %q", tc.name, tc.target, got, tc.want)
		}
	}
}

// TestReferenceFormLosslessForResolution: re-parsing the rendered form
// with the unit's namespace resolves to the SAME instance as the raw
// canonical target — unversioned renders resolve to the lowest
// instance (which is the target), versioned renders resolve exactly.
func TestReferenceFormLosslessForResolution(t *testing.T) {
	g := NewGraph(".", []*exchange.Unit{
		{Identity: exchange.Identity{Namespace: "feather", Type: "ctr", ID: "wave-1", InstanceVersion: 1}},
		{Identity: exchange.Identity{Namespace: "feather", Type: "plan", ID: "roadmap-v1", InstanceVersion: 1}},
		{Identity: exchange.Identity{Namespace: "feather", Type: "plan", ID: "roadmap-v1", InstanceVersion: 2}},
		{Identity: exchange.Identity{Namespace: "other", Type: "sto", ID: "y", InstanceVersion: 3}},
	})
	u := &exchange.Unit{Identity: exchange.Identity{Namespace: "feather", Type: "rel", ID: "v090", InstanceVersion: 1}}
	for _, target := range []string{
		"feather/ctr:wave-1:1",
		"feather/plan:roadmap-v1:1",
		"feather/plan:roadmap-v1:2",
		"feather/plan:roadmap-v1", // unversioned: must render without a ":0" suffix
		"other/sto:y:3",
	} {
		rendered := g.referenceForm(u, target)
		wantRef, err := conformance.ParseReference(target, u.Identity.Namespace, u.Identity.Type)
		if err != nil {
			t.Fatalf("parse %q: %v", target, err)
		}
		gotRef, err := conformance.ParseReference(rendered, u.Identity.Namespace, u.Identity.Type)
		if err != nil {
			t.Fatalf("parse rendered %q: %v", rendered, err)
		}
		want := g.Resolve(wantRef)
		got := g.Resolve(gotRef)
		if got == nil || want == nil || got.CanonicalIdentityForm != want.CanonicalIdentityForm {
			t.Errorf("rendered %q from %q resolves to %v, want %v (target %q)",
				rendered, target, formOf(got), formOf(want), target)
		}
	}
}

// formOf renders a unit's canonical identity form ("" for nil).
func formOf(u *exchange.Unit) string {
	if u == nil {
		return ""
	}
	return u.CanonicalIdentityForm
}
