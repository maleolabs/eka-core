package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
)

// This file tests the planning-state / container-state branches of the
// Authoring transition pipeline (plan- and ctr- targets): the
// forward-only draft -> approved plan table with the immutable lock
// refusal, and the three-state container table (planned -> active ->
// completed) — the activation (planned -> active) gated on the
// exactly-one-active rule and the plan-approval rule with the atomic
// plan lock, the completion (active -> completed) gated on the
// all-done-or-canceled membership rule. Fixtures are store-level seeds
// (the transition resolves the repository context from the cwd).

// transitionRuntime returns a Runtime plus a registered repository
// whose directory becomes the working directory, seeded with the
// eka.yaml identity file (the transition pipeline resolves the
// repository context from the cwd; the target namespace resolves from
// the metadata; FindRepo addresses the registry through the metadata
// identity pair).
func transitionRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	r := testRuntime(t)
	repoDir := t.TempDir()
	writeRuntimeEKAFile(t, repoDir, "proj", "repo", "test-ns")
	m := metadata.Metadata{Version: 1, Project: "proj", Name: "repo", Namespace: "test-ns"}
	project, _, _, err := r.ws.RegisterRepoMetadata(repoDir, m)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoDir)
	return r, project.ID
}

// planLog is the initial change-log of a plan unit ending in the given
// planning-state.
func planLog(state string) []exchange.ChangeLogEntry {
	return []exchange.ChangeLogEntry{
		{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Eng")},
		{Date: "2026-08-05", Domain: "content-state", From: "-", To: "draft", By: conformance.User("Eng")},
		{Date: "2026-08-05", Domain: "planning-state", From: "-", To: state, By: conformance.User("Eng")},
	}
}

// planUnit builds a plan unit with the given planning-state.
func planUnit(id string, version int, state string, log []exchange.ChangeLogEntry) *exchange.Unit {
	u := unit("test-ns", "plan", id, version, version)
	u.Classification = exchange.Classification{Dimension: "planning", Domain: "Planning"}
	u.StateVector = exchange.StateVector{ContentState: "draft", PlanningState: state, ExistenceState: "active"}
	u.ChangeLog = log
	return u
}

// planState reads the current planning-state of a plan line and its
// change-log length.
func planState(t *testing.T, r *Runtime, id string) (string, int) {
	t.Helper()
	units, err := r.ws.Store().UnitsByLine("test-ns", "plan", id)
	if err != nil || len(units) == 0 {
		t.Fatalf("UnitsByLine(plan:%s) = %d units (err %v)", id, len(units), err)
	}
	u := units[0]
	for _, cand := range units {
		if cand.Identity.InstanceVersion > u.Identity.InstanceVersion {
			u = cand
		}
	}
	return u.StateVector.PlanningState, len(u.ChangeLog)
}

// containerState reads the current container-state of a ctr line and
// its change-log length.
func containerState(t *testing.T, r *Runtime, id string) (string, int) {
	t.Helper()
	units, err := r.ws.Store().UnitsByLine("test-ns", "ctr", id)
	if err != nil || len(units) == 0 {
		t.Fatalf("UnitsByLine(ctr:%s) = %d units (err %v)", id, len(units), err)
	}
	u := units[0]
	for _, cand := range units {
		if cand.Identity.InstanceVersion > u.Identity.InstanceVersion {
			u = cand
		}
	}
	return u.StateVector.ContainerState, len(u.ChangeLog)
}

// containerWorld seeds the store with an active container ctr:wave-7
// (depends-on plan:roadmap-v1), a ticket per given work item (both
// deriving from the container AND the work item), and the work items
// themselves with the given execution-states.
func containerWorld(t *testing.T, r *Runtime, project string, states map[string]string) {
	t.Helper()
	ctr := unit("test-ns", "ctr", "wave-7", 1, 1)
	ctr.StateVector = exchange.StateVector{ContainerState: "active", ExistenceState: "active"}
	ctr.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "test-ns/plan:roadmap-v1"}}
	ctr.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-05", Domain: "container-state", From: "-", To: "active", By: conformance.User("Eng")},
		{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Eng")},
	}
	putUnit(t, r, ctr, project, "repo")
	for id, state := range states {
		tkt := unit("test-ns", "tkt", "t-"+id, 1, 1)
		tkt.StateVector = exchange.StateVector{}
		tkt.Relationships = []exchange.Relationship{
			{Type: "derives-from", Target: "test-ns/ctr:wave-7"},
			{Type: "derives-from", Target: "test-ns/sto:" + id},
		}
		putUnit(t, r, tkt, project, "repo")
		sto := unit("test-ns", "sto", id, 1, 1)
		sto.StateVector = exchange.StateVector{ExecutionState: state, ExistenceState: "active"}
		sto.ChangeLog = []exchange.ChangeLogEntry{
			{Date: "2026-08-05", Domain: "execution-state", From: "-", To: state, By: conformance.User("Eng")},
			{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Eng")},
		}
		putUnit(t, r, sto, project, "repo")
	}
}

// plannedCtr builds a PLANNED container unit (Option B: the template's
// initial container-state is planned; the change-log carries the
// "-" -> planned birth entry) deriving from the given plan line.
func plannedCtr(id, planID string) *exchange.Unit {
	ctr := unit("test-ns", "ctr", id, 1, 1)
	ctr.StateVector = exchange.StateVector{ContainerState: "planned", ExistenceState: "active"}
	ctr.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "test-ns/plan:" + planID}}
	ctr.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-05", Domain: "container-state", From: "-", To: "planned", By: conformance.User("Eng")},
		{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Eng")},
	}
	return ctr
}

// --- plan- transitions -------------------------------------------------

// TestTransitionPlanForwardAndExplicit: draft -> approved through
// --forward and through the explicit <to>; the change-log entry is
// appended and the line re-points in place.
func TestTransitionPlanForwardAndExplicit(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, planUnit("roadmap-v1", 1, "draft", planLog("draft")), project, "repo")

	res, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "plan:roadmap-v1", Forward: true, By: "test-agent",
	})
	if err != nil {
		t.Fatalf("--forward: %v", err)
	}
	if res.Target != "test-ns/plan:roadmap-v1" || res.From != "draft" || res.To != "approved" {
		t.Errorf("result = %+v, want test-ns/plan:roadmap-v1 draft -> approved", res)
	}
	if res.By.Name != "test-agent" || res.ObjectHash == "" {
		t.Errorf("result = %+v, want the by authority and a non-empty hash", res)
	}
	state, logLen := planState(t, r, "roadmap-v1")
	if state != "approved" || logLen != 4 {
		t.Errorf("planning-state = %q with %d entries, want approved with 4", state, logLen)
	}

	// A second draft plan approves through the explicit <to>.
	putUnit(t, r, planUnit("roadmap-v2", 1, "draft", planLog("draft")), project, "repo")
	res, err = Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "plan:roadmap-v2", To: "approved", By: "test-agent",
	})
	if err != nil {
		t.Fatalf("explicit to: %v", err)
	}
	if res.From != "draft" || res.To != "approved" {
		t.Errorf("result = %+v, want draft -> approved", res)
	}
	if state, _ := planState(t, r, "roadmap-v2"); state != "approved" {
		t.Errorf("planning-state = %q, want approved", state)
	}
}

// TestTransitionPlanRefusals: approved -> immutable is refused with the
// lock hint (explicit <to> and --forward alike); --backward and any
// other from/to are refused deterministically.
func TestTransitionPlanRefusals(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, planUnit("roadmap-v1", 1, "approved", planLog("approved")), project, "repo")
	putUnit(t, r, planUnit("roadmap-v2", 1, "draft", planLog("draft")), project, "repo")
	putUnit(t, r, planUnit("locked", 1, "immutable", planLog("immutable")), project, "repo")

	cases := []struct {
		name string
		req  TransitionRequest
		want string
	}{
		{"forward from approved", TransitionRequest{Target: "plan:roadmap-v1", Forward: true}, "planning-state immutable is the container lock"},
		{"explicit immutable from approved", TransitionRequest{Target: "plan:roadmap-v1", To: "immutable"}, "planning-state immutable is the container lock"},
		{"backward", TransitionRequest{Target: "plan:roadmap-v1", Backward: true}, "planning-state is forward-only"},
		{"explicit immutable from draft", TransitionRequest{Target: "plan:roadmap-v2", To: "immutable"}, "is not in the planning-state table"},
		{"no-op approved", TransitionRequest{Target: "plan:roadmap-v1", To: "approved"}, "is not in the planning-state table"},
		{"forward from immutable", TransitionRequest{Target: "plan:locked", Forward: true}, "no forward transition"},
		{"to approved from immutable", TransitionRequest{Target: "plan:locked", To: "approved"}, "is not in the planning-state table"},
	}
	for _, c := range cases {
		c.req.RepoPath = "."
		c.req.By = "test-agent"
		_, err := Authoring.Transition(r, c.req)
		var refusal *TransitionRefusal
		if !errors.As(err, &refusal) {
			t.Errorf("%s: err = %v, want a TransitionRefusal", c.name, err)
			continue
		}
		if !strings.Contains(refusal.Error(), c.want) {
			t.Errorf("%s: refusal = %q, want it to contain %q", c.name, refusal.Error(), c.want)
		}
	}
	// The lock-hint refusal carries the activate-a-container hint.
	lockReq := TransitionRequest{RepoPath: ".", Target: "plan:roadmap-v1", Forward: true, By: "test-agent"}
	_, err := Authoring.Transition(r, lockReq)
	var refusal *TransitionRefusal
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Hint, "activate a container deriving from this plan instead") {
		t.Errorf("lock hint = %v, want the activate-a-container hint", err)
	}
	// Nothing was published by the refusals: the plan is still approved.
	if state, _ := planState(t, r, "roadmap-v1"); state != "approved" {
		t.Errorf("planning-state = %q, want approved (refused runs publish nothing)", state)
	}
}

// --- ctr- transitions --------------------------------------------------

// TestTransitionContainerAllDone: active -> completed publishes when
// every work item is done or canceled; --forward from active completes
// too.
func TestTransitionContainerAllDone(t *testing.T) {
	r, project := transitionRuntime(t)
	containerWorld(t, r, project, map[string]string{"one": "done", "two": "canceled"})

	res, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "ctr:wave-7", To: "completed", By: "test-agent",
	})
	if err != nil {
		t.Fatalf("completed with all done/canceled: %v", err)
	}
	if res.Target != "test-ns/ctr:wave-7" || res.From != "active" || res.To != "completed" {
		t.Errorf("result = %+v, want test-ns/ctr:wave-7 active -> completed", res)
	}
	state, logLen := containerState(t, r, "wave-7")
	if state != "completed" || logLen != 3 {
		t.Errorf("container-state = %q with %d entries, want completed with 3", state, logLen)
	}

	// --forward from a fresh active container completes too.
	ctr2 := unit("test-ns", "ctr", "wave-8", 1, 1)
	ctr2.StateVector = exchange.StateVector{ContainerState: "active", ExistenceState: "active"}
	ctr2.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-05", Domain: "container-state", From: "-", To: "active", By: conformance.User("Eng")},
		{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Eng")},
	}
	putUnit(t, r, ctr2, project, "repo")
	res, err = Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "ctr:wave-8", Forward: true, By: "test-agent",
	})
	if err != nil {
		t.Fatalf("--forward from active: %v", err)
	}
	if res.From != "active" || res.To != "completed" {
		t.Errorf("result = %+v, want active -> completed", res)
	}
	if state, _ := containerState(t, r, "wave-8"); state != "completed" {
		t.Errorf("container-state = %q, want completed", state)
	}
}

// TestTransitionContainerPendingItems: the all-done gate refuses with
// the deterministic sorted listing of the pending items and their
// states; --force does not bypass it; nothing is published.
func TestTransitionContainerPendingItems(t *testing.T) {
	r, project := transitionRuntime(t)
	containerWorld(t, r, project, map[string]string{"one": "todo", "two": "in-progress"})

	_, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "ctr:wave-7", To: "completed", By: "test-agent",
	})
	var refusal *TransitionRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("completed with pending items = %v, want a TransitionRefusal", err)
	}
	if !strings.Contains(refusal.Reason, "2 work item(s) not done or canceled") {
		t.Errorf("reason = %q, want the pending count", refusal.Reason)
	}
	for _, want := range []string{"sto:one (todo)", "sto:two (in-progress)"} {
		if !strings.Contains(refusal.Reason, want) {
			t.Errorf("reason = %q, want the pending item %q", refusal.Reason, want)
		}
	}
	if !strings.Contains(refusal.Hint, "transition the pending work items to done (or canceled) first") {
		t.Errorf("hint = %q, want the transition-first hint", refusal.Hint)
	}
	// --force does NOT bypass the all-done gate.
	_, err = Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "ctr:wave-7", To: "completed", By: "test-agent", Confirmed: true,
	})
	if !errors.As(err, &refusal) {
		t.Errorf("--force with pending items = %v, want the gate refusal", err)
	}
	// Nothing was published: the container stays active.
	if state, _ := containerState(t, r, "wave-7"); state != "active" {
		t.Errorf("container-state = %q, want active (refused runs publish nothing)", state)
	}
}

// TestTransitionContainerForwardAndTerminal: --forward from completed
// refuses (terminal); --backward refuses; an explicit backward request
// (to=active from completed) refuses with the forward-only message.
func TestTransitionContainerForwardAndTerminal(t *testing.T) {
	r, project := transitionRuntime(t)
	containerWorld(t, r, project, map[string]string{"one": "done", "two": "done"})
	if _, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "ctr:wave-7", To: "completed", By: "test-agent",
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	cases := []struct {
		name string
		req  TransitionRequest
		want string
	}{
		{"forward from completed", TransitionRequest{Target: "ctr:wave-7", Forward: true}, "completed is terminal"},
		{"backward", TransitionRequest{Target: "ctr:wave-7", Backward: true}, "container-state is forward-only"},
		{"to active from completed", TransitionRequest{Target: "ctr:wave-7", To: "active"}, "container-state is forward-only"},
		{"no-op completed", TransitionRequest{Target: "ctr:wave-7", To: "completed"}, "container-state is forward-only"},
	}
	for _, c := range cases {
		c.req.RepoPath = "."
		c.req.By = "test-agent"
		_, err := Authoring.Transition(r, c.req)
		var refusal *TransitionRefusal
		if !errors.As(err, &refusal) {
			t.Errorf("%s: err = %v, want a TransitionRefusal", c.name, err)
			continue
		}
		if !strings.Contains(refusal.Error(), c.want) {
			t.Errorf("%s: refusal = %q, want it to contain %q", c.name, refusal.Error(), c.want)
		}
	}
	if state, _ := containerState(t, r, "wave-7"); state != "completed" {
		t.Errorf("container-state = %q, want completed (refused runs publish nothing)", state)
	}
}

// --- ctr- activation (planned -> active, Option B) --------------------

// TestTransitionContainerActivation: planned -> active publishes BOTH
// units in one transaction — the container line (planned -> active
// with the appended change-log entry) and the plan line (approved ->
// immutable with the appended change-log entry) — and the result
// surfaces LockedPlan / LockedPlanHash. The lock authority is the
// transition's by, not the container's initial change-log authority.
func TestTransitionContainerActivation(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, planUnit("roadmap-v1", 1, "approved", planLog("approved")), project, "repo")
	putUnit(t, r, plannedCtr("wave-7", "roadmap-v1"), project, "repo")

	res, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "ctr:wave-7", To: "active", By: "test-agent",
	})
	if err != nil {
		t.Fatalf("activation: %v", err)
	}
	if res.Target != "test-ns/ctr:wave-7" || res.From != "planned" || res.To != "active" {
		t.Errorf("result = %+v, want test-ns/ctr:wave-7 planned -> active", res)
	}
	if res.By.Name != "test-agent" || res.ObjectHash == "" {
		t.Errorf("result = %+v, want the by authority and a non-empty hash", res)
	}
	if res.LockedPlan != "test-ns/plan:roadmap-v1" {
		t.Errorf("LockedPlan = %q, want test-ns/plan:roadmap-v1", res.LockedPlan)
	}
	if res.LockedPlanHash == "" || res.LockedPlanHash == res.ObjectHash {
		t.Errorf("LockedPlanHash = %q (container hash %q), want a distinct non-empty hash", res.LockedPlanHash, res.ObjectHash)
	}
	// The container is active with the appended entry; the entry's by
	// is the transition authority.
	state, logLen := containerState(t, r, "wave-7")
	if state != "active" || logLen != 3 {
		t.Errorf("container-state = %q with %d entries, want active with 3", state, logLen)
	}
	ctrUnits, err := r.ws.Store().UnitsByLine("test-ns", "ctr", "wave-7")
	if err != nil {
		t.Fatal(err)
	}
	ctr := ctrUnits[0]
	for _, cand := range ctrUnits {
		if cand.Identity.InstanceVersion > ctr.Identity.InstanceVersion {
			ctr = cand
		}
	}
	last := ctr.ChangeLog[len(ctr.ChangeLog)-1]
	if last.Domain != conformance.DomainContainerState || last.From != "planned" || last.To != "active" {
		t.Errorf("last container entry = %+v, want container-state planned -> active", last)
	}
	if last.By.Name != "test-agent" {
		t.Errorf("container entry by = %q, want the transition authority test-agent", last.By.Name)
	}
	// The plan is immutable with the appended planning-state entry; the
	// lock by is the transition authority; the line re-points at the
	// locked payload.
	plan, ok, err := r.Knowledge.Object("test-ns/plan:roadmap-v1:1")
	if err != nil || !ok {
		t.Fatalf("plan read-back = %v, %v", ok, err)
	}
	if plan.StateVector.PlanningState != "immutable" {
		t.Errorf("planning-state = %q, want immutable (locked)", plan.StateVector.PlanningState)
	}
	if plan.Digest != res.LockedPlanHash {
		t.Errorf("plan digest = %q, want the returned LockedPlanHash %q", plan.Digest, res.LockedPlanHash)
	}
	last = plan.ChangeLog[len(plan.ChangeLog)-1]
	if last.Domain != conformance.DomainPlanningState || last.From != "approved" || last.To != "immutable" {
		t.Errorf("last plan entry = %+v, want planning-state approved -> immutable", last)
	}
	if last.By.Name != "test-agent" {
		t.Errorf("lock by = %q, want the transition authority test-agent", last.By.Name)
	}
}

// TestTransitionContainerActivationRefusedAnotherActive: the protocol
// §3 gate — activating a planned container while ANOTHER container
// line is active is refused with the deterministic message naming the
// active offender (smallest form) and the complete-it hint; nothing
// publishes.
func TestTransitionContainerActivationRefusedAnotherActive(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, planUnit("roadmap-v1", 1, "approved", planLog("approved")), project, "repo")
	putUnit(t, r, plannedCtr("wave-7", "roadmap-v1"), project, "repo")
	// A second container line, already active (born-active docs
	// authoring stays valid).
	other := plannedCtr("wave-6", "roadmap-v1")
	other.StateVector = exchange.StateVector{ContainerState: "active", ExistenceState: "active"}
	other.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-05", Domain: "container-state", From: "-", To: "active", By: conformance.User("Eng")},
		{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Eng")},
	}
	putUnit(t, r, other, project, "repo")

	_, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "ctr:wave-7", To: "active", By: "test-agent",
	})
	var refusal *TransitionRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("activation with another active container = %v, want a TransitionRefusal", err)
	}
	if !strings.Contains(refusal.Reason, "another container test-ns/ctr:wave-6 is active; activate test-ns/ctr:wave-7 only after it completes") {
		t.Errorf("reason = %q, want the other-active refusal naming the offender", refusal.Reason)
	}
	if !strings.Contains(refusal.Hint, "eka transition ctr:wave-6 completed") {
		t.Errorf("hint = %q, want the complete-the-offender hint", refusal.Hint)
	}
	// Nothing was published: the container stays planned, the plan
	// stays approved.
	if state, _ := containerState(t, r, "wave-7"); state != "planned" {
		t.Errorf("container-state = %q, want planned (refused runs publish nothing)", state)
	}
	if state, _ := planState(t, r, "roadmap-v1"); state != "approved" {
		t.Errorf("planning-state = %q, want approved (refused runs publish nothing)", state)
	}
}

// TestTransitionContainerActivationRefusedDraftPlan: the plan-approval
// gate — activating a container whose depends-on plan is still draft is
// refused with the approve-it-first hint; nothing publishes.
func TestTransitionContainerActivationRefusedDraftPlan(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, planUnit("roadmap-v1", 1, "draft", planLog("draft")), project, "repo")
	putUnit(t, r, plannedCtr("wave-7", "roadmap-v1"), project, "repo")

	_, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "ctr:wave-7", To: "active", By: "test-agent",
	})
	var refusal *TransitionRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("activation against a draft plan = %v, want a TransitionRefusal", err)
	}
	if !strings.Contains(refusal.Reason, "the plan test-ns/plan:roadmap-v1 is not approved (planning-state: draft)") {
		t.Errorf("reason = %q, want the not-approved refusal naming the state", refusal.Reason)
	}
	if !strings.Contains(refusal.Hint, "approve it first: eka transition plan:roadmap-v1 approved") {
		t.Errorf("hint = %q, want the approve-it-first hint", refusal.Hint)
	}
	// Nothing was published: the container stays planned, the plan
	// stays draft.
	if state, _ := containerState(t, r, "wave-7"); state != "planned" {
		t.Errorf("container-state = %q, want planned (refused runs publish nothing)", state)
	}
	if state, _ := planState(t, r, "roadmap-v1"); state != "draft" {
		t.Errorf("planning-state = %q, want draft (untouched)", state)
	}
}

// TestTransitionContainerActivationAlreadyImmutable: an
// already-immutable plan is a valid state — the lock is skipped
// (idempotent: a previous activation locked it) and the container
// activates (--forward) without lock fields; exactly ONE immutable
// transition stays on the plan (no second put).
func TestTransitionContainerActivationAlreadyImmutable(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, planUnit("roadmap-v1", 1, "immutable", planLog("immutable")), project, "repo")
	putUnit(t, r, plannedCtr("wave-7", "roadmap-v1"), project, "repo")

	res, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "ctr:wave-7", Forward: true, By: "test-agent",
	})
	if err != nil {
		t.Fatalf("activation with an immutable plan: %v", err)
	}
	if res.From != "planned" || res.To != "active" {
		t.Errorf("result = %+v, want planned -> active", res)
	}
	if res.LockedPlan != "" || res.LockedPlanHash != "" {
		t.Errorf("result = %+v, want no lock fields (already immutable)", res)
	}
	if state, _ := containerState(t, r, "wave-7"); state != "active" {
		t.Errorf("container-state = %q, want active", state)
	}
	plan, ok, err := r.Knowledge.Object("test-ns/plan:roadmap-v1:1")
	if err != nil || !ok {
		t.Fatalf("plan read-back = %v, %v", ok, err)
	}
	if plan.StateVector.PlanningState != "immutable" {
		t.Errorf("planning-state = %q, want immutable", plan.StateVector.PlanningState)
	}
	var locks int
	for _, e := range plan.ChangeLog {
		if e.Domain == conformance.DomainPlanningState && e.To == "immutable" {
			locks++
		}
	}
	if locks != 1 {
		t.Errorf("immutable change-log entries = %d, want 1 (the lock is idempotent)", locks)
	}
}

// TestTransitionContainerActivationTableRefusals: the explicit table is
// the two steps planned -> active, active -> completed — a direct
// planned -> completed request is refused with the table message (the
// transition API exposes the gates as steps, not skips), a no-op
// planned -> planned and a backward request refuse as forward-only.
func TestTransitionContainerActivationTableRefusals(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, planUnit("roadmap-v1", 1, "approved", planLog("approved")), project, "repo")
	putUnit(t, r, plannedCtr("wave-7", "roadmap-v1"), project, "repo")

	cases := []struct {
		name string
		req  TransitionRequest
		want string
	}{
		{"planned to completed", TransitionRequest{Target: "ctr:wave-7", To: "completed"}, "is not in the container-state table"},
		{"planned no-op", TransitionRequest{Target: "ctr:wave-7", To: "planned"}, "container-state is forward-only"},
		{"backward from planned", TransitionRequest{Target: "ctr:wave-7", Backward: true}, "container-state is forward-only"},
	}
	for _, c := range cases {
		c.req.RepoPath = "."
		c.req.By = "test-agent"
		_, err := Authoring.Transition(r, c.req)
		var refusal *TransitionRefusal
		if !errors.As(err, &refusal) {
			t.Errorf("%s: err = %v, want a TransitionRefusal", c.name, err)
			continue
		}
		if !strings.Contains(refusal.Error(), c.want) {
			t.Errorf("%s: refusal = %q, want it to contain %q", c.name, refusal.Error(), c.want)
		}
	}
	if state, _ := containerState(t, r, "wave-7"); state != "planned" {
		t.Errorf("container-state = %q, want planned (refused runs publish nothing)", state)
	}
}
