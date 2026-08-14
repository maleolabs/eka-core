package view

import (
	"testing"

	"github.com/maleolabs/eka-core/exchange"
)

// This file tests the note accessors of the knowledge graph (ADR-019
// D8 revised): NotesFor matches the discusses relationship against the
// subject's line form — stored targets in line form AND canonical form
// (with the instance-version suffix) both resolve — deduplicated by
// note line and sorted by canonical identity.

// noteUnit builds one cmt- unit with the given discusses targets.
func noteUnit(t *testing.T, id string, version int, discusses ...string) *exchange.Unit {
	t.Helper()
	u := &exchange.Unit{
		Identity: exchange.Identity{
			Namespace: "feather", Type: "cmt", ID: id, InstanceVersion: version,
		},
		CanonicalIdentityForm: "feather/cmt:" + id + ":" + itoa(version),
		Relationships:         []exchange.Relationship{},
	}
	for _, d := range discusses {
		u.Relationships = append(u.Relationships, exchange.Relationship{Type: "discusses", Target: d})
	}
	return u
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestNotesFor(t *testing.T) {
	units := []*exchange.Unit{
		noteUnit(t, "a-review", 1, "feather/sto:one"),                  // line-form target
		noteUnit(t, "b-review", 1, "feather/bug:two:1"),                // canonical-form target (with version)
		noteUnit(t, "c-review", 1, "feather/adr:three"),                // unrelated subject
		noteUnit(t, "a-review", 2, "feather/sto:one:1"),                // later instance of the same note line
		noteUnit(t, "d-review", 3, "feather/sto:one", "feather/tkt:t"), // multiple targets
	}
	g := NewGraph("feather", units)

	got := g.NotesFor("feather/sto:one")
	if len(got) != 2 {
		t.Fatalf("NotesFor(feather/sto:one) = %d notes, want 2 (a-review, d-review); got %+v", len(got), got)
	}
	// Sorted by canonical identity.
	if got[0].Identity.ID != "a-review" || got[1].Identity.ID != "d-review" {
		t.Errorf("NotesFor order = %s, %s; want a-review, d-review", got[0].Identity.ID, got[1].Identity.ID)
	}
	// The line's CURRENT payload (the latest instance) is returned:
	// a-review:2 supersedes the older a-review:1.
	if got[0].Identity.InstanceVersion != 2 {
		t.Errorf("a-review instance = %d, want 2 (latest)", got[0].Identity.InstanceVersion)
	}

	// Canonical-form targets resolve to the same line.
	got = g.NotesFor("feather/bug:two")
	if len(got) != 1 || got[0].Identity.ID != "b-review" {
		t.Errorf("NotesFor(feather/bug:two) = %+v, want the b-review note", got)
	}

	// A subject with no notes yields an empty set.
	if got := g.NotesFor("feather/run:x"); len(got) != 0 {
		t.Errorf("NotesFor(feather/run:x) = %+v, want none", got)
	}
}

func TestTicketProjectionNotes(t *testing.T) {
	units := []*exchange.Unit{
		// A ticket deriving from a work item.
		{Identity: exchange.Identity{Namespace: "feather", Type: "tkt", ID: "t", InstanceVersion: 1},
			CanonicalIdentityForm: "feather/tkt:t:1",
			Relationships: []exchange.Relationship{
				{Type: "derives-from", Target: "feather/sto:one:1"},
			}},
		// Its work item.
		{Identity: exchange.Identity{Namespace: "feather", Type: "sto", ID: "one", InstanceVersion: 1},
			CanonicalIdentityForm: "feather/sto:one:1",
			StateVector:           exchange.StateVector{ExecutionState: "in-progress"}},
		// Two notes: one discussing the ticket, one the work item.
		noteUnit(t, "t-review", 1, "feather/tkt:t:1"),
		noteUnit(t, "one-review", 1, "feather/sto:one"),
	}
	g := NewGraph("feather", units)
	p, err := Build("ticket", g, "tkt:t")
	if err != nil {
		t.Fatal(err)
	}
	tp := p.(*TicketProjection)
	if tp.Projected != "in-progress" {
		t.Errorf("projected = %q, want in-progress", tp.Projected)
	}
	if len(tp.Notes) != 2 {
		t.Fatalf("ticket notes = %d, want 2 (ticket + work item)", len(tp.Notes))
	}
	if tp.Notes[0].Note.Identity.ID != "one-review" || tp.Notes[1].Note.Identity.ID != "t-review" {
		t.Errorf("notes order = %s, %s; want one-review, t-review", tp.Notes[0].Note.Identity.ID, tp.Notes[1].Note.Identity.ID)
	}

	// A direct work item surfaces only its own notes.
	p, err = Build("ticket", g, "sto:one")
	if err != nil {
		t.Fatal(err)
	}
	tp = p.(*TicketProjection)
	if len(tp.Notes) != 1 || tp.Notes[0].Note.Identity.ID != "one-review" {
		t.Errorf("direct work item notes = %+v, want only one-review", tp.Notes)
	}
}
