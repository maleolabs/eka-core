package machine_test

// This file is the cross-path consistency contract between the machine
// containers collection (`eka get containers` — machine.NewContainerCollection)
// and the projection engine (`eka view containers` / `eka view board` —
// view.Graph). Both paths must derive identical membership from the SAME
// unit set: a ticket belongs to a container and contributes a work item
// only when the derives-from reference RESOLVES (a versioned target to
// its exact instance, a line target to the line's lowest instance), and
// a ticket belongs to at most one container — the first resolvable ctr-
// target in stored order.
//
// The regression cases mirror real store states that break a parse-only
// derivation:
//
//   - a versioned ctr- target whose instance no longer exists (stale
//     relationship after the referenced instance was superseded) —
//     parse matches the line, resolution does not;
//   - a derives-from work-item target whose unit is absent — parse
//     counts an item the board can never place;
//   - a ticket deriving from TWO container lines — parse counts the
//     ticket for both, the board places it on the first only.
//
// An external test package is required: the machine path never imports
// view/ (reference/cli.md — the documented separation), so the contract
// test consumes both packages as a caller, exactly like the CLI does.

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/machine"
	"github.com/maleolabs/eka-core/view"
)

// consistencyUnit builds one canonical unit for the consistency tests:
// identity at the given instance version, relationships stored verbatim
// (targets in the RSF canonical identity form — always versioned, exactly
// as the compiler emits them, exchange/load.go IdentityForm).
func consistencyUnit(ns, typeToken, id string, v int, rels ...exchange.Relationship) *exchange.Unit {
	u := &exchange.Unit{
		Identity:              exchange.Identity{Namespace: ns, Type: typeToken, ID: id, InstanceVersion: v},
		CanonicalIdentityForm: ns + "/" + typeToken + ":" + id + ":" + strconv.Itoa(v),
		Relationships:         []exchange.Relationship{},
	}
	for _, r := range rels {
		u.Relationships = append(u.Relationships, r)
	}
	return u
}

// ctrCounts is the membership summary one container line reports.
type ctrCounts struct{ tickets, items int }

// machineContainerCounts runs the machine path over the unit set.
func machineContainerCounts(t *testing.T, units []*exchange.Unit) map[string]ctrCounts {
	t.Helper()
	col, err := machine.NewContainerCollection(units)
	if err != nil {
		t.Fatalf("machine.NewContainerCollection: %v", err)
	}
	out := make(map[string]ctrCounts, len(col.Containers))
	for _, c := range col.Containers {
		out[c.CanonicalForm] = ctrCounts{tickets: c.Tickets, items: c.Items}
	}
	return out
}

// viewContainerCounts runs the view containers projection over the same
// unit set.
func viewContainerCounts(t *testing.T, units []*exchange.Unit) map[string]ctrCounts {
	t.Helper()
	g := view.NewGraph(".", units)
	proj, err := view.Build("containers", g, "")
	if err != nil {
		t.Fatalf("view.Build(containers): %v", err)
	}
	p := proj.(*view.ContainersProjection)
	out := make(map[string]ctrCounts, len(p.Containers))
	for _, c := range p.Containers {
		out[c.Identity] = ctrCounts{tickets: c.Tickets, items: c.Items}
	}
	return out
}

// boardSummary runs the view board projection over the unit set and
// returns (total, unassigned, containerCount).
func boardSummary(t *testing.T, units []*exchange.Unit) (int, int, int) {
	t.Helper()
	g := view.NewGraph(".", units)
	proj, err := view.Build("board", g, "")
	if err != nil {
		t.Fatalf("view.Build(board): %v", err)
	}
	b := proj.(*view.BoardProjection)
	return b.Total, b.Unassigned, b.ContainerCount
}

// TestContainersBoardConsistency: for every divergence scenario, the
// machine containers collection and the view containers projection must
// report IDENTICAL tickets/items per container line — and the board must
// see the same assigned items (board total minus unassigned equals the
// machine's item sum across containers).
func TestContainersBoardConsistency(t *testing.T) {
	cases := []struct {
		name  string
		units []*exchange.Unit
	}{
		{
			// A ticket whose ctr- target names instance 9 while the line
			// only carries instance 1: the reference parses as the wave-1
			// line but does not resolve. The ticket must belong to NO
			// container and its work item must be unassigned.
			name: "stale versioned container reference",
			units: []*exchange.Unit{
				consistencyUnit("acme", "ctr", "wave-1", 1,
					exchange.Relationship{Type: "depends-on", Target: "acme/plan:roadmap:1"}),
				consistencyUnit("acme", "sto", "alpha", 1),
				consistencyUnit("acme", "tkt", "stale", 1,
					exchange.Relationship{Type: "derives-from", Target: "acme/ctr:wave-1:9"},
					exchange.Relationship{Type: "derives-from", Target: "acme/sto:alpha:1"}),
			},
		},
		{
			// A ticket whose ctr- target resolves but whose work-item
			// target is absent: the ticket belongs to the container but
			// contributes no item.
			name: "dangling work item reference",
			units: []*exchange.Unit{
				consistencyUnit("acme", "ctr", "wave-1", 1),
				consistencyUnit("acme", "tkt", "ghost", 1,
					exchange.Relationship{Type: "derives-from", Target: "acme/ctr:wave-1:1"},
					exchange.Relationship{Type: "derives-from", Target: "acme/sto:ghost:1"}),
			},
		},
		{
			// A ticket deriving from TWO container lines: first-resolvable
			// wins — the ticket and its work item belong to wave-1 only.
			name: "ticket deriving from two containers",
			units: []*exchange.Unit{
				consistencyUnit("acme", "ctr", "wave-1", 1),
				consistencyUnit("acme", "ctr", "wave-0", 1),
				consistencyUnit("acme", "sto", "alpha", 1),
				consistencyUnit("acme", "tkt", "multi", 1,
					exchange.Relationship{Type: "derives-from", Target: "acme/ctr:wave-1:1"},
					exchange.Relationship{Type: "derives-from", Target: "acme/ctr:wave-0:1"},
					exchange.Relationship{Type: "derives-from", Target: "acme/sto:alpha:1"}),
			},
		},
		{
			// A healthy ticket (resolvable container and work item) plus a
			// second instance of the container line: membership binds to
			// the LINE, and the lowest instance wins the line projection.
			name: "resolvable references with a multi-instance line",
			units: []*exchange.Unit{
				consistencyUnit("acme", "ctr", "wave-1", 1),
				consistencyUnit("acme", "ctr", "wave-1", 2),
				consistencyUnit("acme", "sto", "alpha", 1),
				consistencyUnit("acme", "tkt", "ok", 1,
					exchange.Relationship{Type: "derives-from", Target: "acme/ctr:wave-1:1"},
					exchange.Relationship{Type: "derives-from", Target: "acme/sto:alpha:1"}),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			machineCounts := machineContainerCounts(t, tc.units)
			viewCounts := viewContainerCounts(t, tc.units)
			if !reflect.DeepEqual(machineCounts, viewCounts) {
				t.Errorf("machine and view containers diverge:\nmachine: %+v\nview:   %+v",
					machineCounts, viewCounts)
			}
			total, unassigned, _ := boardSummary(t, tc.units)
			machineItems := 0
			for _, c := range machineCounts {
				machineItems += c.items
			}
			viewItems := 0
			for _, c := range viewCounts {
				viewItems += c.items
			}
			// The board must agree with BOTH paths on how many items are
			// assigned to containers (its unassigned count is the
			// complement over the same membership rule).
			if want := total - unassigned; machineItems != want || viewItems != want {
				t.Errorf("board assigned items = %d, machine sum = %d, view sum = %d",
					want, machineItems, viewItems)
			}
		})
	}
}
