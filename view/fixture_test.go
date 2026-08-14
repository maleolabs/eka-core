package view

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/maleolabs/eka-core/compile"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// loadFixture compiles a fixture repository through the Knowledge
// Compiler and builds its Knowledge Graph — the same pipeline the CLI
// uses (authoring conformance gate + CKO assembly + projection). The
// fixtures must be conformant (TestFixtureConforms pins that).
func loadFixture(t *testing.T, name string) *Graph {
	t.Helper()
	res, err := compile.Compile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("compile fixture %s: %v", name, err)
	}
	return NewGraph(".", res.CKOs)
}

// groupArtifactStates returns the (identity, content-state,
// planning-state, phase) tuples of a group, in order — the projection's
// observable per-entry state.
func groupArtifactStates(group Group) [][4]string {
	out := make([][4]string, 0, len(group.Artifacts))
	for _, a := range group.Artifacts {
		ps, ph := "", ""
		if a.HasPlanningState {
			ps = a.PlanningState
		}
		if a.HasPhase {
			ph = a.Phase
		}
		out = append(out, [4]string{a.Identity, a.ContentState, ps, ph})
	}
	return out
}

// unitFixture builds one canonical unit for projection tests: identity
// line at instance-version 1, a state vector from the given domain
// map, and relationships ordered by (type, target) exactly as the
// compiler orders them. Relationship targets may be written in the
// authoring reference convention ("<type>:<id>"); they are
// canonicalized to the RSF canonical identity form the compiler
// produces.
func unitFixture(t *testing.T, ns, typeToken, id string, states map[string]string, rels ...exchange.Relationship) *exchange.Unit {
	t.Helper()
	u := &exchange.Unit{
		Identity:              exchange.Identity{Namespace: ns, Type: typeToken, ID: id, InstanceVersion: 1},
		CanonicalIdentityForm: LineForm(ns, typeToken, id) + ":1",
		StateVector:           stateVectorOf(states),
		Relationships:         []exchange.Relationship{},
	}
	for _, r := range rels {
		ref, err := conformance.ParseReference(r.Target, ns, typeToken)
		if err != nil {
			t.Fatalf("unitFixture: relationship target %q: %v", r.Target, err)
		}
		u.Relationships = append(u.Relationships, exchange.Relationship{
			Type:   r.Type,
			Target: ref.Namespace + "/" + ref.Type + ":" + ref.ID + ":1",
		})
	}
	sort.Slice(u.Relationships, func(i, j int) bool {
		if u.Relationships[i].Type != u.Relationships[j].Type {
			return u.Relationships[i].Type < u.Relationships[j].Type
		}
		return u.Relationships[i].Target < u.Relationships[j].Target
	})
	return u
}

// stateVectorOf maps a state-domain map onto the fixed-order state
// vector of the canonical unit shape.
func stateVectorOf(states map[string]string) exchange.StateVector {
	return exchange.StateVector{
		ContentState:   states[conformance.DomainContentState],
		ExecutionState: states[conformance.DomainExecutionState],
		PlanningState:  states[conformance.DomainPlanningState],
		ContainerState: states[conformance.DomainContainerState],
		ExistenceState: states[conformance.DomainExistenceState],
	}
}
