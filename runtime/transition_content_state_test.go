package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file tests the content-state branch of the Authoring transition
// pipeline (knowledge-artifact targets): the content-maturity lifecycle
// of the type's variant — the standard variant (draft -> review ->
// approved -> amended), the ADR variant (proposed -> accepted ->
// superseded) and the decision variant (draft -> accepted ->
// superseded). The explicit table is the immediate next step of the
// variant (steps, not skips — the same convention as the plan and
// container branches): skips, reverts, no-ops, --backward and terminal
// states refuse deterministically and publish nothing. The
// adr- -> superseded step is gated on conformance R9 (a replacement
// must reference the ADR via `supersedes`); decision- carries no gate.

// contentUnit builds a knowledge-artifact unit carrying the given
// content-state.
func contentUnit(typ, id string, version int, contentState string, log []exchange.ChangeLogEntry) *exchange.Unit {
	u := unit("test-ns", typ, id, version, version)
	u.StateVector = exchange.StateVector{ContentState: contentState, ExistenceState: "active"}
	u.ChangeLog = log
	return u
}

// contentLog is the initial change-log of a knowledge-artifact unit
// ending in the given content-state.
func contentLog(state string) []exchange.ChangeLogEntry {
	return []exchange.ChangeLogEntry{
		{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Eng")},
		{Date: "2026-08-05", Domain: "content-state", From: "-", To: state, By: conformance.User("Eng")},
	}
}

// contentState reads the current content-state of a line and its
// change-log length.
func contentState(t *testing.T, r *Runtime, typ, id string) (string, int) {
	t.Helper()
	units, err := r.ws.Store().UnitsByLine("test-ns", typ, id)
	if err != nil || len(units) == 0 {
		t.Fatalf("UnitsByLine(%s:%s) = %d units (err %v)", typ, id, len(units), err)
	}
	u := units[0]
	for _, cand := range units {
		if cand.Identity.InstanceVersion > u.Identity.InstanceVersion {
			u = cand
		}
	}
	return u.StateVector.ContentState, len(u.ChangeLog)
}

// TestTransitionContentStateLivingSteps: the standard variant moves
// along its three steps (draft -> review -> approved -> amended)
// through --forward and the explicit <to>; each step appends a
// content-state change-log entry recorded by the transition authority
// and the line re-points in place.
func TestTransitionContentStateLivingSteps(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, contentUnit("spec", "api", 1, "draft", contentLog("draft")), project, "repo")

	// draft -> review through --forward.
	res, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "spec:api", Forward: true, By: "test-agent",
	})
	if err != nil {
		t.Fatalf("draft --forward: %v", err)
	}
	if res.Target != "test-ns/spec:api" || res.From != "draft" || res.To != "review" {
		t.Errorf("result = %+v, want test-ns/spec:api draft -> review", res)
	}
	if res.By.Name != "test-agent" || res.ObjectHash == "" {
		t.Errorf("result = %+v, want the by authority and a non-empty hash", res)
	}
	if state, logLen := contentState(t, r, "spec", "api"); state != "review" || logLen != 3 {
		t.Errorf("content-state = %q with %d entries, want review with 3", state, logLen)
	}

	// review -> approved through the explicit <to>.
	res, err = Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "spec:api", To: "approved", By: "test-agent",
	})
	if err != nil {
		t.Fatalf("review -> approved: %v", err)
	}
	if res.From != "review" || res.To != "approved" {
		t.Errorf("result = %+v, want review -> approved", res)
	}
	if state, logLen := contentState(t, r, "spec", "api"); state != "approved" || logLen != 4 {
		t.Errorf("content-state = %q with %d entries, want approved with 4", state, logLen)
	}

	// approved -> amended through --forward.
	res, err = Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "spec:api", Forward: true, By: "test-agent",
	})
	if err != nil {
		t.Fatalf("approved --forward: %v", err)
	}
	if res.From != "approved" || res.To != "amended" {
		t.Errorf("result = %+v, want approved -> amended", res)
	}
	state, logLen := contentState(t, r, "spec", "api")
	if state != "amended" || logLen != 5 {
		t.Errorf("content-state = %q with %d entries, want amended with 5", state, logLen)
	}
	units, err := r.ws.Store().UnitsByLine("test-ns", "spec", "api")
	if err != nil {
		t.Fatal(err)
	}
	last := units[0].ChangeLog[len(units[0].ChangeLog)-1]
	if last.Domain != conformance.DomainContentState || last.From != "approved" || last.To != "amended" {
		t.Errorf("last entry = %+v, want content-state approved -> amended", last)
	}
	if last.By.Name != "test-agent" {
		t.Errorf("last entry by = %q, want the transition authority test-agent", last.By.Name)
	}
}

// TestTransitionContentStateADRAcceptance: the acceptance example —
// an adr- published at its initial content-state (proposed, the adr
// variant has no "draft") transitions to accepted through the explicit
// <to>; accepted -> superseded publishes when a published successor
// references the ADR via `supersedes`.
func TestTransitionContentStateADRAcceptance(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, contentUnit("adr", "one", 1, "proposed", contentLog("proposed")), project, "repo")

	res, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "adr:one", To: "accepted", By: "test-agent",
	})
	if err != nil {
		t.Fatalf("adr proposed -> accepted: %v", err)
	}
	if res.Target != "test-ns/adr:one" || res.From != "proposed" || res.To != "accepted" {
		t.Errorf("result = %+v, want test-ns/adr:one proposed -> accepted", res)
	}
	if state, logLen := contentState(t, r, "adr", "one"); state != "accepted" || logLen != 3 {
		t.Errorf("content-state = %q with %d entries, want accepted with 3", state, logLen)
	}

	// accepted -> superseded, gated on a published successor
	// superseding the target.
	succ := contentUnit("adr", "two", 1, "proposed", contentLog("proposed"))
	succ.Relationships = []exchange.Relationship{{Type: "supersedes", Target: "test-ns/adr:one"}}
	putUnit(t, r, succ, project, "repo")
	res, err = Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "adr:one", To: "superseded", By: "test-agent",
	})
	if err != nil {
		t.Fatalf("adr accepted -> superseded: %v", err)
	}
	if res.From != "accepted" || res.To != "superseded" {
		t.Errorf("result = %+v, want accepted -> superseded", res)
	}
	if state, _ := contentState(t, r, "adr", "one"); state != "superseded" {
		t.Errorf("content-state = %q, want superseded", state)
	}
}

// TestTransitionContentStateADRSupersedeGate: accepted -> superseded
// is refused (gate R9) when no published replacement references the ADR
// via `supersedes`; the refusal names the target and hints at the
// successor; nothing publishes.
func TestTransitionContentStateADRSupersedeGate(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, contentUnit("adr", "one", 1, "accepted", contentLog("accepted")), project, "repo")

	_, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "adr:one", To: "superseded", By: "test-agent",
	})
	var refusal *TransitionRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("superseded without a replacement = %v, want a TransitionRefusal", err)
	}
	if !strings.Contains(refusal.Reason, "transition gate R9") || !strings.Contains(refusal.Reason, "test-ns/adr:one") {
		t.Errorf("reason = %q, want the R9 gate refusal naming the target", refusal.Reason)
	}
	if !strings.Contains(refusal.Hint, "create the successor ADR with --supersedes") {
		t.Errorf("hint = %q, want the create-the-successor hint", refusal.Hint)
	}
	if state, _ := contentState(t, r, "adr", "one"); state != "accepted" {
		t.Errorf("content-state = %q, want accepted (refused runs publish nothing)", state)
	}
}

// TestTransitionContentStateDecision: the decision variant moves
// draft -> accepted -> superseded, and decision- supersession carries
// no replacement gate (R9 applies to adr- only).
func TestTransitionContentStateDecision(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, contentUnit("dec", "reverse-proxy", 1, "draft", contentLog("draft")), project, "repo")

	res, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "dec:reverse-proxy", To: "accepted", By: "test-agent",
	})
	if err != nil {
		t.Fatalf("dec draft -> accepted: %v", err)
	}
	if res.From != "draft" || res.To != "accepted" {
		t.Errorf("result = %+v, want draft -> accepted", res)
	}
	res, err = Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "dec:reverse-proxy", To: "superseded", By: "test-agent",
	})
	if err != nil {
		t.Fatalf("dec accepted -> superseded without a replacement: %v", err)
	}
	if res.From != "accepted" || res.To != "superseded" {
		t.Errorf("result = %+v, want accepted -> superseded", res)
	}
	if state, _ := contentState(t, r, "dec", "reverse-proxy"); state != "superseded" {
		t.Errorf("content-state = %q, want superseded", state)
	}
}

// TestTransitionContentStateRefusals: the explicit table is the
// immediate next step of the variant — a skip (draft -> approved on the
// standard variant, proposed -> superseded on the adr variant), a
// revert, a no-op and a --backward request refuse deterministically;
// terminal states (amended / superseded) refuse everything. Refused
// runs publish nothing.
func TestTransitionContentStateRefusals(t *testing.T) {
	r, project := transitionRuntime(t)
	putUnit(t, r, contentUnit("spec", "api", 1, "draft", contentLog("draft")), project, "repo")
	putUnit(t, r, contentUnit("spec", "approved-one", 1, "review", contentLog("review")), project, "repo")
	putUnit(t, r, contentUnit("spec", "amended-one", 1, "amended", contentLog("amended")), project, "repo")
	putUnit(t, r, contentUnit("adr", "skip-adr", 1, "proposed", contentLog("proposed")), project, "repo")
	putUnit(t, r, contentUnit("adr", "accepted-adr", 1, "accepted", contentLog("accepted")), project, "repo")

	cases := []struct {
		name string
		req  TransitionRequest
		want string
	}{
		{"living skip draft to approved", TransitionRequest{Target: "spec:api", To: "approved"}, "is not in the content-state table"},
		{"living no-op draft", TransitionRequest{Target: "spec:api", To: "draft"}, "content-state is forward-only"},
		{"living revert review to draft", TransitionRequest{Target: "spec:approved-one", To: "draft"}, "content-state is forward-only"},
		{"living backward", TransitionRequest{Target: "spec:approved-one", Backward: true}, "content-state is forward-only"},
		{"living terminal explicit", TransitionRequest{Target: "spec:amended-one", To: "review"}, "amended is terminal"},
		{"living terminal forward", TransitionRequest{Target: "spec:amended-one", Forward: true}, "no forward transition"},
		{"adr skip proposed to superseded", TransitionRequest{Target: "adr:skip-adr", To: "superseded"}, "is not in the content-state table"},
		{"adr terminal explicit", TransitionRequest{Target: "adr:accepted-adr", To: "accepted"}, "content-state is forward-only"},
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
	// The legal-transitions hint names the next step of the variant.
	_, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "spec:api", To: "amended", By: "test-agent",
	})
	var refusal *TransitionRefusal
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Hint, `legal transitions from "draft": review`) {
		t.Errorf("hint = %v, want the next-step hint for the standard variant", err)
	}
	// Nothing was published by the refusals.
	if state, _ := contentState(t, r, "spec", "api"); state != "draft" {
		t.Errorf("content-state = %q, want draft (refused runs publish nothing)", state)
	}
	if state, _ := contentState(t, r, "spec", "approved-one"); state != "review" {
		t.Errorf("content-state = %q, want review (refused runs publish nothing)", state)
	}
	if state, _ := contentState(t, r, "spec", "amended-one"); state != "amended" {
		t.Errorf("content-state = %q, want amended (refused runs publish nothing)", state)
	}
	if state, _ := contentState(t, r, "adr", "skip-adr"); state != "proposed" {
		t.Errorf("content-state = %q, want proposed (refused runs publish nothing)", state)
	}
}

// TestTransitionContentStateNonKnowledgeRefusals: a knowledge-artifact
// target whose current unit carries no content-state refuses with the
// missing-state message; a type that owns no content-state (a
// projection like tkt-) stays not transitionable.
func TestTransitionContentStateNonKnowledgeRefusals(t *testing.T) {
	r, project := transitionRuntime(t)
	// An adr unit whose state vector is missing content-state.
	u := unit("test-ns", "adr", "bare", 1, 1)
	u.StateVector = exchange.StateVector{ExistenceState: "active"}
	u.ChangeLog = contentLog("")
	putUnit(t, r, u, project, "repo")
	putUnit(t, r, contentUnit("tkt", "t1", 1, "draft", contentLog("draft")), project, "repo")

	_, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "adr:bare", To: "accepted", By: "test-agent",
	})
	var refusal *TransitionRefusal
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Reason, "carries no content-state") {
		t.Errorf("missing content-state = %v, want the carries-no-content-state refusal", err)
	}
	_, err = Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "tkt:t1", To: "review", By: "test-agent",
	})
	if err == nil || !strings.Contains(err.Error(), "not transitionable") {
		t.Errorf("tkt target = %v, want the not-transitionable usage error", err)
	}
}
