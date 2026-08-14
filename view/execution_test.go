package view

import (
	"reflect"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// columnIDs returns the identities of one column.
func columnIDs(col StateColumn) []string {
	out := make([]string, 0, len(col.WorkItems))
	for _, wi := range col.WorkItems {
		out = append(out, wi.Identity)
	}
	return out
}

func TestExecutionProjection(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("execution", g, "")
	if err != nil {
		t.Fatalf("Build(execution): %v", err)
	}
	exec, ok := p.(*ExecutionProjection)
	if !ok {
		t.Fatalf("Build(execution) = %T, want *ExecutionProjection", p)
	}
	if exec.Container == nil || exec.Container.Identity != validForm+"ctr:wave-1" {
		t.Fatalf("Container = %+v, want ctr:wave-1", exec.Container)
	}
	if exec.MultipleActive {
		t.Error("MultipleActive must be false on the valid fixture")
	}
	if len(exec.Tickets) != 8 {
		t.Errorf("Tickets = %d, want 8 (seven resolved + one unresolved)", len(exec.Tickets))
	}
	if exec.Total != 5 {
		t.Errorf("Total = %d, want 5 (dedup keeps each work item once)", exec.Total)
	}

	// Fixed column order (six values since ADR-019: canceled added).
	wantOrder := []string{"planned", "todo", "in-progress", "in-review", "done", "canceled"}
	gotOrder := make([]string, len(exec.Columns))
	for i, col := range exec.Columns {
		gotOrder[i] = col.State
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("column order = %v, want %v", gotOrder, wantOrder)
	}

	// Per-column membership.
	cases := map[string][]string{
		"planned":     {validForm + "sto:alpha"},
		"todo":        {validForm + "sto:beta"},
		"in-progress": {validForm + "ts:gamma"},
		"in-review":   {validForm + "bug:delta"},
		"done":        {validForm + "ch:epsilon"},
		"canceled":    {},
	}
	for _, col := range exec.Columns {
		want := cases[col.State]
		if got := columnIDs(col); !reflect.DeepEqual(got, want) {
			t.Errorf("column %q = %v, want %v", col.State, got, want)
		}
	}

	// Counts derive from the columns.
	for state, n := range map[string]int{"planned": 1, "todo": 1, "in-progress": 1, "in-review": 1, "done": 1, "canceled": 0} {
		if got := exec.Columns.Count(state); got != n {
			t.Errorf("Count(%q) = %d, want %d", state, got, n)
		}
	}
	if got := exec.Columns.Count("nope"); got != 0 {
		t.Errorf("Count(unknown) = %d, want 0", got)
	}

	// Work items carry the owner state and informational dimension.
	for _, col := range exec.Columns {
		for _, wi := range col.WorkItems {
			if wi.State != col.State {
				t.Errorf("work item %s in column %q but owns state %q", wi.Identity, col.State, wi.State)
			}
		}
	}
	var alpha *WorkItem
	for _, col := range exec.Columns {
		for i := range col.WorkItems {
			if col.WorkItems[i].ID == "alpha" {
				alpha = &col.WorkItems[i]
			}
		}
	}
	if alpha == nil || !alpha.HasDimension || alpha.Dimension != "operations" {
		t.Errorf("sto:alpha must carry its informational dimension, got %+v", alpha)
	}
}

// TestExecutionProjectionTickets verifies the ticket block of the
// merged projection: tickets deriving from the active container, sorted
// by identity, each carrying the status projected from its work item.
// The duplicate ticket (sto-alpha-dup) and the multi-reference ticket
// (sto-beta-multi, first resolvable reference wins) are members too.
func TestExecutionProjectionTickets(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("execution", g, "")
	if err != nil {
		t.Fatalf("Build(execution): %v", err)
	}
	exec := p.(*ExecutionProjection)
	wantTickets := []Ticket{
		{Identity: validForm + "tkt:bug-delta", Type: "tkt", ID: "bug-delta", Projected: "in-review"},
		{Identity: validForm + "tkt:ch-epsilon", Type: "tkt", ID: "ch-epsilon", Projected: "done"},
		{Identity: validForm + "tkt:sto-alpha", Type: "tkt", ID: "sto-alpha", Projected: "planned"},
		{Identity: validForm + "tkt:sto-alpha-dup", Type: "tkt", ID: "sto-alpha-dup", Projected: "planned"},
		{Identity: validForm + "tkt:sto-beta", Type: "tkt", ID: "sto-beta", Projected: "todo"},
		{Identity: validForm + "tkt:sto-beta-multi", Type: "tkt", ID: "sto-beta-multi", Projected: "todo"},
		{Identity: validForm + "tkt:ts-gamma", Type: "tkt", ID: "ts-gamma", Projected: "in-progress"},
		{Identity: validForm + "tkt:unresolved", Type: "tkt", ID: "unresolved", Projected: "unresolved"},
	}
	if !reflect.DeepEqual(exec.Tickets, wantTickets) {
		t.Errorf("Tickets = %+v\nwant %+v", exec.Tickets, wantTickets)
	}
}

func TestExecutionProjectionNoActiveContainer(t *testing.T) {
	g := loadFixture(t, "no-active")
	p, err := Build("execution", g, "")
	if err != nil {
		t.Fatalf("Build(execution): %v", err)
	}
	exec := p.(*ExecutionProjection)
	if exec.Container != nil {
		t.Errorf("Container = %+v, want nil", exec.Container)
	}
	if exec.MultipleActive {
		t.Error("MultipleActive must be false")
	}
	if len(exec.Tickets) != 0 {
		t.Errorf("Tickets = %v, want none", exec.Tickets)
	}
	if len(exec.Columns) != 6 {
		t.Fatalf("columns = %d, want the fixed six (canceled added, ADR-019)", len(exec.Columns))
	}
	for _, col := range exec.Columns {
		if len(col.WorkItems) != 0 {
			t.Errorf("column %q must be empty, got %v", col.State, columnIDs(col))
		}
	}
	if exec.Total != 0 {
		t.Errorf("Total = %d, want 0", exec.Total)
	}
}

// TestExecutionProjectionNoTicketMembership: an active container with
// work items but no tkt- membership — the board projection lists the
// items as unassigned, the execution projection is empty (Total 0)
// because members are derived from the container's tickets. The
// projection stays shape-stable: fixed six columns, all zero.
func TestExecutionProjectionNoTicketMembership(t *testing.T) {
	g := NewGraph(".", []*exchange.Unit{
		unitFixture(t, "acme", "ctr", "wave-7", map[string]string{
			conformance.DomainContainerState: "active",
			conformance.DomainExistenceState: "active",
		}),
		unitFixture(t, "acme", "sto", "alpha", map[string]string{
			conformance.DomainExecutionState: "todo",
		}),
	})
	p, err := Build("execution", g, "")
	if err != nil {
		t.Fatalf("Build(execution): %v", err)
	}
	exec := p.(*ExecutionProjection)
	if exec.Container == nil || exec.Container.Identity != "acme/ctr:wave-7" {
		t.Errorf("Container = %+v, want acme/ctr:wave-7", exec.Container)
	}
	if exec.MultipleActive {
		t.Error("MultipleActive must be false")
	}
	if len(exec.Tickets) != 0 {
		t.Errorf("Tickets = %v, want none (no tkt- derives-from the container)", exec.Tickets)
	}
	if len(exec.Columns) != 6 {
		t.Fatalf("columns = %d, want the fixed six (canceled added, ADR-019)", len(exec.Columns))
	}
	for _, col := range exec.Columns {
		if len(col.WorkItems) != 0 {
			t.Errorf("column %q must be empty, got %v", col.State, columnIDs(col))
		}
	}
	if exec.Total != 0 {
		t.Errorf("Total = %d, want 0", exec.Total)
	}
}

func TestExecutionProjectionMultipleActive(t *testing.T) {
	g := loadFixture(t, "multi-active")
	p, err := Build("execution", g, "")
	if err != nil {
		t.Fatalf("Build(execution): %v", err)
	}
	exec := p.(*ExecutionProjection)
	if !exec.MultipleActive {
		t.Error("MultipleActive must be true on the multi-active fixture")
	}
	if exec.Container == nil || exec.Container.ID != "wave-1" {
		t.Errorf("Container = %+v, want the lexicographically smallest (wave-1)", exec.Container)
	}
}

// TestExecutionProjectionSortByCreated: within a state column the work
// items order by created date ascending (identity reversed on purpose
// here), so the board reads oldest item first — display ordering only;
// the underlying graph retrieval stays canonical.
func TestExecutionProjectionSortByCreated(t *testing.T) {
	ctr := unitFixture(t, "acme", "ctr", "wave-7", map[string]string{
		conformance.DomainContainerState: "active",
		conformance.DomainExistenceState: "active",
	})
	// "zeta" is created before "alpha", so it must lead the todo
	// column despite alpha < zeta alphabetically.
	stoZeta := unitFixture(t, "acme", "sto", "zeta", map[string]string{
		conformance.DomainExecutionState: "todo",
	})
	stoZeta.Created = "2026-08-01"
	stoAlpha := unitFixture(t, "acme", "sto", "alpha", map[string]string{
		conformance.DomainExecutionState: "todo",
	})
	stoAlpha.Created = "2026-08-02"
	tktZeta := unitFixture(t, "acme", "tkt", "t1", nil,
		exchange.Relationship{Type: "derives-from", Target: "ctr:wave-7"},
		exchange.Relationship{Type: "derives-from", Target: "sto:zeta"})
	tktAlpha := unitFixture(t, "acme", "tkt", "t2", nil,
		exchange.Relationship{Type: "derives-from", Target: "ctr:wave-7"},
		exchange.Relationship{Type: "derives-from", Target: "sto:alpha"})
	g := NewGraph(".", []*exchange.Unit{ctr, stoZeta, stoAlpha, tktZeta, tktAlpha})
	proj, err := Build("execution", g, "")
	if err != nil {
		t.Fatalf("Build(execution): %v", err)
	}
	exec := proj.(*ExecutionProjection)
	// todo is the second fixed column.
	col := exec.Columns[1]
	want := []string{"acme/sto:zeta", "acme/sto:alpha"}
	if got := columnIDs(col); !reflect.DeepEqual(got, want) {
		t.Errorf("todo column = %v, want %v (created ascending, not alphabetical)", got, want)
	}
}

// TestExecutionAliasesResolveIdentically verifies the alias contract:
// sprint and wave resolve to the execution builder, producing
// byte-identical models.
func TestExecutionAliasesResolveIdentically(t *testing.T) {
	g := loadFixture(t, "valid")
	build := func(name string) *ExecutionProjection {
		t.Helper()
		p, err := Build(name, g, "")
		if err != nil {
			t.Fatalf("Build(%s): %v", name, err)
		}
		exec, ok := p.(*ExecutionProjection)
		if !ok {
			t.Fatalf("Build(%s) = %T, want *ExecutionProjection", name, p)
		}
		return exec
	}
	exec, sprint, wave := build("execution"), build("sprint"), build("wave")
	if !reflect.DeepEqual(sprint, exec) {
		t.Error("sprint alias must resolve identically to execution")
	}
	if !reflect.DeepEqual(wave, exec) {
		t.Error("wave alias must resolve identically to execution")
	}
	if sprint.Name() != "execution" {
		t.Errorf("sprint alias Name() = %q, want execution", sprint.Name())
	}
}
