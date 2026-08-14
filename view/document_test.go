package view

import (
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// TestParseDocumentTarget: the target grammar splits into
// (namespace, type, id); any part may be empty.
func TestParseDocumentTarget(t *testing.T) {
	cases := []struct {
		target      string
		ns, typ, id string
	}{
		{"feather/sto:alpha:2", "feather", "sto", "alpha"},
		{"feather/sto:alpha", "feather", "sto", "alpha"},
		{"sto:alpha", "", "sto", "alpha"},
		{"sto-alpha", "", "sto", "alpha"},
		{"alpha", "", "", "alpha"},
		{"feather/draft-x", "feather", "", "draft-x"},
		{"tkt-sto-alpha", "", "tkt", "sto-alpha"},
		{"", "", "", ""},
	}
	for _, c := range cases {
		ns, typ, id := parseDocumentTarget(c.target)
		if ns != c.ns || typ != c.typ || id != c.id {
			t.Errorf("parseDocumentTarget(%q) = (%q, %q, %q), want (%q, %q, %q)",
				c.target, ns, typ, id, c.ns, c.typ, c.id)
		}
	}
}

// TestDocumentByTarget: the resolution grammar — bare id, typed forms,
// qualified forms, and the smallest-canonical ambiguity rule.
func TestDocumentByTarget(t *testing.T) {
	g := loadFixture(t, "valid")

	cases := []struct {
		target string
		want   string // canonical line form, "" = nil
	}{
		{"alpha", "eka-view-fixture/sto:alpha"},                                     // bare id across types
		{"sto:alpha", "eka-view-fixture/sto:alpha"},                                 // typed
		{"sto-alpha", "eka-view-fixture/sto:alpha"},                                 // dash file form
		{"eka-view-fixture/sto:alpha", "eka-view-fixture/sto:alpha"},                // qualified
		{"001-login-serialization", "eka-view-fixture/adr:001-login-serialization"}, // bare adr id
		{"tkt-sto-alpha", "eka-view-fixture/tkt:sto-alpha"},                         // the ticket via dash form
		{"other-ns/sto:alpha", ""},                                                  // wrong namespace
		{"ghost", ""},                                                               // unknown
	}
	for _, c := range cases {
		u := g.DocumentByTarget(c.target)
		got := ""
		if u != nil {
			got = LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
		}
		if got != c.want {
			t.Errorf("DocumentByTarget(%q) = %q, want %q", c.target, got, c.want)
		}
	}
}

// TestDocumentByTargetLatestInstance: an unqualified target (bare id,
// typed, or dash form) resolves to the line's HIGHEST instance — the
// current knowledge, never the original revision (ADR-025). The
// qualified form resolves through the line index the same way.
func TestDocumentByTargetLatestInstance(t *testing.T) {
	stoV1 := unitFixture(t, "probe", "sto", "alpha", map[string]string{
		conformance.DomainExecutionState: "planned",
	})
	stoV1.Identity.InstanceVersion = 1
	stoV1.CanonicalIdentityForm = "probe/sto:alpha:1"
	stoV2 := unitFixture(t, "probe", "sto", "alpha", map[string]string{
		conformance.DomainExecutionState: "done",
	})
	stoV2.Identity.InstanceVersion = 2
	stoV2.CanonicalIdentityForm = "probe/sto:alpha:2"

	g := NewGraph(".", []*exchange.Unit{stoV1, stoV2})
	for _, target := range []string{"alpha", "sto:alpha", "sto-alpha", "probe/sto:alpha", "probe/alpha"} {
		u := g.DocumentByTarget(target)
		if u == nil {
			t.Fatalf("DocumentByTarget(%q) must resolve", target)
		}
		if u.Identity.InstanceVersion != 2 {
			t.Errorf("DocumentByTarget(%q) instance = %d, want 2 (the highest instance, ADR-025)", target, u.Identity.InstanceVersion)
		}
		if u.StateVector.ExecutionState != "done" {
			t.Errorf("DocumentByTarget(%q) state = %q, want done (the latest revision)", target, u.StateVector.ExecutionState)
		}
	}
}

// TestTicketByTargetWorkItem: the ticket projection resolves direct
// work items too (board items) — by exact id first, then typed forms.
func TestTicketByTargetWorkItem(t *testing.T) {
	g := loadFixture(t, "valid")

	// "alpha" has no ticket id match (ticket ids are sto-alpha etc.),
	// so the work item sto:alpha resolves.
	u := g.TicketByTarget("alpha")
	if u == nil || u.Identity.Type != "sto" || u.Identity.ID != "alpha" {
		t.Fatalf("TicketByTarget(alpha) = %+v, want sto:alpha", u)
	}
	// "ts-gamma" exact-matches the TICKET id first (historical
	// semantics preserved).
	u = g.TicketByTarget("ts-gamma")
	if u == nil || u.Identity.Type != "tkt" || u.Identity.ID != "ts-gamma" {
		t.Fatalf("TicketByTarget(ts-gamma) = %+v, want tkt:ts-gamma", u)
	}
	// Typed work-item form resolves the work item directly.
	u = g.TicketByTarget("sto:beta")
	if u == nil || u.Identity.Type != "sto" || u.Identity.ID != "beta" {
		t.Fatalf("TicketByTarget(sto:beta) = %+v, want sto:beta", u)
	}
}

// TestDocumentContentOrder: structured-json content renders the
// required sections in registry order first, then extra keys sorted.
func TestDocumentContentOrder(t *testing.T) {
	g := loadFixture(t, "valid")
	u := g.DocumentByTarget("001-login-serialization")
	if u == nil {
		t.Fatal("fixture adr must resolve")
	}
	d, err := buildDocument(g, "001-login-serialization")
	if err != nil {
		t.Fatalf("buildDocument: %v", err)
	}
	doc := d.(*DocumentProjection).Document
	if len(doc.Content) == 0 {
		t.Fatal("the adr must carry content sections")
	}
	// Required sections of adr- come first (registry order: Context,
	// Decision, Consequences, Alternatives Considered).
	wantFirst := []string{"context", "decision", "consequences"}
	for i, key := range wantFirst {
		if i >= len(doc.Content) || doc.Content[i].Key != key {
			t.Errorf("content[%d] = %q, want %q (registry order first)", i, doc.Content[i].Key, key)
		}
	}
	// Extras follow sorted among themselves.
	for i := len(wantFirst) + 1; i < len(doc.Content); i++ {
		if doc.Content[i-1].Key > doc.Content[i].Key {
			t.Errorf("extra content keys must be sorted: %q > %q",
				doc.Content[i-1].Key, doc.Content[i].Key)
		}
	}
	if doc.IsWorkItem {
		t.Error("an adr is not a work item")
	}
	if doc.State == "" {
		t.Error("an adr must carry its content state")
	}
}
