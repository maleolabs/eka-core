package view

import (
	"reflect"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

func TestArchitectureProjection(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("architecture", g, "")
	if err != nil {
		t.Fatalf("Build(architecture): %v", err)
	}
	arch, ok := p.(*ArchitectureProjection)
	if !ok {
		t.Fatalf("Build(architecture) = %T, want *ArchitectureProjection", p)
	}

	// Fixed group order.
	wantOrder := []string{"Decisions", "Architecture Descriptions", "Specifications", "Standards & Guidelines", "Vocabulary"}
	gotOrder := make([]string, len(arch.Groups))
	for i, gr := range arch.Groups {
		gotOrder[i] = gr.Name
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("group order = %v, want %v", gotOrder, wantOrder)
	}

	// Decisions merges adr- and dec- in canonical identity order
	// (adr:001 < adr:002 < adr:003 < dec:001), with the ADR and decision
	// content-state variants.
	wantGroups := map[string][][4]string{
		"Decisions": {
			{validForm + "adr:001-login-serialization", "accepted", "", ""},
			{validForm + "adr:002-session-encoding", "superseded", "", ""},
			{validForm + "adr:003-token-format", "accepted", "", ""},
			{validForm + "dec:001-api-shape", "accepted", "", ""},
		},
		"Architecture Descriptions": {
			{validForm + "arc:system-architecture", "approved", "", ""},
		},
		"Specifications": {
			{validForm + "spec:auth-flow", "draft", "", ""},
		},
		"Standards & Guidelines": {
			{validForm + "std:gofmt", "review", "", ""},
		},
		"Vocabulary": {
			{validForm + "gls:domain-terms", "amended", "", ""},
		},
	}
	for _, gr := range arch.Groups {
		want := wantGroups[gr.Name]
		if got := groupArtifactStates(gr); !reflect.DeepEqual(got, want) {
			t.Errorf("group %q = %+v, want %+v", gr.Name, got, want)
		}
	}
}

// TestArchitectureProjectionEmptyDomain: no Architecture artifacts —
// empty projection, fixed group shape preserved.
func TestArchitectureProjectionEmptyDomain(t *testing.T) {
	g := loadFixture(t, "no-active")
	p, err := Build("architecture", g, "")
	if err != nil {
		t.Fatalf("Build(architecture): %v", err)
	}
	arch := p.(*ArchitectureProjection)
	if len(arch.Groups) != 5 {
		t.Fatalf("groups = %d, want the fixed five", len(arch.Groups))
	}
	for _, gr := range arch.Groups {
		if len(gr.Artifacts) != 0 {
			t.Errorf("group %q must be empty, got %v", gr.Name, gr.Artifacts)
		}
	}
}

// TestArchitectureProjectionShowsLatestInstance: a line with several
// instances projects as ONE artifact carrying the state of the
// HIGHEST instance — the latest revision of the knowledge (ADR-025).
// The original (first) instance must never surface: the projection
// shows the line as it is now, not as it was first published.
func TestArchitectureProjectionShowsLatestInstance(t *testing.T) {
	v1 := unitFixture(t, "probe", "arc", "sys", map[string]string{
		conformance.DomainContentState:   "draft",
		conformance.DomainExistenceState: "active",
	})
	v1.Identity.InstanceVersion = 1
	v1.CanonicalIdentityForm = "probe/arc:sys:1"
	v2 := unitFixture(t, "probe", "arc", "sys", map[string]string{
		conformance.DomainContentState:   "approved",
		conformance.DomainExistenceState: "active",
	})
	v2.Identity.InstanceVersion = 2
	v2.CanonicalIdentityForm = "probe/arc:sys:2"

	g := NewGraph(".", []*exchange.Unit{v1, v2})
	p, err := Build("architecture", g, "")
	if err != nil {
		t.Fatalf("Build(architecture): %v", err)
	}
	arch := p.(*ArchitectureProjection)

	var got []DomainArtifact
	for _, gr := range arch.Groups {
		if gr.Name == "Architecture Descriptions" {
			got = gr.Artifacts
		}
	}
	if len(got) != 1 {
		t.Fatalf("Architecture Descriptions artifacts = %d, want exactly 1 line (v1 and v2 collapse)", len(got))
	}
	a := got[0]
	if a.Identity != "probe/arc:sys" {
		t.Errorf("identity = %q, want probe/arc:sys", a.Identity)
	}
	if a.ContentState != "approved" {
		t.Errorf("content-state = %q, want %q (the highest instance, ADR-025; the original shows draft)",
			a.ContentState, "approved")
	}
}
