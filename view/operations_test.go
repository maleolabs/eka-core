package view

import (
	"reflect"
	"testing"
)

func TestOperationsProjection(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("operations", g, "")
	if err != nil {
		t.Fatalf("Build(operations): %v", err)
	}
	ops, ok := p.(*OperationsProjection)
	if !ok {
		t.Fatalf("Build(operations) = %T, want *OperationsProjection", p)
	}

	// Fixed group order.
	wantOrder := []string{"Runbooks", "Release Records"}
	gotOrder := make([]string, len(ops.Groups))
	for i, gr := range ops.Groups {
		gotOrder[i] = gr.Name
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("group order = %v, want %v", gotOrder, wantOrder)
	}

	// Per-group membership and content states.
	wantGroups := map[string][][4]string{
		"Runbooks": {
			{validForm + "run:deploy", "approved", "", ""},
		},
		"Release Records": {
			{validForm + "rel:release-1", "review", "", ""},
		},
	}
	for _, gr := range ops.Groups {
		want := wantGroups[gr.Name]
		if got := groupArtifactStates(gr); !reflect.DeepEqual(got, want) {
			t.Errorf("group %q = %+v, want %+v", gr.Name, got, want)
		}
	}
}

// TestOperationsProjectionEmptyDomain: no Operations artifacts — empty
// projection, fixed group shape preserved.
func TestOperationsProjectionEmptyDomain(t *testing.T) {
	g := loadFixture(t, "no-active")
	p, err := Build("operations", g, "")
	if err != nil {
		t.Fatalf("Build(operations): %v", err)
	}
	ops := p.(*OperationsProjection)
	if len(ops.Groups) != 2 {
		t.Fatalf("groups = %d, want the fixed two", len(ops.Groups))
	}
	for _, gr := range ops.Groups {
		if len(gr.Artifacts) != 0 {
			t.Errorf("group %q must be empty, got %v", gr.Name, gr.Artifacts)
		}
	}
}
