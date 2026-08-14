package contexts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/runtime"
)

// This file tests the Context Engine at the engine level: the
// deterministic construction of the Context Object over a seeded
// Runtime. The seed is the view "valid" fixture through the Runtime
// (runtime.Ensure + Authoring.Sync — the store-backed setup of the
// get path); the fixture graph is read and asserted directly.

// seedContextRepo seeds a fresh workspace (EKA_HOME) with a copy of
// the view "valid" fixture through the Runtime, optionally adding
// extra authoring docs before the sync. Returns the repo path. (The
// cmd package test helpers are unreachable here by design — the
// contexts tests must not import cmd; the copy helper is local.)
func seedContextRepo(t *testing.T, extra func(repo string)) string {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copyFixtureLocal(t, filepath.Join("..", "view", "testdata", "valid"))
	if extra != nil {
		extra(repo)
	}
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := runtime.Authoring.Sync(r, repo, runtime.SyncOptions{Pull: true, Push: true}); err != nil {
		t.Fatal(err)
	}
	return repo
}

// openRuntime opens the seeded workspace (the seed helper closes its
// own runtime after the sync; the test opens a fresh one).
func openRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	r, err := runtime.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// lineNumber resolves the issue number of one fixture line through the
// Runtime (the same accessor the engine uses) — the tests compare
// against the runtime truth instead of pinning allocation-order
// numbers.
func lineNumber(t *testing.T, r *runtime.Runtime, typeToken, id string) int {
	t.Helper()
	n, err := r.Knowledge.NumberForLine("eka-view-fixture", "eka-view-fixture", typeToken, id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestBuildDependencyStratifies builds the dependency context of a
// known fixture unit (eka-view-fixture/tkt:sto-alpha, which
// derives-from ctr:wave-1 + sto:alpha — verified against the fixture
// docs/operating/projections/tkt-sto-alpha.md) and asserts the full
// classification: strata ascending with the expected units and roles,
// sections with the expected canonical forms and roles, history
// ascending, focus detail and the summary counts.
func TestBuildDependencyStratifies(t *testing.T) {
	seedContextRepo(t, nil)
	r := openRuntime(t)
	obj, err := New(r).Build("eka-view-fixture/tkt:sto-alpha", "eka-view-fixture", DepthDependency, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.Schema != Schema || obj.Kind != "context" || obj.Depth != "dependency" {
		t.Errorf("object spine = %q/%q/%q, want eka-context-v1/context/dependency", obj.Schema, obj.Kind, obj.Depth)
	}
	// Focus detail: the ticket line, its number, domain and stratum.
	if obj.Focus.CanonicalForm != "eka-view-fixture/tkt:sto-alpha:1" || obj.Focus.LineForm != "eka-view-fixture/tkt:sto-alpha" {
		t.Errorf("focus forms = %q/%q", obj.Focus.CanonicalForm, obj.Focus.LineForm)
	}
	if want := lineNumber(t, r, "tkt", "sto-alpha"); obj.Focus.Number != want {
		t.Errorf("focus number = %d, want %d", obj.Focus.Number, want)
	}
	if obj.Focus.EngineeringDomain != "Execution" || obj.Focus.Stratum != 4 {
		t.Errorf("focus domain/stratum = %q/%d, want Execution/4", obj.Focus.EngineeringDomain, obj.Focus.Stratum)
	}
	if len(obj.Focus.Relationships) != 2 || obj.Focus.Relationships[0].Type != "derives-from" {
		t.Errorf("focus relationships = %v, want the two derives-from edges", obj.Focus.Relationships)
	}
	// Strata: one stratum (Execution/4) with the two upstream units,
	// sorted by canonical form (ctr before sto).
	if len(obj.Strata) != 1 {
		t.Fatalf("strata = %d groups, want 1", len(obj.Strata))
	}
	st := obj.Strata[0]
	if st.Stratum != 4 || st.Domain != "Execution" || len(st.Units) != 2 {
		t.Fatalf("stratum = %+v, want Execution/4 with 2 units", st)
	}
	wantForms := []string{"eka-view-fixture/ctr:wave-1:1", "eka-view-fixture/sto:alpha:1"}
	for i, u := range st.Units {
		if u.CanonicalForm != wantForms[i] {
			t.Errorf("stratum unit %d = %s, want %s", i, u.CanonicalForm, wantForms[i])
		}
		if u.Role != "derives-from" {
			t.Errorf("stratum unit %d role = %q, want derives-from", i, u.Role)
		}
	}
	// Sections: upstream and dependencies carry the two derives-from
	// targets; downstream, constraints, decisions, planning and review
	// are empty (all collected units are Execution/4 — no higher
	// authority — and none of the type tokens match).
	if len(obj.Sections.Upstream) != 2 || len(obj.Sections.Dependencies) != 2 {
		t.Fatalf("upstream/dependencies = %d/%d, want 2/2", len(obj.Sections.Upstream), len(obj.Sections.Dependencies))
	}
	if obj.Sections.Upstream[0].LineForm != "eka-view-fixture/ctr:wave-1" ||
		obj.Sections.Upstream[1].LineForm != "eka-view-fixture/sto:alpha" {
		t.Errorf("upstream = %v, want ctr:wave-1 then sto:alpha (canonical order)", obj.Sections.Upstream)
	}
	if obj.Sections.Upstream[1].Role != "derives-from" || obj.Sections.Dependencies[1].Role != "derives-from" {
		t.Errorf("section roles = %q/%q, want derives-from", obj.Sections.Upstream[1].Role, obj.Sections.Dependencies[1].Role)
	}
	if len(obj.Sections.Downstream) != 0 || len(obj.Sections.Constraints) != 0 ||
		len(obj.Sections.Decisions) != 0 || len(obj.Sections.Planning) != 0 || len(obj.Sections.Review) != 0 {
		t.Errorf("empty sections must stay empty: downstream=%d constraints=%d decisions=%d planning=%d review=%d",
			len(obj.Sections.Downstream), len(obj.Sections.Constraints),
			len(obj.Sections.Decisions), len(obj.Sections.Planning), len(obj.Sections.Review))
	}
	// Summary: 2 distinct units, 2 non-empty sections (upstream +
	// dependencies), 1 history entry.
	if obj.Summary != (Summary{Focus: 1, Units: 2, Sections: 2, History: 1}) {
		t.Errorf("summary = %+v, want {1 2 2 1}", obj.Summary)
	}
	// History: the single instance, ascending (one entry).
	if len(obj.Sections.History) != 1 || obj.Sections.History[0].InstanceVersion != 1 {
		t.Fatalf("history = %v, want the single instance 1", obj.Sections.History)
	}
	if obj.Sections.History[0].CanonicalForm != "eka-view-fixture/tkt:sto-alpha:1" || obj.Sections.History[0].ObjectHash == "" {
		t.Errorf("history entry = %+v, want the canonical form with an object hash", obj.Sections.History[0])
	}
	// The projectID "" contract: no numbers anywhere.
	objNoNum, err := New(r).Build("eka-view-fixture/tkt:sto-alpha", "", DepthDependency, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if objNoNum.Focus.Number != 0 {
		t.Errorf("focus number with projectID \"\" = %d, want 0 (omitted)", objNoNum.Focus.Number)
	}
	for _, st := range objNoNum.Strata {
		for _, u := range st.Units {
			if u.Number != 0 {
				t.Errorf("entry %s number with projectID \"\" = %d, want 0", u.LineForm, u.Number)
			}
		}
	}
}

// TestBuildDepthBounding asserts the depth ladder: local collects
// nothing (Units == 1 — the focus itself; no sections besides
// history), dependency collects exactly the distinct one-hop
// neighbors, and engineering is bounded: equal to dependency when the
// fixture has no higher-stratum hop-2 units, strictly larger when it
// does (the extra-docs seed adds a Discovery finding referenced by a
// collected ticket), and never larger than dependency + 64.
func TestBuildDepthBounding(t *testing.T) {
	seedContextRepo(t, nil)
	r := openRuntime(t)

	// local: the focus plus its history — no sections, Units == 1.
	obj, err := New(r).Build("eka-view-fixture/tkt:sto-alpha", "eka-view-fixture", DepthLocal, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.Summary.Units != 1 || obj.Summary.Sections != 0 || obj.Summary.History != 1 {
		t.Errorf("local summary = %+v, want {1 1 0 1}", obj.Summary)
	}
	if len(obj.Strata) != 1 || obj.Strata[0].Units[0].CanonicalForm != "eka-view-fixture/tkt:sto-alpha:1" {
		t.Errorf("local strata = %+v, want the focus's own stratum with the focus entry", obj.Strata)
	}
	for _, list := range [][]Entry{obj.Sections.Upstream, obj.Sections.Downstream, obj.Sections.Dependencies,
		obj.Sections.Constraints, obj.Sections.Decisions, obj.Sections.Planning, obj.Sections.Review} {
		if len(list) != 0 {
			t.Errorf("local depth must collect no sections, got %d entries", len(list))
		}
	}
	if len(obj.Sections.History) != 1 {
		t.Errorf("local history = %d entries, want 1", len(obj.Sections.History))
	}

	// dependency vs engineering on the plain fixture: no higher-
	// stratum hop-2 units exist (every collected unit's own
	// relationships target Execution/4 units or the focus line), so
	// engineering equals dependency.
	dep, err := New(r).Build("eka-view-fixture/tkt:sto-alpha", "eka-view-fixture", DepthDependency, Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := New(r).Build("eka-view-fixture/tkt:sto-alpha", "eka-view-fixture", DepthEngineering, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if dep.Summary.Units != 2 {
		t.Errorf("dependency units = %d, want 2 (the two derives-from targets)", dep.Summary.Units)
	}
	if eng.Summary.Units != dep.Summary.Units {
		t.Errorf("engineering units = %d, want %d (no higher-stratum hop-2 in the fixture)", eng.Summary.Units, dep.Summary.Units)
	}
	if eng.Summary.Units > dep.Summary.Units+maxConstraintClosure {
		t.Errorf("engineering units %d exceed dependency %d + 64", eng.Summary.Units, dep.Summary.Units)
	}
}

// TestBuildDepthBoundingClosure seeds a higher-authority hop-2 unit
// (a Discovery finding referenced by a ticket that derives from the
// focus story's ticket neighborhood) and asserts the closure: the
// finding joins the collection (strata) and the constraints section
// with the role "constraint", engineering units strictly exceed the
// dependency units, and the cap still holds.
func TestBuildDepthBoundingClosure(t *testing.T) {
	seedContextRepo(t, func(repo string) {
		// The constraint: a Discovery finding (stratum 1 — higher
		// authority than the Execution/4 focus) referenced by a ticket
		// that derives from sto:alpha.
		fnd := `---
namespace: eka-view-fixture
type: fnd
id: context-spike
instance-version: 1
revision: 1
content-state: draft
existence-state: active
dimension: research
author: Engineering Architecture
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on: []
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering Architecture
  - date: 2026-08-05
    domain: content-state
    from: "-"
    to: draft
    by: Engineering Architecture
---
# FND — Context Spike

## Purpose

Spike the context closure.

## Content

Findings.

## Investigation Summary

Summary.

## Conclusion

Conclusion.
`
		if err := os.WriteFile(filepath.Join(repo, "docs", "research", "fnd-context-spike.md"), []byte(fnd), 0o644); err != nil {
			t.Fatal(err)
		}
		// The ticket: derives from the container (Rule 8) plus the
		// focus story AND the finding — the ticket joins the focus's
		// downstream, and its derives-from reaches the higher
		// authority through the hop-2 expansion.
		tkt := `---
namespace: eka-view-fixture
type: tkt
id: context-spike
instance-version: 1
revision: 1
author: Engineering Architecture
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from:
  - ctr:wave-1
  - sto:alpha
  - fnd:context-spike
depends-on: []
---

> Generated — State Projection. Do NOT edit state here; refresh on read.

# Ticket — Context Spike

## Commands

- run the context spike.

## Projected Status

Projected from the owner work item.
`
		if err := os.WriteFile(filepath.Join(repo, "docs", "operating", "projections", "tkt-context-spike.md"), []byte(tkt), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	r := openRuntime(t)
	dep, err := New(r).Build("eka-view-fixture/sto:alpha", "eka-view-fixture", DepthDependency, Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := New(r).Build("eka-view-fixture/sto:alpha", "eka-view-fixture", DepthEngineering, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Dependency: the three tickets deriving from sto:alpha.
	if dep.Summary.Units != 3 {
		t.Errorf("dependency units = %d, want 3 (the three deriving tickets)", dep.Summary.Units)
	}
	// Engineering: the closure adds the Discovery finding (stratum 1
	// < 4) through the ticket's derives-from — strictly larger than
	// dependency, still bounded by the cap.
	if eng.Summary.Units != 4 {
		t.Errorf("engineering units = %d, want 4 (dependency 3 + the finding)", eng.Summary.Units)
	}
	if eng.Summary.Units <= dep.Summary.Units {
		t.Errorf("engineering units %d must exceed dependency units %d when a higher-stratum hop-2 exists", eng.Summary.Units, dep.Summary.Units)
	}
	if eng.Summary.Units > dep.Summary.Units+maxConstraintClosure {
		t.Errorf("engineering units %d exceed dependency %d + 64", eng.Summary.Units, dep.Summary.Units)
	}
	// The finding joins the constraints with the role "constraint".
	if len(eng.Sections.Constraints) != 1 {
		t.Fatalf("constraints = %d entries, want 1 (the finding)", len(eng.Sections.Constraints))
	}
	c := eng.Sections.Constraints[0]
	if c.LineForm != "eka-view-fixture/fnd:context-spike" || c.Role != "constraint" || c.Stratum != 1 {
		t.Errorf("constraint = %+v, want the finding with role constraint, stratum 1", c)
	}
	// The finding also joins the strata landscape (its own stratum 1
	// group, ascending before the Execution/4 group).
	if len(eng.Strata) != 2 || eng.Strata[0].Stratum != 1 || eng.Strata[1].Stratum != 4 {
		t.Errorf("strata = %d groups (%d..%d), want Discovery/1 then Execution/4",
			len(eng.Strata), eng.Strata[0].Stratum, eng.Strata[1].Stratum)
	}
	if eng.Strata[0].Units[0].LineForm != "eka-view-fixture/fnd:context-spike" {
		t.Errorf("stratum 1 units = %v, want the finding", eng.Strata[0].Units)
	}
}

// TestBuildDeterministic builds the same context twice (dependency
// and engineering) and byte-compares the Marshal() outputs: the
// Context Object is a pure function of (subject, depth, options).
func TestBuildDeterministic(t *testing.T) {
	seedContextRepo(t, nil)
	r := openRuntime(t)
	e := New(r)
	for _, depth := range []Depth{DepthDependency, DepthEngineering} {
		a, err := e.Build("eka-view-fixture/ctr:wave-1", "eka-view-fixture", depth, Options{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := e.Build("eka-view-fixture/ctr:wave-1", "eka-view-fixture", depth, Options{})
		if err != nil {
			t.Fatal(err)
		}
		aOut, err := a.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		bOut, err := b.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if !bytesEqual(aOut, bOut) {
			t.Errorf("depth %s: two builds must produce byte-identical objects:\n%s\n%s", depth, aOut, bOut)
		}
	}
}

// TestBuildUnknownSubject asserts the Resolve error path: an unknown
// identity and an unparseable form both surface as deterministic
// errors (the CLI maps them to exit 2).
func TestBuildUnknownSubject(t *testing.T) {
	seedContextRepo(t, nil)
	r := openRuntime(t)
	e := New(r)
	if _, err := e.Build("eka-view-fixture/sto:ghost", "eka-view-fixture", DepthDependency, Options{}); err == nil {
		t.Error("unknown identity must error")
	} else if !strings.Contains(err.Error(), "no knowledge object matches") {
		t.Errorf("unknown identity error = %q, want the not-found message", err)
	}
	if _, err := e.Build("not a reference", "eka-view-fixture", DepthDependency, Options{}); err == nil {
		t.Error("unparseable subject must error")
	} else if !strings.Contains(err.Error(), "contexts:") {
		t.Errorf("unparseable subject error = %q, want the contexts: prefix", err)
	}
	if _, err := e.Build("eka-view-fixture/sto:alpha", "eka-view-fixture", Depth("bogus"), Options{}); err == nil {
		t.Error("unknown depth must error")
	}
}

// TestMarshalCompactParity asserts the two serializers emit the same
// object: Marshal (indented) and MarshalCompact (one line) parse to
// identical JSON (reflect.DeepEqual on the decoded values) — only the
// whitespace differs.
func TestMarshalCompactParity(t *testing.T) {
	seedContextRepo(t, nil)
	r := openRuntime(t)
	obj, err := New(r).Build("eka-view-fixture/bug:delta", "eka-view-fixture", DepthDependency, Options{})
	if err != nil {
		t.Fatal(err)
	}
	pretty, err := obj.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	compact, err := obj.MarshalCompact()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(pretty), "\n") || !strings.HasSuffix(string(compact), "\n") {
		t.Error("both serializers must end in a single trailing newline")
	}
	var a, b any
	if err := json.Unmarshal(pretty, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(compact, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Error("Marshal and MarshalCompact must parse to identical JSON")
	}
	if strings.Count(string(compact), "\n") != 1 || !strings.HasSuffix(string(compact), "\n") {
		t.Error("MarshalCompact must be a single line plus the trailing newline")
	}
}

// TestStrataRespectsStratification asserts the strata landscape of a
// broad context (ctr:wave-1: every ticket deriving from the
// container): every entry's stratum field matches
// conformance.Stratum(entry domain), the strata are ascending, and
// the units within a stratum are sorted by canonical form.
func TestStrataRespectsStratification(t *testing.T) {
	seedContextRepo(t, nil)
	r := openRuntime(t)
	obj, err := New(r).Build("eka-view-fixture/ctr:wave-1", "eka-view-fixture", DepthDependency, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.Summary.Units != 8 {
		t.Errorf("summary units = %d, want 8 (the eight deriving tickets)", obj.Summary.Units)
	}
	prev := 0
	unitCount := 0
	for i, st := range obj.Strata {
		if st.Stratum <= prev {
			t.Errorf("strata must be ascending, stratum %d after %d", st.Stratum, prev)
		}
		prev = st.Stratum
		for j, u := range st.Units {
			unitCount++
			want := conformance.Stratum(conformance.Domain(u.Domain))
			if u.Stratum != want {
				t.Errorf("stratum %d unit %s: entry stratum = %d, want conformance.Stratum(%q) = %d",
					i, u.LineForm, u.Stratum, u.Domain, want)
			}
			if j > 0 && st.Units[j-1].CanonicalForm >= u.CanonicalForm {
				t.Errorf("stratum %d units must be sorted by canonical form", i)
			}
		}
	}
	if unitCount != obj.Summary.Units {
		t.Errorf("strata unit count = %d, want %d (every collected unit in exactly one stratum)",
			unitCount, obj.Summary.Units)
	}
}

// TestBuildConstraintClosureCap seeds 66 higher-authority findings
// (Discovery/1) referenced by a ticket deriving from the focus story's
// ticket neighborhood and asserts the deterministic cap of the
// engineering closure: exactly 64 of them join the collection (the
// first 64 in canonical-form order — context-cap-01..64), the 65th
// and 66th stay out, and engineering units == dependency units + 64
// exactly. Exercises the branch no other test reaches: the cap return
// of expandConstraints.
func TestBuildConstraintClosureCap(t *testing.T) {
	seedContextRepo(t, func(repo string) {
		// The 66 findings (stratum 1 — higher authority than the
		// Execution/4 focus). Zero-padded ids: canonical-form order
		// equals numeric order.
		for i := 1; i <= 66; i++ {
			id := fmt.Sprintf("context-cap-%02d", i)
			fnd := fmt.Sprintf(`---
namespace: eka-view-fixture
type: fnd
id: %s
instance-version: 1
revision: 1
content-state: draft
existence-state: active
dimension: research
author: Engineering Architecture
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on: []
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering Architecture
  - date: 2026-08-05
    domain: content-state
    from: "-"
    to: draft
    by: Engineering Architecture
---
# FND — Context Cap %s

## Purpose

Exercise the closure cap.

## Content

Findings.

## Investigation Summary

Summary.

## Conclusion

Conclusion.
`, id, id)
			if err := os.WriteFile(filepath.Join(repo, "docs", "research", "fnd-"+id+".md"), []byte(fnd), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// The ticket: derives from the container (Rule 8), the focus
		// story and all 66 findings — the findings become hop-2
		// candidates of the closure.
		var derives strings.Builder
		derives.WriteString("derives-from:\n  - ctr:wave-1\n  - sto:alpha\n")
		for i := 1; i <= 66; i++ {
			fmt.Fprintf(&derives, "  - fnd:context-cap-%02d\n", i)
		}
		tkt := fmt.Sprintf(`---
namespace: eka-view-fixture
type: tkt
id: context-cap
instance-version: 1
revision: 1
author: Engineering Architecture
created: 2026-08-05
updated: 2026-08-05
supersedes: []
%sdepends-on: []
---

> Generated — State Projection. Do NOT edit state here; refresh on read.

# Ticket — Context Cap

## Commands

- run the context cap.

## Projected Status

Projected from the owner work item.
`, derives.String())
		if err := os.WriteFile(filepath.Join(repo, "docs", "operating", "projections", "tkt-context-cap.md"), []byte(tkt), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	r := openRuntime(t)
	dep, err := New(r).Build("eka-view-fixture/sto:alpha", "eka-view-fixture", DepthDependency, Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := New(r).Build("eka-view-fixture/sto:alpha", "eka-view-fixture", DepthEngineering, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Dependency: the three tickets deriving from sto:alpha (the two
	// fixture tickets plus the seeded context-cap ticket). The 66
	// findings are hop-2 — outside the dependency collection.
	if dep.Summary.Units != 3 {
		t.Errorf("dependency units = %d, want 3 (the three deriving tickets)", dep.Summary.Units)
	}
	// Engineering: the closure adds exactly 64 findings — the cap
	// returns after the first 64 in canonical-form order
	// (context-cap-01..64); context-cap-65 and -66 stay out.
	if eng.Summary.Units != dep.Summary.Units+maxConstraintClosure {
		t.Errorf("engineering units = %d, want dependency %d + cap %d",
			eng.Summary.Units, dep.Summary.Units, maxConstraintClosure)
	}
	if len(eng.Sections.Constraints) != maxConstraintClosure {
		t.Fatalf("constraints = %d entries, want the cap %d", len(eng.Sections.Constraints), maxConstraintClosure)
	}
	for i, c := range eng.Sections.Constraints {
		if c.Role != "constraint" {
			t.Errorf("constraint %d role = %q, want constraint", i, c.Role)
		}
	}
	first, last := eng.Sections.Constraints[0], eng.Sections.Constraints[len(eng.Sections.Constraints)-1]
	if first.LineForm != "eka-view-fixture/fnd:context-cap-01" || last.LineForm != "eka-view-fixture/fnd:context-cap-64" {
		t.Errorf("constraint window = %s .. %s, want fnd:context-cap-01 .. fnd:context-cap-64",
			first.LineForm, last.LineForm)
	}
	for _, c := range eng.Sections.Constraints {
		if c.LineForm == "eka-view-fixture/fnd:context-cap-65" || c.LineForm == "eka-view-fixture/fnd:context-cap-66" {
			t.Errorf("constraint %s must stay outside the cap window", c.LineForm)
		}
	}
	// The 64 findings join the stratum-1 group of the landscape.
	if len(eng.Strata) != 2 || len(eng.Strata[0].Units) != maxConstraintClosure {
		t.Errorf("strata = %+v, want Discovery/1 with the 64 capped findings then Execution/4", eng.Strata)
	}
}

// TestBuildUpstreamFirstRoleWins seeds a ticket with two relationship
// types pointing at the SAME target (depends-on AND derives-from ->
// sto:beta) and asserts the first-role-wins contract: the unit appears
// once in a section with the FIRST edge type in stored (type, target)
// order ("depends-on" < "derives-from" — the dedup keeps the first
// role, deterministic by construction).
func TestBuildUpstreamFirstRoleWins(t *testing.T) {
	seedContextRepo(t, func(repo string) {
		tkt := `---
namespace: eka-view-fixture
type: tkt
id: multi-role
instance-version: 1
revision: 1
author: Engineering Architecture
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from:
  - ctr:wave-1
  - sto:alpha
  - sto:beta
depends-on:
  - sto:beta
---

> Generated — State Projection. Do NOT edit state here; refresh on read.

# Ticket — Multi Role

## Commands

- run the multi-role test.

## Projected Status

Projected from the owner work item.
`
		if err := os.WriteFile(filepath.Join(repo, "docs", "operating", "projections", "tkt-multi-role.md"), []byte(tkt), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	r := openRuntime(t)
	obj, err := New(r).Build("eka-view-fixture/tkt:multi-role", "eka-view-fixture", DepthDependency, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Upstream: the three distinct targets, each exactly once. The
	// first edge type in stored (type, target) order wins: for
	// sto:beta that is "depends-on" (depends-on < derives-from).
	if len(obj.Sections.Upstream) != 3 {
		t.Fatalf("upstream = %d entries, want 3 (ctr:wave-1, sto:alpha, sto:beta)", len(obj.Sections.Upstream))
	}
	seen := map[string]int{}
	for _, e := range obj.Sections.Upstream {
		seen[e.LineForm]++
		if e.LineForm == "eka-view-fixture/sto:beta" && e.Role != "depends-on" {
			t.Errorf("sto:beta upstream role = %q, want the first edge type depends-on", e.Role)
		}
	}
	for _, form := range []string{"eka-view-fixture/ctr:wave-1", "eka-view-fixture/sto:alpha", "eka-view-fixture/sto:beta"} {
		if seen[form] != 1 {
			t.Errorf("upstream %s appears %d times, want exactly once", form, seen[form])
		}
	}
	// Dependencies: the same three targets, role = the dependency edge
	// type of each (sto:beta keeps depends-on — the depRole map is
	// first-wins over the From scan order).
	if len(obj.Sections.Dependencies) != 3 {
		t.Fatalf("dependencies = %d entries, want 3", len(obj.Sections.Dependencies))
	}
	for _, e := range obj.Sections.Dependencies {
		if e.LineForm == "eka-view-fixture/sto:beta" && e.Role != "depends-on" {
			t.Errorf("sto:beta dependency role = %q, want depends-on", e.Role)
		}
		if e.LineForm == "eka-view-fixture/ctr:wave-1" && e.Role != "derives-from" {
			t.Errorf("ctr:wave-1 dependency role = %q, want derives-from", e.Role)
		}
	}
}

// TestBuildEmptyNeighborhood builds the dependency context of a
// relationship-free fixture unit (adr:001-login-serialization) and
// asserts the empty-neighborhood contract: no collected units, no
// sections, an empty strata list that is STILL present in the JSON
// ("strata": [] — the stable schema position, mirrored by the always-
// present "sections" object).
func TestBuildEmptyNeighborhood(t *testing.T) {
	seedContextRepo(t, nil)
	r := openRuntime(t)
	obj, err := New(r).Build("eka-view-fixture/adr:001-login-serialization", "eka-view-fixture", DepthDependency, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.Summary.Units != 0 || obj.Summary.Sections != 0 {
		t.Errorf("summary = %+v, want {1 0 0 1} (no neighbors, history only)", obj.Summary)
	}
	if len(obj.Strata) != 0 {
		t.Errorf("strata = %d groups, want none", len(obj.Strata))
	}
	for _, list := range [][]Entry{obj.Sections.Upstream, obj.Sections.Downstream, obj.Sections.Dependencies,
		obj.Sections.Constraints, obj.Sections.Decisions, obj.Sections.Planning, obj.Sections.Review} {
		if len(list) != 0 {
			t.Errorf("empty sections must stay empty, got %d entries", len(list))
		}
	}
	out, err := obj.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"strata": []`) {
		t.Errorf("serialized object must carry the empty strata list explicitly, got:\n%s", out)
	}
}

// copyFixtureLocal is the local copy helper of the contexts tests
// (the cmd package test helpers are unreachable here by design — the
// contexts tests must not import cmd).
func copyFixtureLocal(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// bytesEqual compares two byte slices (the test-local alias keeps the
// assertions readable).
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
