package view

import (
	"reflect"
	"testing"
)

func TestTicketProjection(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("ticket", g, "tkt-ts-gamma")
	if err != nil {
		t.Fatalf("Build(ticket, tkt-ts-gamma): %v", err)
	}
	tp, ok := p.(*TicketProjection)
	if !ok {
		t.Fatalf("Build(ticket) = %T, want *TicketProjection", p)
	}

	// Identity, projected status (derived from the owner work item, not
	// the ticket body), work item and container.
	if tp.Ticket.Identity != validForm+"tkt:ts-gamma" {
		t.Errorf("Ticket.Identity = %q", tp.Ticket.Identity)
	}
	if tp.Projected != "in-progress" {
		t.Errorf("Projected = %q, want in-progress (owner state)", tp.Projected)
	}
	if tp.WorkItem == nil || tp.WorkItem.Identity != validForm+"ts:gamma" || tp.WorkItem.State != "in-progress" {
		t.Errorf("WorkItem = %+v, want ts:gamma/in-progress", tp.WorkItem)
	}
	if tp.Container == nil || tp.Container.Identity != validForm+"ctr:wave-1" {
		t.Errorf("Container = %+v, want ctr:wave-1", tp.Container)
	}

	// Relationship list: the derives-from references in file order.
	if want := []string{"ctr:wave-1", "ts:gamma"}; !reflect.DeepEqual(tp.References, want) {
		t.Errorf("References = %v, want %v", tp.References, want)
	}

	// All target forms resolve to the same ticket.
	for _, target := range []string{"ts-gamma", "tkt:ts-gamma"} {
		p2, err := Build("ticket", g, target)
		if err != nil {
			t.Fatalf("Build(ticket, %q): %v", target, err)
		}
		if p2.(*TicketProjection).Ticket.Identity != tp.Ticket.Identity {
			t.Errorf("target %q resolved differently", target)
		}
	}
}

func TestTicketProjectionDone(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("ticket", g, "tkt-ch-epsilon")
	if err != nil {
		t.Fatalf("Build(ticket, tkt-ch-epsilon): %v", err)
	}
	tp := p.(*TicketProjection)
	if tp.Projected != "done" {
		t.Errorf("Projected = %q, want done", tp.Projected)
	}
	if tp.WorkItem == nil || tp.WorkItem.ID != "epsilon" || tp.WorkItem.Type != "ch" {
		t.Errorf("WorkItem = %+v, want ch:epsilon", tp.WorkItem)
	}
}

// TestTicketUnresolved verifies the projection for a ticket without a
// resolvable work item: explicit "unresolved" status, container still
// resolved, relationship list intact.
func TestTicketUnresolved(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("ticket", g, "tkt-unresolved")
	if err != nil {
		t.Fatalf("Build(ticket, tkt-unresolved): %v", err)
	}
	tp := p.(*TicketProjection)
	if tp.Projected != "unresolved" {
		t.Errorf("Projected = %q, want unresolved", tp.Projected)
	}
	if tp.WorkItem != nil {
		t.Errorf("WorkItem = %+v, want nil", tp.WorkItem)
	}
	if tp.Container == nil || tp.Container.Identity != validForm+"ctr:wave-1" {
		t.Errorf("Container = %+v, want ctr:wave-1", tp.Container)
	}
	if want := []string{"ctr:wave-1"}; !reflect.DeepEqual(tp.References, want) {
		t.Errorf("References = %v, want %v", tp.References, want)
	}
}

// TestTicketProjectionCompletedContainer resolves a ticket inside the
// completed container — the ticket projection is container-agnostic.
func TestTicketProjectionCompletedContainer(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("ticket", g, "tkt-sto-legacy")
	if err != nil {
		t.Fatalf("Build(ticket, tkt-sto-legacy): %v", err)
	}
	tp := p.(*TicketProjection)
	if tp.Projected != "done" {
		t.Errorf("Projected = %q, want done", tp.Projected)
	}
	if tp.Container == nil || tp.Container.ID != "wave-0" || tp.Container.State != "completed" {
		t.Errorf("Container = %+v, want wave-0/completed", tp.Container)
	}
}

// TestDeterministicOrdering verifies that building the same projection
// twice over identical state produces identical models, and that the
// model is stable across repeated Build calls.
func TestDeterministicOrdering(t *testing.T) {
	g := loadFixture(t, "valid")
	build := func(name, target string) Projection {
		t.Helper()
		p, err := Build(name, g, target)
		if err != nil {
			t.Fatalf("Build(%s): %v", name, err)
		}
		return p
	}
	for _, tc := range []struct{ name, target string }{
		{"discovery", ""},
		{"architecture", ""},
		{"planning", ""},
		{"execution", ""},
		{"operations", ""},
		{"ticket", "tkt-ts-gamma"},
	} {
		a, b := build(tc.name, tc.target), build(tc.name, tc.target)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s projection is not deterministic", tc.name)
		}
	}
}
