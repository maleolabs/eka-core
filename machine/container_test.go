package machine

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// containerUnit builds one canonical unit for the containers tests:
// identity line at instance-version 1, a state vector from the given
// domain map, the given created date and relationships canonicalized
// to the RSF identity form (targets written in the authoring reference
// convention).
func containerUnit(t *testing.T, ns, typeToken, id string, v int, created string, states map[string]string, rels ...exchange.Relationship) *exchange.Unit {
	t.Helper()
	u := &exchange.Unit{
		Identity:              exchange.Identity{Namespace: ns, Type: typeToken, ID: id, InstanceVersion: v},
		CanonicalIdentityForm: ns + "/" + typeToken + ":" + id + ":" + itoa(v),
		Created:               created,
		StateVector: exchange.StateVector{
			ContainerState: states[conformance.DomainContainerState],
			ExecutionState: states[conformance.DomainExecutionState],
			ExistenceState: states[conformance.DomainExistenceState],
		},
		Relationships: []exchange.Relationship{},
	}
	for _, r := range rels {
		ref, err := conformance.ParseReference(r.Target, ns, typeToken)
		if err != nil {
			t.Fatalf("containerUnit: relationship target %q: %v", r.Target, err)
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

// containersFixtureUnits builds the hand-built unit set of the
// containers tests: two acme containers (wave-1 active with a plan,
// tickets and work items; wave-0 completed with an active->completed
// transition), a multi-instance container line (dedup: v2 wins — the
// latest knowledge version, ADR-025), a
// third container in another namespace (sorting), the tickets and work
// items, and an rvw- unit that must be ignored by the derivation.
func containersFixtureUnits(t *testing.T) []*exchange.Unit {
	t.Helper()
	return []*exchange.Unit{
		// The active container with a plan, two tickets with work items
		// and one unresolved ticket. Instance 2 of the same line exists
		// too — the highest instance (v2) wins the derivation (its
		// instance 1 sibling carries the plan relationship).
		containerUnit(t, "acme", "ctr", "wave-1", 1, "2026-08-05",
			map[string]string{conformance.DomainContainerState: "active", conformance.DomainExistenceState: "active"},
			exchange.Relationship{Type: "depends-on", Target: "plan:roadmap-2026:1"}),
		containerUnit(t, "acme", "ctr", "wave-1", 2, "2026-08-06",
			map[string]string{conformance.DomainContainerState: "active", conformance.DomainExistenceState: "active"}),
		// The completed container with an active -> completed
		// container-state transition in its change log.
		func() *exchange.Unit {
			u := containerUnit(t, "acme", "ctr", "wave-0", 1, "2026-08-01",
				map[string]string{conformance.DomainContainerState: "completed", conformance.DomainExistenceState: "active"})
			u.ChangeLog = []exchange.ChangeLogEntry{
				{Date: "2026-08-01", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
				{Date: "2026-08-01", Domain: "container-state", From: "-", To: "active", By: conformance.User("Engineering")},
				{Date: "2026-08-04", Domain: "container-state", From: "active", To: "completed", By: conformance.User("Engineering")},
			}
			return u
		}(),
		// A third container in another namespace (canonical-form
		// sorting: acme < beta).
		containerUnit(t, "beta", "ctr", "first", 1, "2026-08-02",
			map[string]string{conformance.DomainContainerState: "completed", conformance.DomainExistenceState: "active"}),
		// Tickets: two work-item tickets of wave-1, one unresolved
		// ticket of wave-1, one ticket of wave-0.
		containerUnit(t, "acme", "tkt", "sto-alpha", 1, "2026-08-05", nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-1"},
			exchange.Relationship{Type: "derives-from", Target: "sto:alpha"}),
		containerUnit(t, "acme", "tkt", "ts-gamma", 1, "2026-08-05", nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-1"},
			exchange.Relationship{Type: "derives-from", Target: "ts:gamma"}),
		containerUnit(t, "acme", "tkt", "unresolved", 1, "2026-08-05", nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-1"}),
		containerUnit(t, "acme", "tkt", "sto-beta", 1, "2026-08-01", nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-0"},
			exchange.Relationship{Type: "derives-from", Target: "sto:beta"}),
		// The work items.
		containerUnit(t, "acme", "sto", "alpha", 1, "2026-08-05",
			map[string]string{conformance.DomainExecutionState: "planned", conformance.DomainExistenceState: "active"}),
		containerUnit(t, "acme", "ts", "gamma", 1, "2026-08-05",
			map[string]string{conformance.DomainExecutionState: "in-progress", conformance.DomainExistenceState: "active"}),
		containerUnit(t, "acme", "sto", "beta", 1, "2026-08-01",
			map[string]string{conformance.DomainExecutionState: "done", conformance.DomainExistenceState: "active"}),
		// A review unit deriving from wave-1: never a ticket, never a
		// work item — the derivation must ignore it.
		containerUnit(t, "acme", "rvw", "review-1", 1, "2026-08-05",
			map[string]string{conformance.DomainContentState: "approved", conformance.DomainExistenceState: "active"},
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-1"}),
	}
}

// TestNewContainerCollectionDerivation: the containers projection
// derives each container line from its lowest instance: canonical
// form, plan (first depends-on target verbatim), tickets and items
// (relationship-only membership through the tickets' derives-from),
// started/ended dates and the container state.
func TestNewContainerCollectionDerivation(t *testing.T) {
	col, err := NewContainerCollection(containersFixtureUnits(t))
	if err != nil {
		t.Fatal(err)
	}
	if col.Schema != Schema || col.Collection != "containers" {
		t.Errorf("collection header = %s/%s, want eka-cko-v2/containers", col.Schema, col.Collection)
	}
	if col.Count != 3 || len(col.Containers) != 3 {
		t.Fatalf("count = %d (containers %d), want 3 (multi-instance line deduplicated)", col.Count, len(col.Containers))
	}
	if col.Pagination != nil {
		t.Errorf("default collection must carry no pagination, got %+v", col.Pagination)
	}
	// Sorted by canonical form: acme before beta.
	wantForms := []string{"acme/ctr:wave-0", "acme/ctr:wave-1", "beta/ctr:first"}
	for i, w := range wantForms {
		if col.Containers[i].CanonicalForm != w {
			t.Errorf("containers[%d].CanonicalForm = %q, want %q", i, col.Containers[i].CanonicalForm, w)
		}
	}
	wave1 := col.Containers[1]
	if wave1.ID != "wave-1" || wave1.ContainerState != "active" {
		t.Errorf("wave-1 identity/state = %q/%q", wave1.ID, wave1.ContainerState)
	}
	// Plan: the first depends-on target, stored form verbatim (the
	// highest instance wins — instance 2 has no depends-on, so the
	// projection shows no plan; the relationship lives in instance 1).
	if wave1.Plan != "" {
		t.Errorf("wave-1 plan = %q, want \"\" (highest instance carries no depends-on)", wave1.Plan)
	}
	// Tickets: 3 (sto-alpha, ts-gamma, unresolved — the rvw unit is
	// never a ticket); items: 2 (alpha, gamma — the unresolved ticket
	// contributes nothing).
	if wave1.Tickets != 3 || wave1.Items != 2 {
		t.Errorf("wave-1 tickets/items = %d/%d, want 3/2", wave1.Tickets, wave1.Items)
	}
	if wave1.StartedAt != "2026-08-06" || wave1.EndedAt != "" {
		t.Errorf("wave-1 started/ended = %q/%q, want 2026-08-06/\"\" (still active)", wave1.StartedAt, wave1.EndedAt)
	}
	wave0 := col.Containers[0]
	if wave0.ContainerState != "completed" || wave0.Plan != "" {
		t.Errorf("wave-0 state/plan = %q/%q, want completed/\"\" (no depends-on)", wave0.ContainerState, wave0.Plan)
	}
	if wave0.Tickets != 1 || wave0.Items != 1 {
		t.Errorf("wave-0 tickets/items = %d/%d, want 1/1", wave0.Tickets, wave0.Items)
	}
	// EndedAt: only the container with the active -> completed
	// transition in its change log carries the completion date.
	if wave0.EndedAt != "2026-08-04" {
		t.Errorf("wave-0 endedAt = %q, want 2026-08-04 (the change-log date)", wave0.EndedAt)
	}
	if wave0.StartedAt != "2026-08-01" {
		t.Errorf("wave-0 startedAt = %q, want 2026-08-01 (the unit's created date)", wave0.StartedAt)
	}
	third := col.Containers[2]
	if third.Tickets != 0 || third.Items != 0 || third.Plan != "" {
		t.Errorf("beta/ctr:first must be an empty container, got tickets/items/plan = %d/%d/%q", third.Tickets, third.Items, third.Plan)
	}
}

// TestContainerCollectionEmpty: an empty unit list yields a containers
// collection with count 0 and an empty container list (never null).
func TestContainerCollectionEmpty(t *testing.T) {
	col, err := NewContainerCollection(nil)
	if err != nil {
		t.Fatal(err)
	}
	if col.Count != 0 || len(col.Containers) != 0 {
		t.Errorf("count = %d, containers = %d, want 0 and 0", col.Count, len(col.Containers))
	}
	got, err := col.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schema": "eka-cko-v2",
  "collection": "containers",
  "count": 0,
  "containers": []
}
`
	if string(got) != want {
		t.Errorf("empty containers collection differs:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestContainerCollectionFilterActive: FilterActive keeps only the
// active containers; Count narrows to the filtered population (the
// total before any page window).
func TestContainerCollectionFilterActive(t *testing.T) {
	col, err := NewContainerCollection(containersFixtureUnits(t))
	if err != nil {
		t.Fatal(err)
	}
	col.FilterActive()
	if col.Count != 1 {
		t.Errorf("count = %d after FilterActive, want 1 (the filtered population)", col.Count)
	}
	if len(col.Containers) != 1 || col.Containers[0].CanonicalForm != "acme/ctr:wave-1" {
		t.Errorf("FilterActive kept %v, want only acme/ctr:wave-1", col.Containers)
	}
	if col.Containers[0].ContainerState != "active" {
		t.Errorf("kept container state = %q, want active", col.Containers[0].ContainerState)
	}
}

// TestContainerCollectionFilterContainer: FilterContainer keeps only
// the matching container and reports false (leaving the collection
// untouched) when nothing matches.
func TestContainerCollectionFilterContainer(t *testing.T) {
	col, err := NewContainerCollection(containersFixtureUnits(t))
	if err != nil {
		t.Fatal(err)
	}
	if ok := col.FilterContainer("acme/ctr:wave-0"); !ok {
		t.Fatal("FilterContainer(acme/ctr:wave-0) must be found")
	}
	if col.Count != 1 {
		t.Errorf("count = %d after FilterContainer, want 1 (the filtered population)", col.Count)
	}
	if len(col.Containers) != 1 || col.Containers[0].CanonicalForm != "acme/ctr:wave-0" {
		t.Errorf("FilterContainer kept %v, want only acme/ctr:wave-0", col.Containers)
	}
	col, err = NewContainerCollection(containersFixtureUnits(t))
	if err != nil {
		t.Fatal(err)
	}
	if ok := col.FilterContainer("acme/ctr:ghost"); ok {
		t.Error("FilterContainer(acme/ctr:ghost) must report not-found")
	}
	if len(col.Containers) != 3 {
		t.Errorf("a failed FilterContainer must leave the collection untouched, got %d containers", len(col.Containers))
	}
}

// TestContainerCollectionPage: Page windows the Containers slice,
// Count stays the total and the Pagination metadata is set; a limit of
// 0 (--offset without --limit) windows to the end.
func TestContainerCollectionPage(t *testing.T) {
	col, err := NewContainerCollection(containersFixtureUnits(t))
	if err != nil {
		t.Fatal(err)
	}
	col.Page(1, 1)
	if col.Count != 3 {
		t.Errorf("count = %d after Page, want 3 (the total)", col.Count)
	}
	if len(col.Containers) != 1 || col.Containers[0].CanonicalForm != "acme/ctr:wave-1" {
		t.Errorf("page 1 of limit 1 kept %v, want only acme/ctr:wave-1", col.Containers)
	}
	want := &Pagination{Offset: 1, Limit: 1, Page: 2, Total: 3, Pages: 3}
	if col.Pagination == nil || *col.Pagination != *want {
		t.Errorf("pagination = %+v, want %+v", col.Pagination, want)
	}

	// --offset without --limit: limit 0 windows to the end.
	col, err = NewContainerCollection(containersFixtureUnits(t))
	if err != nil {
		t.Fatal(err)
	}
	col.Page(2, 0)
	if len(col.Containers) != 1 || col.Containers[0].CanonicalForm != "beta/ctr:first" {
		t.Errorf("offset 2 without limit kept %v, want only beta/ctr:first", col.Containers)
	}
	want = &Pagination{Offset: 2, Limit: 0, Page: 1, Total: 3, Pages: 0}
	if col.Pagination == nil || *col.Pagination != *want {
		t.Errorf("pagination = %+v, want %+v", col.Pagination, want)
	}

	// An offset past the end: an empty window, still with metadata.
	col, err = NewContainerCollection(containersFixtureUnits(t))
	if err != nil {
		t.Fatal(err)
	}
	col.Page(5, 2)
	if len(col.Containers) != 0 {
		t.Errorf("offset past the end must yield an empty list, got %v", col.Containers)
	}
	want = &Pagination{Offset: 5, Limit: 2, Page: 3, Total: 3, Pages: 2}
	if col.Pagination == nil || *col.Pagination != *want {
		t.Errorf("pagination = %+v, want %+v", col.Pagination, want)
	}
}

// TestContainerCollectionMarshal: the default marshal carries no
// "pagination" key; after Page the pagination object is appended with
// the pinned field order; the compact form is a single line with the
// same document.
func TestContainerCollectionMarshal(t *testing.T) {
	col, err := NewContainerCollection(containersFixtureUnits(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := col.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, `"pagination"`) {
		t.Errorf("default marshal must not carry pagination:\n%s", s)
	}
	// The pinned container field order (highest instance wins: the
	// wave-1 line's v2 carries no depends-on, so plan is omitted).
	if !strings.Contains(s, `"canonicalForm": "acme/ctr:wave-1"`) ||
		!strings.Contains(s, `"id": "wave-1"`) ||
		!strings.Contains(s, `"items": 2`) ||
		!strings.Contains(s, `"tickets": 3`) ||
		!strings.Contains(s, `"startedAt": "2026-08-06"`) ||
		!strings.Contains(s, `"containerState": "active"`) {
		t.Errorf("wave-1 fields missing or out of order:\n%s", s)
	}
	// The completed wave-0 carries no plan (its own section only).
	start := strings.Index(s, `"canonicalForm": "acme/ctr:wave-0"`)
	end := strings.Index(s, `"canonicalForm": "acme/ctr:wave-1"`)
	wave0 := s[start:end]
	if strings.Contains(wave0, `"plan"`) {
		t.Errorf("wave-0 carries no depends-on, so plan must be omitted:\n%s", wave0)
	}
	if !strings.Contains(wave0, `"endedAt": "2026-08-04"`) {
		t.Errorf("wave-0 must carry its completion date:\n%s", wave0)
	}
	if strings.Contains(s, `"endedAt": "2026-08-05"`) {
		t.Errorf("the active wave-1 must not carry endedAt:\n%s", s)
	}

	col, err = NewContainerCollection(containersFixtureUnits(t))
	if err != nil {
		t.Fatal(err)
	}
	col.Page(0, 1)
	got, err = col.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s = string(got)
	if !strings.Contains(s, `"pagination": {`) {
		t.Errorf("paged marshal must carry the pagination object:\n%s", s)
	}
	compact, err := col.MarshalCompact()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(compact), "\n") != 1 || !strings.HasSuffix(string(compact), "\n") {
		t.Errorf("compact must be one line plus a trailing newline, got %q", compact)
	}
	var a, b map[string]any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(compact, &b); err != nil {
		t.Fatal(err)
	}
	pa, _ := json.Marshal(a)
	pb, _ := json.Marshal(b)
	if string(pa) != string(pb) {
		t.Errorf("compact and pretty must carry the same document")
	}
}

// TestCollectionPage: the domain collection pages like the containers
// collection — the Units slice is windowed, Count stays the TOTAL and
// the Pagination metadata is set; the default marshal stays
// byte-identical (no pagination key).
func TestCollectionPage(t *testing.T) {
	units := containersFixtureUnits(t)
	col, err := NewCollection("Execution", units)
	if err != nil {
		t.Fatal(err)
	}
	before, err := col.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), `"pagination"`) {
		t.Errorf("default collection marshal must not carry pagination:\n%s", before)
	}
	total := len(col.Units)
	if total != 12 {
		t.Fatalf("fixture unit count = %d, want 12", total)
	}
	col.Page(1, 2)
	if col.Count != total {
		t.Errorf("count = %d after Page, want %d (the total)", col.Count, total)
	}
	if len(col.Units) != 2 {
		t.Errorf("page 1 of limit 2 must window to 2 units, got %d", len(col.Units))
	}
	want := &Pagination{Offset: 1, Limit: 2, Page: 1, Total: total, Pages: 6}
	if col.Pagination == nil || *col.Pagination != *want {
		t.Errorf("pagination = %+v, want %+v", col.Pagination, want)
	}
}
