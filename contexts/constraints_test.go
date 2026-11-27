package contexts

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/maleolabs/eka-core/v2/exchange"
)

// This file tests the snapshot side of the constraint index
// (ConstraintIndex and SubtreeConstraints): the strata-1-2 latest-active
// selection, the type grouping, and the subtree binding.

// constraintUnit builds one fixture unit for the index tests. applied is the
// decoded `appliesTo` value; nil yields a payload with no declaration.
func constraintUnit(typ, id string, version int, active bool, applied any) *exchange.Unit {
	existence := "active"
	if !active {
		existence = "superseded"
	}
	payload := []byte("body")
	if applied != nil {
		b, err := json.Marshal(map[string]any{"appliesTo": applied})
		if err != nil {
			panic(err)
		}
		payload = b
	}
	return &exchange.Unit{
		Identity:              exchange.Identity{Namespace: "ns", Type: typ, ID: id, InstanceVersion: version},
		CanonicalIdentityForm: "ns/" + typ + ":" + id + ":" + strconv.Itoa(version),
		StateVector:           exchange.StateVector{ExistenceState: existence},
		ContentPayload:        payload,
	}
}

// TestConstraintIndexSelectionAndOrder: only active strata-1/2 units are
// selected; types are grouped and ordered by (stratum, token); ids are sorted
// within a type. A non-active line, an out-of-range stratum (Execution) and an
// unknown token contribute nothing.
func TestConstraintIndexSelectionAndOrder(t *testing.T) {
	units := []*exchange.Unit{
		constraintUnit("std", "zeta", 1, true, nil),
		constraintUnit("req", "product", 1, true, nil),
		constraintUnit("fnd", "identity", 1, true, nil),
		constraintUnit("adr", "002-x", 1, true, nil),
		constraintUnit("adr", "001-a", 2, true, nil), // latest instance wins the line
		constraintUnit("adr", "001-a", 1, false, nil),
		constraintUnit("sto", "ignored", 1, true, nil), // Execution: stratum 4, excluded
		constraintUnit("zzz", "unknown", 1, true, nil), // no home domain
		constraintUnit("fnd", "inactive", 1, false, nil),
	}
	got := ConstraintIndex(units)
	want := []ConstraintGroup{
		{Type: "fnd", IDs: []string{"identity"}},
		{Type: "req", IDs: []string{"product"}},
		{Type: "adr", IDs: []string{"001-a", "002-x"}},
		{Type: "std", IDs: []string{"zeta"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("index = %+v, want %+v", got, want)
	}
}

// TestSubtreeConstraintsBindsOnlyUnderDir: a subtree names only the lines
// whose `appliesTo` matches a tracked file under dir; a line that matches only
// elsewhere binds nothing, and an unbound dir is nil.
func TestSubtreeConstraintsBindsOnlyUnderDir(t *testing.T) {
	units := []*exchange.Unit{
		constraintUnit("adr", "a", 1, true, []string{"src/**"}),
		constraintUnit("fnd", "b", 1, true, []string{"docs/**"}),
		constraintUnit("std", "c", 1, true, nil), // no scope: binds no path
	}
	tracked := []string{"src/main.go", "docs/x.md", "other/y.txt"}

	got, err := SubtreeConstraints(units, tracked, "src")
	if err != nil {
		t.Fatal(err)
	}
	want := []ConstraintGroup{{Type: "adr", IDs: []string{"a"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("src binding = %+v, want %+v", got, want)
	}

	got, err = SubtreeConstraints(units, tracked, "docs")
	if err != nil {
		t.Fatal(err)
	}
	want = []ConstraintGroup{{Type: "fnd", IDs: []string{"b"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("docs binding = %+v, want %+v", got, want)
	}

	got, err = SubtreeConstraints(units, tracked, "other")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("unbound dir = %+v, want nil", got)
	}
}

// TestSubtreeConstraintsOrderingDeterministic: the binding is grouped and
// ordered exactly like ConstraintIndex (type by (stratum, token), ids sorted).
func TestSubtreeConstraintsOrderingDeterministic(t *testing.T) {
	units := []*exchange.Unit{
		constraintUnit("std", "zeta", 1, true, []string{"src/**"}),
		constraintUnit("req", "alpha", 1, true, []string{"src/**"}),
		constraintUnit("fnd", "beta", 1, true, []string{"src/**"}),
		constraintUnit("req", "gamma", 1, true, []string{"src/**"}),
	}
	got, err := SubtreeConstraints(units, []string{"src/a.go"}, "src")
	if err != nil {
		t.Fatal(err)
	}
	want := []ConstraintGroup{
		{Type: "fnd", IDs: []string{"beta"}},
		{Type: "req", IDs: []string{"alpha", "gamma"}},
		{Type: "std", IDs: []string{"zeta"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("binding = %+v, want %+v", got, want)
	}
}

// TestSubtreeConstraintsMalformedIsError: a malformed `appliesTo` on an
// active strata-1/2 unit is an error, never silently ignored.
func TestSubtreeConstraintsMalformedIsError(t *testing.T) {
	bad := constraintUnit("adr", "bad", 1, true, nil)
	bad.ContentPayload = []byte(`{"appliesTo":["a**b"]}`)
	if _, err := SubtreeConstraints([]*exchange.Unit{bad}, []string{"src/a.go"}, "src"); err == nil {
		t.Fatal("malformed `appliesTo` must be an error")
	}
}
