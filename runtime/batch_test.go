package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file tests the batch authoring API: NewDraftBatch (atomic
// multi-scaffold: all-or-nothing rollback, duplicate rejection, content
// merge) and PublishBatch (topological order over the draft graph,
// cycle and dangling-reference refusal, per-draft atomic publish).

// planningUnitBatch is the acceptance shape of the batch feature: a
// planning unit — scope, plan, work items, container and tickets — with
// the work-item text's edges (sto depends-on ctr, tkt derives-from
// ctr+sto, plan derives-from scp). Every target is publishable as
// scaffolded, so the batch must publish clean in topological order.
func planningUnitBatch(ns string) []BatchDraft {
	return []BatchDraft{
		{Type: "scp", ID: "product-v1", Dimension: "planning"},
		{Type: "plan", ID: "roadmap-v2", Dimension: "planning",
			Relationships: []exchange.Relationship{{Type: "derives-from", Target: "scp:product-v1"}}},
		{Type: "ctr", ID: "wave-7",
			Relationships: []exchange.Relationship{{Type: "depends-on", Target: "plan:roadmap-v2"}}},
		{Type: "sto", ID: "item-1",
			Relationships: []exchange.Relationship{{Type: "depends-on", Target: "ctr:wave-7"}}},
		{Type: "sto", ID: "item-2",
			Relationships: []exchange.Relationship{{Type: "depends-on", Target: "ctr:wave-7"}}},
		{Type: "tkt", ID: "ticket-1",
			Relationships: []exchange.Relationship{
				{Type: "derives-from", Target: "ctr:wave-7"},
				{Type: "derives-from", Target: "sto:item-1"},
			}},
		{Type: "tkt", ID: "ticket-2",
			Relationships: []exchange.Relationship{
				{Type: "derives-from", Target: "ctr:wave-7"},
				{Type: "derives-from", Target: "sto:item-2"},
			}},
	}
}

// wantPlanningUnitOrder is the deterministic topological order of the
// planning-unit batch: referenced drafts first, ties broken by
// identity.
var wantPlanningUnitOrder = []string{
	"feather/scp:product-v1",
	"feather/plan:roadmap-v2",
	"feather/ctr:wave-7",
	"feather/sto:item-1",
	"feather/sto:item-2",
	"feather/tkt:ticket-1",
	"feather/tkt:ticket-2",
}

// --- NewDraftBatch -----------------------------------------------------

// TestNewDraftBatchCreatesAll: one batch invocation scaffolds every
// target in declaration order with its relationships, and the set
// validates clean (the acceptance criterion: a planning unit is created
// in one command).
func TestNewDraftBatchCreatesAll(t *testing.T) {
	r, project := draftRuntime(t)
	res, err := Authoring.NewDraftBatch(r, NewDraftBatchRequest{
		Project:   project,
		Namespace: "feather",
		By:        conformance.User("Ada Lovelace"),
		Drafts:    planningUnitBatch("feather"),
	})
	if err != nil {
		t.Fatalf("NewDraftBatch: %v", err)
	}
	if len(res.Created) != 7 {
		t.Fatalf("Created = %d drafts, want 7", len(res.Created))
	}
	for i, want := range []string{"scp:product-v1", "plan:roadmap-v2", "ctr:wave-7", "sto:item-1", "sto:item-2", "tkt:ticket-1", "tkt:ticket-2"} {
		got := res.Created[i].Type + ":" + res.Created[i].ID
		if got != want {
			t.Errorf("Created[%d] = %s, want %s", i, got, want)
		}
	}
	// The tkt- template carries the projection header (rule 8's header
	// requirement), so the scaffolded ticket is publishable as-is once
	// its container is published.
	data, err := os.ReadFile(res.Created[5].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"derivesFrom": [`) {
		t.Errorf("tkt draft missing derivesFrom frontmatter:\n%s", data)
	}
	if !strings.Contains(string(data), "State Projection") {
		t.Errorf("tkt draft missing the projection header (rule 8):\n%s", data)
	}
}

// TestNewDraftBatchAtomicRollback: when one target cannot be scaffolded
// (here: a tkt- without a container reference, violating rule 8's
// scaffold guard), the run refuses and removes the drafts it created —
// no partial set is left behind.
func TestNewDraftBatchAtomicRollback(t *testing.T) {
	r, project := draftRuntime(t)
	_, err := Authoring.NewDraftBatch(r, NewDraftBatchRequest{
		Project:   project,
		Namespace: "feather",
		By:        conformance.User("Ada Lovelace"),
		Drafts: []BatchDraft{
			{Type: "sto", ID: "ok-1"},
			{Type: "sto", ID: "ok-2"},
			{Type: "tkt", ID: "orphan" /* no ctr derives-from */},
		},
	})
	if err == nil {
		t.Fatal("NewDraftBatch = nil error, want refusal")
	}
	for _, want := range []string{"draft 3 of 3", "tkt:orphan", "were removed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err, want)
		}
	}
	// Both scaffolded-before-the-failure drafts are gone.
	for _, id := range []string{"ok-1", "ok-2"} {
		p := filepath.Join(r.Path(), "drafts", project, "sto-"+id+".json")
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("draft %s must be rolled back, stat err = %v", p, err)
		}
	}
}

// TestNewDraftBatchDuplicateIdentity: two batch targets sharing one
// identity are refused before any file is written.
func TestNewDraftBatchDuplicateIdentity(t *testing.T) {
	r, project := draftRuntime(t)
	_, err := Authoring.NewDraftBatch(r, NewDraftBatchRequest{
		Project:   project,
		Namespace: "feather",
		Drafts: []BatchDraft{
			{Type: "sto", ID: "dup"},
			{Type: "sto", ID: "dup"},
		},
	})
	if err == nil {
		t.Fatal("NewDraftBatch = nil error, want duplicate-identity refusal")
	}
	if !strings.Contains(err.Error(), "share the identity sto:dup") {
		t.Errorf("refusal = %q, want duplicate-identity message", err)
	}
}

// TestNewDraftBatchContentMerged: the per-target inline content is
// merged over the required-section placeholders.
func TestNewDraftBatchContentMerged(t *testing.T) {
	r, project := draftRuntime(t)
	res, err := Authoring.NewDraftBatch(r, NewDraftBatchRequest{
		Project:   project,
		Namespace: "feather",
		Drafts: []BatchDraft{
			{Type: "sto", ID: "filled", Content: map[string]any{
				"description":        "batch body",
				"acceptanceCriteria": "ac",
			}},
		},
	})
	if err != nil {
		t.Fatalf("NewDraftBatch: %v", err)
	}
	data, err := os.ReadFile(res.Created[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"description": "batch body"`) {
		t.Errorf("draft content missing the batch content:\n%s", data)
	}
}

// --- PublishBatch ------------------------------------------------------

// TestPublishBatchTopologicalOrder: the planning-unit batch is
// published in one run in the deterministic topological order
// (referenced drafts first) and every R-rule gate passes — in
// particular rule 8's tkt -> ctr resolution, which only succeeds
// because the container is published before its tickets.
func TestPublishBatchTopologicalOrder(t *testing.T) {
	r, project := draftRuntime(t)
	if _, err := Authoring.NewDraftBatch(r, NewDraftBatchRequest{
		Project:   project,
		Namespace: "feather",
		By:        conformance.User("Ada Lovelace"),
		Drafts:    planningUnitBatch("feather"),
	}); err != nil {
		t.Fatalf("NewDraftBatch: %v", err)
	}

	res, err := Authoring.PublishBatch(r, PublishBatchOptions{})
	if err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}
	if len(res.Published) != 7 {
		t.Fatalf("Published = %d, want 7", len(res.Published))
	}
	for i, want := range wantPlanningUnitOrder {
		if got := res.Published[i].Form; got != want+":1" {
			t.Errorf("Published[%d] = %s, want %s:1", i, got, want)
		}
	}
	// Every draft file is gone: the single-use ticket was consumed.
	for _, d := range []string{"scp-product-v1", "plan-roadmap-v2", "ctr-wave-7", "sto-item-1", "sto-item-2", "tkt-ticket-1", "tkt-ticket-2"} {
		p := filepath.Join(r.Path(), "drafts", project, d+".json")
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("draft %s must be consumed by the batch publish, stat err = %v", p, err)
		}
	}
}

// TestPublishBatchEmptyBacklog: no pending drafts is a valid no-op.
func TestPublishBatchEmptyBacklog(t *testing.T) {
	r, _ := draftRuntime(t)
	res, err := Authoring.PublishBatch(r, PublishBatchOptions{})
	if err != nil {
		t.Fatalf("PublishBatch on an empty backlog: %v", err)
	}
	if len(res.Published) != 0 {
		t.Errorf("Published = %d, want 0", len(res.Published))
	}
}

// TestPublishBatchCycleRefusal: a dependency cycle among pending drafts
// is refused before anything is published, naming the cycle members.
func TestPublishBatchCycleRefusal(t *testing.T) {
	r, project := draftRuntime(t)
	if _, err := Authoring.NewDraftBatch(r, NewDraftBatchRequest{
		Project:   project,
		Namespace: "feather",
		Drafts: []BatchDraft{
			{Type: "sto", ID: "a", Relationships: []exchange.Relationship{{Type: "depends-on", Target: "sto:b"}}},
			{Type: "sto", ID: "b", Relationships: []exchange.Relationship{{Type: "depends-on", Target: "sto:a"}}},
		},
	}); err != nil {
		t.Fatalf("NewDraftBatch: %v", err)
	}
	res, err := Authoring.PublishBatch(r, PublishBatchOptions{})
	var ce *BatchCycleError
	if !errors.As(err, &ce) {
		t.Fatalf("PublishBatch err = %v, want *BatchCycleError", err)
	}
	if got := strings.Join(ce.Drafts, ","); got != "sto:a,sto:b" {
		t.Errorf("cycle members = %q, want sto:a,sto:b", got)
	}
	// A refused run publishes nothing (res is nil: pre-flight refusal).
	if res != nil && len(res.Published) != 0 {
		t.Errorf("Published = %d, want 0 (nothing before the refusal)", len(res.Published))
	}
}

// TestPublishBatchSelfReferenceIsCycle: a draft referencing itself is a
// length-1 cycle and is refused with the cycle class.
func TestPublishBatchSelfReferenceIsCycle(t *testing.T) {
	r, project := draftRuntime(t)
	if _, err := Authoring.NewDraftBatch(r, NewDraftBatchRequest{
		Project:   project,
		Namespace: "feather",
		Drafts: []BatchDraft{
			{Type: "sto", ID: "loop", Relationships: []exchange.Relationship{{Type: "depends-on", Target: "sto:loop"}}},
		},
	}); err != nil {
		t.Fatalf("NewDraftBatch: %v", err)
	}
	_, err := Authoring.PublishBatch(r, PublishBatchOptions{})
	var ce *BatchCycleError
	if !errors.As(err, &ce) {
		t.Fatalf("PublishBatch err = %v, want *BatchCycleError", err)
	}
	if len(ce.Drafts) != 1 || ce.Drafts[0] != "sto:loop" {
		t.Errorf("cycle members = %v, want [sto:loop]", ce.Drafts)
	}
}

// TestPublishBatchUnresolvedRefusal: a pending draft referencing a
// target that is neither pending nor published is refused before
// anything is published, naming the draft and the target.
func TestPublishBatchUnresolvedRefusal(t *testing.T) {
	r, project := draftRuntime(t)
	if _, err := Authoring.NewDraftBatch(r, NewDraftBatchRequest{
		Project:   project,
		Namespace: "feather",
		Drafts: []BatchDraft{
			{Type: "sto", ID: "dangling", Relationships: []exchange.Relationship{{Type: "depends-on", Target: "ctr:ghost"}}},
		},
	}); err != nil {
		t.Fatalf("NewDraftBatch: %v", err)
	}
	res, err := Authoring.PublishBatch(r, PublishBatchOptions{})
	var ue *BatchUnresolvedError
	if !errors.As(err, &ue) {
		t.Fatalf("PublishBatch err = %v, want *BatchUnresolvedError", err)
	}
	if ue.Draft != "sto:dangling" || ue.Target != "ctr:ghost" {
		t.Errorf("unresolved = draft %q target %q, want sto:dangling / ctr:ghost", ue.Draft, ue.Target)
	}
	// A refused run publishes nothing (res is nil: pre-flight refusal).
	if res != nil && len(res.Published) != 0 {
		t.Errorf("Published = %d, want 0", len(res.Published))
	}
}

// TestPublishBatchPublishedReferenceResolves: a draft referencing an
// object that is already published (outside the pending set) publishes
// cleanly — the pre-flight gate accepts store-resolvable targets.
func TestPublishBatchPublishedReferenceResolves(t *testing.T) {
	r, project := draftRuntime(t)
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "ctr", ID: "wave-7",
		Relationships: []exchange.Relationship{{Type: "depends-on", Target: "plan:roadmap-v2"}},
	}); err != nil {
		t.Fatalf("NewDraft(ctr): %v", err)
	}
	// The plan must exist in the store first: publish it from a draft
	// created by the batch (the batch is the only source of plans here,
	// so scaffold + publish it individually).
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "plan", ID: "roadmap-v2", Dimension: "planning",
	}); err != nil {
		t.Fatalf("NewDraft(plan): %v", err)
	}
	if _, err := Authoring.Publish(r, "feather/plan:roadmap-v2", PublishOptions{}); err != nil {
		t.Fatalf("Publish(plan): %v", err)
	}

	// Now publish the ctr via the batch: its plan- reference resolves
	// in the store, so the batch must not refuse it as unresolved.
	if _, err := Authoring.PublishBatch(r, PublishBatchOptions{}); err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}
}

// TestPublishBatchStopsOnValidationFailure: a draft failing CKO-level
// validation stops the run; the drafts ordered before it are published,
// the failing draft and everything after it stay pending.
func TestPublishBatchStopsOnValidationFailure(t *testing.T) {
	r, project := draftRuntime(t)
	// The tkt- carries a corrupted commands section (not the projection
	// header), so rule 8 blocks it — and it is ordered after the ctr it
	// references, so the ctr must already be published when the run
	// stops.
	if _, err := Authoring.NewDraftBatch(r, NewDraftBatchRequest{
		Project:   project,
		Namespace: "feather",
		Drafts: []BatchDraft{
			{Type: "ctr", ID: "wave-7",
				Relationships: []exchange.Relationship{{Type: "depends-on", Target: "plan:roadmap-v2"}}},
			{Type: "plan", ID: "roadmap-v2", Dimension: "planning"},
			{Type: "tkt", ID: "broken",
				Relationships: []exchange.Relationship{{Type: "derives-from", Target: "ctr:wave-7"}},
				Content:       map[string]any{"commands": "not a projection header"}},
		},
	}); err != nil {
		t.Fatalf("NewDraftBatch: %v", err)
	}
	res, err := Authoring.PublishBatch(r, PublishBatchOptions{})
	var pe *PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("PublishBatch err = %v, want *PublishError", err)
	}
	if len(res.Published) != 2 {
		t.Fatalf("Published = %d, want 2 (plan + ctr before the failure)", len(res.Published))
	}
	// The failing tkt- stays pending.
	if _, serr := os.Stat(filepath.Join(r.Path(), "drafts", project, "tkt-broken.json")); serr != nil {
		t.Errorf("the failing draft must stay pending: %v", serr)
	}
}
