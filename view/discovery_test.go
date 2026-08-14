package view

import (
	"reflect"
	"testing"
)

func TestDiscoveryProjection(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("discovery", g, "")
	if err != nil {
		t.Fatalf("Build(discovery): %v", err)
	}
	disc, ok := p.(*DiscoveryProjection)
	if !ok {
		t.Fatalf("Build(discovery) = %T, want *DiscoveryProjection", p)
	}

	// Fixed group order.
	wantOrder := []string{"Vision", "Strategy", "Requirements", "Research Findings"}
	gotOrder := make([]string, len(disc.Groups))
	for i, gr := range disc.Groups {
		gotOrder[i] = gr.Name
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("group order = %v, want %v", gotOrder, wantOrder)
	}

	// Per-group membership and content states.
	wantGroups := map[string][][4]string{
		"Vision": {
			{validForm + "vis:product-vision", "draft", "", ""},
		},
		"Strategy": {
			{validForm + "str:go-to-market", "review", "", ""},
		},
		"Requirements": {
			{validForm + "req:onboarding", "approved", "", ""},
		},
		"Research Findings": {
			{validForm + "fnd:market-research", "approved", "", ""},
		},
	}
	for _, gr := range disc.Groups {
		want := wantGroups[gr.Name]
		if got := groupArtifactStates(gr); !reflect.DeepEqual(got, want) {
			t.Errorf("group %q = %+v, want %+v", gr.Name, got, want)
		}
	}
}

// TestDiscoveryProjectionEmptyDomain: no Discovery artifacts — empty
// projection, fixed group shape preserved.
func TestDiscoveryProjectionEmptyDomain(t *testing.T) {
	g := loadFixture(t, "no-active")
	p, err := Build("discovery", g, "")
	if err != nil {
		t.Fatalf("Build(discovery): %v", err)
	}
	disc := p.(*DiscoveryProjection)
	if len(disc.Groups) != 4 {
		t.Fatalf("groups = %d, want the fixed four", len(disc.Groups))
	}
	for _, gr := range disc.Groups {
		if len(gr.Artifacts) != 0 {
			t.Errorf("group %q must be empty, got %v", gr.Name, gr.Artifacts)
		}
	}
}
