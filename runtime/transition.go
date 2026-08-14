package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
)

// This file implements the `eka transition` side of the Authoring API
// (ADR-019 D2, revised per implementation feedback): the structured,
// non-interactive way to move a work item along the D1 transition
// table.
//
// Revised model (documented deviation): transitions operate on the
// WORKSPACE, never on the repository docs tree — the docs tree is the
// legacy backward-compatibility authoring path, not the target of
// current EKA authoring. The work item is resolved as a canonical unit
// in the workspace store (run 'eka sync' first), the transition is
// validated against the D1 table and the R13 gates (notes = published
// cmt- units + EKA_HOME/drafts cmt- drafts discussing the target),
// then the transition is PUBLISHED: the line's reference moves to a
// new immutable payload (same canonical form, new state + appended
// change-log entry) through store.PutUnit — the previous payload stays
// in the content-addressed archive with the prev_hash lineage edge, so
// the ADR-011 chain of immutable units is preserved while every line
// resolution (get/view/board) keeps showing the current state.
//
// `by` is an audit record, not an access control: the engine has no
// authentication — authority belongs to the application layer.

// TransitionRequest describes one requested execution-state transition.
type TransitionRequest struct {
	// RepoPath is the directory the repository is addressed from (the
	// walk-up locates the eka.yaml repository root — ADR-018).
	RepoPath string
	// Target is the work item line: "<ns>/<type>:<id>" (qualified —
	// must equal the repository namespace) or "<type>:<id>"
	// (unqualified — the repository namespace applies).
	Target string
	// To is the explicit destination execution-state value (D1 table).
	// Exactly one of To / Forward / Backward must be set.
	To string
	// Forward requests the next step of the D1 table from the current
	// state (planned->todo->...->done; canceled->todo).
	Forward bool
	// Backward requests the one-step pull-back from the current state
	// (in-review->in-progress, in-progress->todo).
	Backward bool
	// By is the resolved change-log authority name (non-empty;
	// resolved by BySource — the engine never falls back to a default
	// authority).
	By string
	// ByKind is the authority identity kind (user | agent | worker; ""
	// = user — RFC author identity).
	ByKind string
	// Confirmed pre-authorizes the active-container confirmation: a
	// work item not registered in the current active container refuses
	// until the caller confirms (the CLI renders the interactive
	// prompt, or maps --force to true). The transition is NEVER
	// published before this confirmation — a refused or cancelled run
	// leaves the store untouched.
	Confirmed bool
}

// TransitionResult is the deterministic outcome of one transition.
type TransitionResult struct {
	// Target is the canonical line identity of the work item
	// ("<namespace>/<type>:<id>").
	Target string
	// From and To are the execution-state values of the transition.
	From, To string
	// By is the change-log authority identity.
	By conformance.AuthorIdentity
	// ObjectHash is the content-derived digest of the new payload the
	// line's reference now points at.
	ObjectHash string
	// LockedPlan is the canonical line form of the plan a container
	// (ctr-) ACTIVATION locked atomically with the activation
	// (protocol §4): planning-state -> immutable. "" when the
	// transition performed no lock (a non-container transition, a
	// completion, or an activation whose plan was already immutable).
	LockedPlan string
	// LockedPlanHash is the object hash of the locked plan payload.
	LockedPlanHash string
	// Warning is set when the work item is NOT registered in the
	// current active container (the deterministic warning banner and
	// the interactive confirmation gate of the CLI); "" when it is.
	Warning string
}

// TransitionRefusal is a deterministic transition refusal carrying the
// user-facing reason and hint (exit 1 class: illegal transition, gate
// unmet, repository/workspace-state refusal, unconfirmed
// active-container membership).
type TransitionRefusal struct {
	Reason string
	Hint   string
	// Warning is the deterministic active-container banner rendered by
	// the CLI ("" when the refusal is not membership-related).
	Warning string
	// Confirmation reports that the refusal is the active-container
	// confirmation gate: the caller may prompt interactively and retry
	// with Confirmed=true. The transition was NOT published.
	Confirmation bool
}

// Error renders the deterministic refusal message.
func (e *TransitionRefusal) Error() string {
	return fmt.Sprintf("transition refused: %s; %s", e.Reason, e.Hint)
}

// BySource resolves the change-log authority identity: the --by flag
// name plus the --by-kind (user | agent | worker; "" = user). Without
// --by the authority falls back to `git config user.name` (a user).
// The returned identity is never empty-named (an unresolved source is
// an error).
func BySource(flagValue, kindValue, repoDir string) (conformance.AuthorIdentity, error) {
	kind := strings.TrimSpace(kindValue)
	if kind == "" {
		kind = conformance.KindUser
	}
	if !conformance.IsAuthorKind(kind) {
		return conformance.AuthorIdentity{}, fmt.Errorf("unknown author kind %q (allowed: %s)", kindValue, strings.Join(conformance.AuthorKinds, ", "))
	}
	name := strings.TrimSpace(flagValue)
	if name != "" {
		return conformance.AuthorIdentity{Kind: kind, Name: name}, nil
	}
	if kind != conformance.KindUser {
		return conformance.AuthorIdentity{}, fmt.Errorf("an %s authority requires --by <name>", kind)
	}
	cmd := exec.Command("git", "config", "user.name")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return conformance.AuthorIdentity{}, fmt.Errorf("cannot resolve the change-log authority: `git config user.name` failed in %s: %v; pass --by <name>", repoDir, err)
	}
	name = strings.TrimSpace(string(out))
	if name == "" {
		return conformance.AuthorIdentity{}, fmt.Errorf("cannot resolve the change-log authority: `git config user.name` returned an empty name; pass --by <name>")
	}
	return conformance.AuthorIdentity{Kind: conformance.KindUser, Name: name}, nil
}

// Transition performs one legal transition and publishes it to the
// workspace store. Pipeline (deterministic):
//
//  1. resolve the repository context (eka.yaml walk-up; ADR-018) and
//     the registered workspace project (unregistered -> refusal);
//  2. resolve the target work item line in the workspace store (the
//     highest instance is the current state; not in the store ->
//     refusal with the sync hint);
//  3. derive/validate the destination (explicit `to`, --forward or
//     --backward) against the D1 table;
//  4. validate the R13 gates early: in-review requires a resolved
//     implementation note, done requires every note resolved — notes
//     are the published cmt- units of the project and the cmt- drafts
//     under EKA_HOME/drafts that discuss the target;
//  5. when the work item is not registered in the current active
//     container, the result carries the deterministic warning (the CLI
//     renders the banner + interactive confirmation);
//  6. publish: store.PutUnit with the same canonical form, the new
//     execution state and the appended change-log entry; the previous
//     payload stays in the content-addressed archive (prev_hash
//     lineage).
func (AuthoringService) Transition(rt *Runtime, req TransitionRequest) (*TransitionResult, error) {
	if strings.TrimSpace(req.By) == "" {
		return nil, &TransitionRefusal{
			Reason: "the change-log authority (by) is required",
			Hint:   "pass --by <name> or let it resolve from `git config user.name`",
		}
	}
	byIdentity := conformance.AuthorIdentity{Kind: req.ByKind, Name: req.By}
	if byIdentity.Kind == "" {
		byIdentity.Kind = conformance.KindUser
	}
	if !conformance.IsAuthorKind(byIdentity.Kind) {
		return nil, fmt.Errorf("transition: unknown author kind %q (allowed: %s)", req.ByKind, strings.Join(conformance.AuthorKinds, ", ")) // Exit 2: usage.
	}
	if req.To != "" && (req.Forward || req.Backward) {
		return nil, fmt.Errorf("transition: pass either an explicit <to> or --forward/--backward, not both") // Exit 2: usage.
	}
	if req.Forward && req.Backward {
		return nil, fmt.Errorf("transition: --forward and --backward are mutually exclusive") // Exit 2: usage.
	}
	root, meta, err := resolveRepoContext(req.RepoPath)
	if err != nil {
		var ctx repoContext
		if errors.As(err, &ctx) {
			return nil, &TransitionRefusal{Reason: ctx.Error(), Hint: "run 'eka init' first"}
		}
		return nil, err
	}
	ref, err := conformance.ParseReference(req.Target, "", "")
	if err != nil {
		return nil, fmt.Errorf("transition: invalid target %q: %w", req.Target, err) // Exit 2: usage.
	}
	if ref.HasVersion {
		return nil, fmt.Errorf("transition: %s is a canonical published form; transition addresses the work item line", req.Target) // Exit 2.
	}
	if !conformance.IsWorkItemType(ref.Type) && ref.Type != "plan" && ref.Type != "ctr" {
		return nil, fmt.Errorf("transition: %s is not transitionable (work items sto/ts/bug/td/ch/spk, plans plan-, containers ctr-)", req.Target) // Exit 2.
	}
	if ref.Namespace != "" && ref.Namespace != meta.Namespace {
		return nil, fmt.Errorf("transition: target namespace %s differs from the repository namespace %s; cross-platform access is read-only",
			ref.Namespace, meta.Namespace) // Exit 2: usage.
	}
	if ref.Namespace == "" {
		ref.Namespace = meta.Namespace
	}

	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	st, err := rt.requireStore()
	if err != nil {
		return nil, err
	}
	repo, found, err := ws.FindRepo(root)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &TransitionRefusal{
			Reason: "the repository is not registered in the workspace",
			Hint:   "run 'eka sync' once to register the repository identity from eka.yaml",
		}
	}
	project := repo.ProjectID

	// The transitionable types and their state domains: work items own
	// execution-state (the D1 table), plans own planning-state (draft
	// -> approved; immutable is the container lock), containers own
	// container-state (active -> completed, gated on the all-done
	// membership rule).
	typeWord := "work item"
	stateDomain := conformance.DomainExecutionState
	switch ref.Type {
	case "plan":
		typeWord = "plan"
		stateDomain = conformance.DomainPlanningState
	case "ctr":
		typeWord = "container"
		stateDomain = conformance.DomainContainerState
	}

	// The current state: the highest instance of the line (the line is
	// the object; transitions re-point the reference in place).
	line, err := st.UnitsByLine(ref.Namespace, ref.Type, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("transition: %w", err)
	}
	var current *exchange.Unit
	for _, u := range line {
		if current == nil || u.Identity.InstanceVersion > current.Identity.InstanceVersion {
			current = u
		}
	}
	if current == nil {
		return nil, &TransitionRefusal{
			Reason: fmt.Sprintf("%s %s/%s:%s was not found in the workspace store", typeWord, ref.Namespace, ref.Type, ref.ID),
			Hint:   "run 'eka sync' first (the docs tree is legacy authoring; transitions operate on the workspace)",
		}
	}
	var from string
	switch stateDomain {
	case conformance.DomainExecutionState:
		from = current.StateVector.ExecutionState
	case conformance.DomainPlanningState:
		from = current.StateVector.PlanningState
	case conformance.DomainContainerState:
		from = current.StateVector.ContainerState
	}
	if from == "" {
		return nil, &TransitionRefusal{
			Reason: fmt.Sprintf("%s %s/%s:%s carries no %s", typeWord, ref.Namespace, ref.Type, ref.ID, stateDomain),
			Hint:   "run 'eka validate' to see the authoring violations",
		}
	}

	// The planning-state / container-state branches (plan- and ctr-
	// targets). Work items continue with the D1 pipeline below.
	if ref.Type == "plan" {
		return transitionPlanState(st, project, repo.Name, ref, current, from, req, byIdentity)
	}
	if ref.Type == "ctr" {
		return transitionContainerState(st, project, repo.Name, ref, current, from, req, byIdentity)
	}

	// Destination: explicit <to>, or the derived --forward/--backward
	// step of the D1 table.
	to := req.To
	if req.Forward || req.Backward {
		to = transitionStep(from, req.Forward)
		if to == "" {
			direction := "forward"
			if req.Backward {
				direction = "backward"
			}
			return nil, &TransitionRefusal{
				Reason: fmt.Sprintf("there is no %s transition from %q in the D1 table", direction, from),
				Hint:   legalTransitionsHint(from),
			}
		}
	}
	if !conformance.IsLegalExecutionTransition(from, to) {
		return nil, &TransitionRefusal{
			Reason: fmt.Sprintf("transition %s -> %s is not in the D1 table", from, to),
			Hint:   legalTransitionsHint(from),
		}
	}

	// Early R13 gate check: the same rules `eka sync` enforces for the
	// legacy docs tree, evaluated over the workspace notes (published
	// units + drafts).
	notes, err := collectGateNotes(ws, st, project, ref)
	if err != nil {
		return nil, fmt.Errorf("transition: %w", err)
	}
	if violations := gateViolations(notes, to); len(violations) > 0 {
		return nil, &TransitionRefusal{
			Reason: violations[0],
			Hint:   "create the required note ('eka note --role implementation', then set note-state resolved) or choose another transition",
		}
	}

	// Active-container membership: a work item not registered in the
	// current active container refuses until the caller confirms
	// (Confirmed) — the confirmation is a PRE-FLIGHT gate, so a
	// refused or cancelled run never publishes anything.
	warning := ""
	if !registeredInActiveContainer(st, project, ref) {
		warning = fmt.Sprintf("%s is not registered in the current active container (no ticket deriving from an active ctr- references it)",
			ref.Namespace+"/"+ref.Type+":"+ref.ID)
		if !req.Confirmed {
			return nil, &TransitionRefusal{
				Reason:       ref.Namespace + "/" + ref.Type + ":" + ref.ID + " is not registered in the current active container",
				Hint:         "confirm in a terminal or pass --force to proceed",
				Warning:      warning,
				Confirmation: true,
			}
		}
	}

	// Publish the transition in place: the same canonical form, the new
	// execution state and the appended change-log entry.
	today := time.Now().Format("2006-01-02")
	next := *current // shallow copy; the mutable slices below are rebuilt.
	next.StateVector.ExecutionState = to
	next.Updated = today
	next.ChangeLog = append(append([]exchange.ChangeLogEntry{}, current.ChangeLog...), exchange.ChangeLogEntry{
		Date: today, Domain: conformance.DomainExecutionState, From: from, To: to, By: byIdentity,
	})
	unitJSON, err := exchange.MarshalUnit(&next)
	if err != nil {
		return nil, fmt.Errorf("transition: cannot serialize %s: %w", next.CanonicalIdentityForm, err)
	}
	hash, _, err := st.PutUnit(unitJSON, next.ContentPayload, store.Ref{
		Form:            next.CanonicalIdentityForm,
		ProjectID:       project,
		SourceRepo:      repo.Name,
		Namespace:       next.Identity.Namespace,
		Type:            next.Identity.Type,
		ID:              next.Identity.ID,
		InstanceVersion: next.Identity.InstanceVersion,
		Revision:        next.Revision,
		Dimension:       next.Classification.Dimension,
		Domain:          next.Classification.Domain,
		Phase:           next.Phase,
		UpdatedAt:       today,
	})
	if err != nil {
		return nil, fmt.Errorf("transition: cannot publish %s: %w", next.CanonicalIdentityForm, err)
	}
	return &TransitionResult{
		Target:     ref.Namespace + "/" + ref.Type + ":" + ref.ID,
		From:       from,
		To:         to,
		By:         byIdentity,
		ObjectHash: hash,
		Warning:    warning,
	}, nil
}

// transitionStep derives the --forward/--backward destination of the D1
// table from the current state: forward = the next sequential step
// (planned->todo->in-progress->in-review->done; canceled->todo),
// backward = the one-step pull-back (in-review->in-progress,
// in-progress->todo). "" = no step in that direction (done has no
// forward step; planned/todo have no backward step; done/canceled
// pull back only through explicit transitions).
func transitionStep(from string, forward bool) string {
	if forward {
		switch from {
		case "planned":
			return "todo"
		case "todo":
			return "in-progress"
		case "in-progress":
			return "in-review"
		case "in-review":
			return "done"
		case "canceled":
			return "todo"
		}
		return ""
	}
	switch from {
	case "in-review":
		return "in-progress"
	case "in-progress":
		return "todo"
	}
	return ""
}

// legalTransitionsHint renders the D1 destinations of a state for the
// refusal hint ("legal transitions from <from>: ...").
func legalTransitionsHint(from string) string {
	var to []string
	for _, c := range []string{"todo", "in-progress", "in-review", "done", "canceled"} {
		if conformance.IsLegalExecutionTransition(from, c) {
			to = append(to, c)
		}
	}
	if len(to) == 0 {
		return fmt.Sprintf("no legal transition from %q (done is terminal; canceled is the only exit)", from)
	}
	return fmt.Sprintf("legal transitions from %q: %s", from, strings.Join(to, ", "))
}

// transitionPlanState performs the planning-state branch of the
// transition pipeline (plan- targets): the forward-only table is
// draft -> approved. Planning-state immutable is the container lock
// (protocol §4): it happens atomically with the container birth and
// cannot be requested directly — approved -> immutable is refused with
// the lock hint (explicit <to> and --forward alike). No note gates, no
// active-container confirmation (plan and container transitions are
// work-item-only gates).
func transitionPlanState(st *store.Store, project, sourceRepo string, ref conformance.Reference, current *exchange.Unit, from string, req TransitionRequest, byIdentity conformance.AuthorIdentity) (*TransitionResult, error) {
	to := req.To
	if req.Forward || req.Backward {
		if req.Backward {
			return nil, &TransitionRefusal{
				Reason: "planning-state is forward-only",
				Hint:   legalPlanningTransitionsHint(from),
			}
		}
		switch from {
		case "draft":
			to = "approved"
		case "approved":
			return nil, planLockRefusal()
		default: // immutable: the lock is terminal, no forward step.
			return nil, &TransitionRefusal{
				Reason: fmt.Sprintf("there is no forward transition from %q in the planning-state table", from),
				Hint:   "planning-state immutable is the container lock; it is terminal",
			}
		}
	}
	// Explicit <to>: draft -> approved only; approved -> immutable is
	// refused with the lock hint even when requested explicitly.
	if from == "approved" && to == "immutable" {
		return nil, planLockRefusal()
	}
	if !(from == "draft" && to == "approved") {
		return nil, &TransitionRefusal{
			Reason: fmt.Sprintf("transition %s -> %s is not in the planning-state table", from, to),
			Hint:   legalPlanningTransitionsHint(from),
		}
	}
	return publishStateTransition(st, project, sourceRepo, current, from, to, conformance.DomainPlanningState, byIdentity)
}

// transitionContainerState performs the container-state branch of the
// transition pipeline (ctr- targets) — the three-state table
// planned -> active -> completed (Option B):
//
//   - ACTIVATION (planned -> active), gated on the exactly-one-active
//     rule (protocol §3: no OTHER container line may be active) and on
//     the plan-approval rule (the depends-on plan must be approved);
//     the activation LOCKS the plan (planning-state -> immutable,
//     protocol §4 lock-atomic-with-generation) atomically with the
//     activation — one store transaction, so the active container and
//     its locked plan land together or not at all. An already-immutable
//     plan is the idempotent skip (no lock performed this run).
//   - COMPLETION (active -> completed), gated on the container's work
//     items all being done or canceled (the membership rule of the
//     execution projection: the tkt- units whose derives-from resolves
//     to the container line, then the work items those tickets
//     register; the highest instance per line is the current state).
//
// The explicit table is the two steps only: planned -> completed
// directly is refused with the table message even though the
// forward-only rule tolerates skipping (conformance); the transition
// API exposes the gates as steps, not skips. --force does NOT bypass
// either gate — the gates are the point; --force stays the work-item
// confirmation pre-authorization only. The container is its own
// authority: no active-container confirmation, no note gates. The
// lock authority is the transition's `by` (req.By -> byIdentity), the
// acting authority — consistent with publishStateTransition.
func transitionContainerState(st *store.Store, project, sourceRepo string, ref conformance.Reference, current *exchange.Unit, from string, req TransitionRequest, byIdentity conformance.AuthorIdentity) (*TransitionResult, error) {
	to := req.To
	if req.Forward || req.Backward {
		if req.Backward {
			return nil, &TransitionRefusal{
				Reason: "container-state is forward-only",
				Hint:   legalContainerTransitionsHint(from),
			}
		}
		switch from {
		case "planned":
			to = "active"
		case "active":
			to = "completed"
		case "completed":
			return nil, &TransitionRefusal{
				Reason: "completed is terminal",
				Hint:   legalContainerTransitionsHint(from),
			}
		}
	}
	if !((from == "planned" && to == "active") || (from == "active" && to == "completed")) {
		hint := legalContainerTransitionsHint(from)
		values := conformance.DomainValues(conformance.DomainContainerState, "ctr")
		if valueIndex(values, to) <= valueIndex(values, from) {
			// A backward or no-op request: container-state is forward-only.
			return nil, &TransitionRefusal{Reason: "container-state is forward-only", Hint: hint}
		}
		return nil, &TransitionRefusal{
			Reason: fmt.Sprintf("transition %s -> %s is not in the container-state table", from, to),
			Hint:   hint,
		}
	}

	// ACTIVATION (planned -> active): the protocol §3 gate and the
	// plan-approval gate; the plan lock lands atomically with the
	// activation (store.PutUnits on the lockPlanPuts batch).
	if from == "planned" {
		// Gate (protocol §3): exactly one container may be active at a
		// time — a planned container activates only when no OTHER
		// container line is active. The smallest canonical form of the
		// active offender is named (deterministic). A store read
		// failure refuses closed: an activation whose project cannot be
		// scanned never proceeds.
		other, ok := otherActiveContainer(st, project, ref)
		if !ok {
			return nil, &TransitionRefusal{
				Reason: "cannot read the project's containers: the exactly-one-active gate (protocol §3) could not be evaluated",
				Hint:   "run 'eka sync' first and retry the activation",
			}
		}
		if other != "" {
			return nil, &TransitionRefusal{
				Reason: fmt.Sprintf("another container %s is active; activate %s only after it completes",
					other, ref.Namespace+"/"+ref.Type+":"+ref.ID),
				Hint: "eka transition ctr:" + containerBareID(other) + " completed",
			}
		}

		today := time.Now().Format("2006-01-02")
		next := *current // shallow copy; the mutable slices below are rebuilt.
		next.StateVector.ContainerState = "active"
		next.Updated = today
		next.ChangeLog = append(append([]exchange.ChangeLogEntry{}, current.ChangeLog...), exchange.ChangeLogEntry{
			Date: today, Domain: conformance.DomainContainerState, From: from, To: "active", By: byIdentity,
		})
		// The plan-approval gate + the lock: refuses when the plan is
		// not approved (the approve-it-first message), skips when it is
		// already immutable.
		puts, info, err := lockPlanPuts(st, project, sourceRepo, &next, byIdentity)
		if err != nil {
			return nil, err
		}
		hashes, err := st.PutUnits(puts)
		if err != nil {
			return nil, fmt.Errorf("transition: cannot publish %s: %w", next.CanonicalIdentityForm, err)
		}
		res := &TransitionResult{
			Target:     ref.Namespace + "/" + ref.Type + ":" + ref.ID,
			From:       from,
			To:         to,
			By:         byIdentity,
			ObjectHash: hashes[0],
		}
		if info.Locked {
			// The locked plan's object hash: the batch's second hash
			// (put 2 — the locked plan payload), in input order.
			info.Hash = hashes[1]
			res.LockedPlan = info.Plan
			res.LockedPlanHash = info.Hash
		}
		return res, nil
	}

	// COMPLETION (active -> completed): the all-done gate — every work
	// item the container registers must be done or canceled
	// (deterministic sorted listing of the pending items with their
	// states). No lock involved.
	items := containerWorkItems(st, project, ref)
	var pending []string
	for _, item := range items {
		state := item.StateVector.ExecutionState
		if state != "done" && state != "canceled" {
			pending = append(pending, item.Identity.Type+":"+item.Identity.ID+" ("+state+")")
		}
	}
	if len(pending) > 0 {
		sort.Strings(pending)
		return nil, &TransitionRefusal{
			Reason: fmt.Sprintf("container %s cannot complete: %d work item(s) not done or canceled: %s",
				ref.Namespace+"/"+ref.Type+":"+ref.ID, len(pending), strings.Join(pending, ", ")),
			Hint: "transition the pending work items to done (or canceled) first",
		}
	}
	return publishStateTransition(st, project, sourceRepo, current, from, to, conformance.DomainContainerState, byIdentity)
}

// otherActiveContainer returns the canonical line form of the smallest
// OTHER active container line of the project (highest instance per
// line — the byLine pattern of containerWorkItems; the target's own
// line never counts against itself), or "" when no other container is
// active. ok=false when the store cannot be read — the conservative
// answer: the exactly-one-active gate fails closed on unreadable
// projects.
func otherActiveContainer(st *store.Store, project string, target conformance.Reference) (other string, ok bool) {
	units, err := st.UnitsByProject(project)
	if err != nil {
		return "", false
	}
	byLine := map[string]*exchange.Unit{}
	for _, u := range units {
		if u.Identity.Type != "ctr" {
			continue
		}
		key := u.Identity.Namespace + "/" + u.Identity.Type + ":" + u.Identity.ID
		if cur, exists := byLine[key]; !exists || u.Identity.InstanceVersion > cur.Identity.InstanceVersion {
			byLine[key] = u
		}
	}
	targetLine := target.Namespace + "/" + target.Type + ":" + target.ID
	var active []string
	for line, u := range byLine {
		if line != targetLine && u.StateVector.ContainerState == "active" {
			active = append(active, line)
		}
	}
	if len(active) == 0 {
		return "", true
	}
	sort.Strings(active)
	return active[0], true
}

// containerBareID extracts the bare id of a canonical line form
// ("<ns>/ctr:<id>" -> "<id>") for the deterministic completion hint.
func containerBareID(line string) string {
	if i := strings.LastIndex(line, ":"); i >= 0 {
		return line[i+1:]
	}
	return line
}

// planLockRefusal is the deterministic refusal of a direct transition
// to planning-state immutable: immutable is the container lock — it
// happens atomically with the container ACTIVATION (protocol §4,
// Option B) and cannot be requested directly.
func planLockRefusal() *TransitionRefusal {
	return &TransitionRefusal{
		Reason: "planning-state immutable is the container lock; it happens atomically with the container activation (eka transition ctr:<id> active)",
		Hint:   "activate a container deriving from this plan instead",
	}
}

// legalPlanningTransitionsHint renders the planning-state destinations
// of a state for the refusal hint (the forward-only table draft ->
// approved; immutable is the container lock).
func legalPlanningTransitionsHint(from string) string {
	switch from {
	case "draft":
		return `legal transitions from "draft": approved`
	case "approved":
		return planLockRefusal().Reason
	}
	return `no legal transition from "immutable" (immutable is the container lock; it is terminal)`
}

// legalContainerTransitionsHint renders the container-state
// destinations of a state for the refusal hint (the two-step table
// planned -> active -> completed; completed is terminal).
func legalContainerTransitionsHint(from string) string {
	switch from {
	case "planned":
		return `legal transitions from "planned": active`
	case "active":
		return `legal transitions from "active": completed`
	}
	return `no legal transition from "completed" (completed is terminal)`
}

// valueIndex returns the position of v in values, or -1.
func valueIndex(values []string, v string) int {
	for i, x := range values {
		if x == v {
			return i
		}
	}
	return -1
}

// publishStateTransition publishes one transitioned state in place
// (the plan/container branches): the same canonical form, the new
// state value and the appended change-log entry; the previous payload
// stays in the content-addressed archive (prev_hash lineage). Returns
// the new object hash.
func publishStateTransition(st *store.Store, project, sourceRepo string, current *exchange.Unit, from, to, domain string, byIdentity conformance.AuthorIdentity) (*TransitionResult, error) {
	today := time.Now().Format("2006-01-02")
	next := *current // shallow copy; the mutable slices below are rebuilt.
	switch domain {
	case conformance.DomainPlanningState:
		next.StateVector.PlanningState = to
	case conformance.DomainContainerState:
		next.StateVector.ContainerState = to
	}
	next.Updated = today
	next.ChangeLog = append(append([]exchange.ChangeLogEntry{}, current.ChangeLog...), exchange.ChangeLogEntry{
		Date: today, Domain: domain, From: from, To: to, By: byIdentity,
	})
	unitJSON, err := exchange.MarshalUnit(&next)
	if err != nil {
		return nil, fmt.Errorf("transition: cannot serialize %s: %w", next.CanonicalIdentityForm, err)
	}
	hash, _, err := st.PutUnit(unitJSON, next.ContentPayload, store.Ref{
		Form:            next.CanonicalIdentityForm,
		ProjectID:       project,
		SourceRepo:      sourceRepo,
		Namespace:       next.Identity.Namespace,
		Type:            next.Identity.Type,
		ID:              next.Identity.ID,
		InstanceVersion: next.Identity.InstanceVersion,
		Revision:        next.Revision,
		Dimension:       next.Classification.Dimension,
		Domain:          next.Classification.Domain,
		Phase:           next.Phase,
		UpdatedAt:       today,
	})
	if err != nil {
		return nil, fmt.Errorf("transition: cannot publish %s: %w", next.CanonicalIdentityForm, err)
	}
	return &TransitionResult{
		Target:     current.Identity.Namespace + "/" + current.Identity.Type + ":" + current.Identity.ID,
		From:       from,
		To:         to,
		By:         byIdentity,
		ObjectHash: hash,
	}, nil
}

// gateNote is one note candidate of the R13 gate evaluation: its role
// (structured content), its note-state and its identity (for the
// done-gate listing).
type gateNote struct {
	Role      string
	NoteState string
	Identity  string
}

// collectGateNotes gathers the notes discussing the target work item:
// the published cmt- units of the project (repo-synced or
// workspace-published) plus the cmt- drafts under EKA_HOME/drafts.
func collectGateNotes(ws *workspace.Workspace, st *store.Store, project string, target conformance.Reference) ([]gateNote, error) {
	var notes []gateNote
	units, err := st.UnitsByProject(project)
	if err != nil {
		return nil, err
	}
	// Per note LINE one gate note: the line's CURRENT payload — the
	// latest instance (publish advances the line; older instances stay
	// in the archive and never gate).
	latest := map[string]*exchange.Unit{}
	for _, u := range units {
		if u.Identity.Type != "cmt" {
			continue
		}
		if !discussesTarget(u, target) {
			continue
		}
		key := u.Identity.Namespace + "/" + u.Identity.Type + ":" + u.Identity.ID
		if prev, ok := latest[key]; !ok || u.Identity.InstanceVersion > prev.Identity.InstanceVersion {
			latest[key] = u
		}
	}
	for _, u := range latest {
		notes = append(notes, gateNote{
			Role:      noteRoleOf(u),
			NoteState: u.StateVector.NoteState,
			Identity:  u.Identity.Namespace + "/cmt:" + u.Identity.ID,
		})
	}
	// cmt- drafts of the project's drafts directory.
	dir := filepath.Join(draftsRoot(ws), project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return notes, nil
		}
		return nil, fmt.Errorf("cannot scan drafts: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || !strings.HasPrefix(e.Name(), "cmt-") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		artifact, err := conformance.ScanFile(path)
		if err != nil || artifact == nil || artifact.Type != "cmt" {
			continue // Unreadable/malformed drafts are publish's concern.
		}
		if !discussesTargetArtifact(artifact, target) {
			continue
		}
		role, _ := artifact.ContentFields["role"].(string)
		notes = append(notes, gateNote{
			Role:      role,
			NoteState: artifact.States[conformance.DomainNoteState],
			Identity:  artifact.Namespace + "/cmt:" + artifact.ID,
		})
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Identity < notes[j].Identity })
	return notes, nil
}

// discussesTarget reports whether a unit's discusses references resolve
// to the target identity line.
func discussesTarget(u *exchange.Unit, target conformance.Reference) bool {
	for _, rel := range u.Relationships {
		if rel.Type != "discusses" {
			continue
		}
		ref, err := conformance.ParseReference(rel.Target, u.Identity.Namespace, u.Identity.Type)
		if err != nil {
			continue
		}
		if ref.Namespace == target.Namespace && ref.Type == target.Type && ref.ID == target.ID {
			return true
		}
	}
	return false
}

// discussesTargetArtifact reports whether a draft artifact's discusses
// references resolve to the target identity line.
func discussesTargetArtifact(a *conformance.Artifact, target conformance.Reference) bool {
	for _, raw := range a.Relations["discusses"] {
		ref, err := conformance.ParseReference(raw, a.Namespace, a.Type)
		if err != nil {
			continue
		}
		if ref.Namespace == target.Namespace && ref.Type == target.Type && ref.ID == target.ID {
			return true
		}
	}
	return false
}

// noteRoleOf extracts the role of a published cmt- unit from its
// structured-json payload ("" when the payload is not parseable).
func noteRoleOf(u *exchange.Unit) string {
	if u.Content.Representation != exchange.StructuredJSON {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(u.ContentPayload, &fields); err != nil || fields == nil {
		return ""
	}
	role, _ := fields["role"].(string)
	return role
}

// gateViolations evaluates the D6 gates for the destination state:
// in-review requires at least one note with role "implementation" and
// note-state "resolved"; done requires EVERY note resolved (the open
// note identities are listed). Empty = the gates pass.
func gateViolations(notes []gateNote, to string) []string {
	switch to {
	case "in-review":
		for _, n := range notes {
			if n.Role == conformance.NoteRoleImplementation && n.NoteState == "resolved" {
				return nil
			}
		}
		return []string{fmt.Sprintf("transition gate R13: execution-state in-review requires at least one note with role \"implementation\" and note-state \"resolved\" (got %d notes discussing the work item)", len(notes))}
	case "done":
		var open []string
		for _, n := range notes {
			if n.NoteState != "resolved" {
				open = append(open, n.Identity)
			}
		}
		if len(open) > 0 {
			sort.Strings(open)
			return []string{fmt.Sprintf("transition gate R13: execution-state done requires every note resolved; open notes: %s",
				strings.Join(open, ", "))}
		}
	}
	return nil
}

// ticketMembership extracts the container line and work item line a
// ticket registers: the derives-from references of a tkt- unit (the
// membership rule of the execution projection — the first ctr-
// reference is the container, the first work-item reference the
// registered item). References that do not parse or do not resolve in
// the store are skipped; "" = the side is not registered.
func ticketMembership(u *exchange.Unit, resolve func(conformance.Reference) *exchange.Unit) (container, workItem string) {
	for _, rel := range u.Relationships {
		if rel.Type != "derives-from" {
			continue
		}
		ref, err := conformance.ParseReference(rel.Target, u.Identity.Namespace, u.Identity.Type)
		if err != nil || resolve(ref) == nil {
			continue
		}
		line := ref.Namespace + "/" + ref.Type + ":" + ref.ID
		switch {
		case container == "" && ref.Type == "ctr":
			container = line
		case workItem == "" && conformance.IsWorkItemType(ref.Type):
			workItem = line
		}
	}
	return container, workItem
}

// registeredInActiveContainer reports whether the target work item is
// registered in the current active container: there must be a ticket
// (tkt-) deriving from an active container (ctr- with
// container-state active) whose derives-from also resolves to the work
// item (the membership rule of the execution projection).
func registeredInActiveContainer(st *store.Store, project string, target conformance.Reference) bool {
	units, err := st.UnitsByProject(project)
	if err != nil {
		return false // Conservative: an unreadable store never confirms.
	}
	byLine := map[string]*exchange.Unit{}
	for _, u := range units {
		key := u.Identity.Namespace + "/" + u.Identity.Type + ":" + u.Identity.ID
		if cur, ok := byLine[key]; !ok || u.Identity.InstanceVersion > cur.Identity.InstanceVersion {
			byLine[key] = u
		}
	}
	resolve := func(ref conformance.Reference) *exchange.Unit {
		return byLine[ref.Namespace+"/"+ref.Type+":"+ref.ID]
	}
	activeContainers := map[string]bool{}
	for _, u := range units {
		if u.Identity.Type == "ctr" && u.StateVector.ContainerState == "active" {
			activeContainers[u.Identity.Namespace+"/ctr:"+u.Identity.ID] = true
		}
	}
	targetLine := target.Namespace + "/" + target.Type + ":" + target.ID
	for _, u := range units {
		if u.Identity.Type != "tkt" {
			continue
		}
		container, workItem := ticketMembership(u, resolve)
		if container != "" && activeContainers[container] && workItem == targetLine {
			return true
		}
	}
	return false
}

// containerWorkItems returns the work items of a container line: every
// tkt- unit whose derives-from resolves to the container, then the
// work item lines those tickets register (deduped by line), resolved
// to the HIGHEST instance per line (the current state — the all-done
// gate never reads stale instances), sorted by canonical form. Store
// failures return nil: a container whose membership cannot be read
// never passes the all-done gate.
func containerWorkItems(st *store.Store, project string, ctrRef conformance.Reference) []*exchange.Unit {
	units, err := st.UnitsByProject(project)
	if err != nil {
		return nil
	}
	byLine := map[string]*exchange.Unit{}
	for _, u := range units {
		key := u.Identity.Namespace + "/" + u.Identity.Type + ":" + u.Identity.ID
		if cur, ok := byLine[key]; !ok || u.Identity.InstanceVersion > cur.Identity.InstanceVersion {
			byLine[key] = u
		}
	}
	resolve := func(ref conformance.Reference) *exchange.Unit {
		return byLine[ref.Namespace+"/"+ref.Type+":"+ref.ID]
	}
	ctrLine := ctrRef.Namespace + "/" + ctrRef.Type + ":" + ctrRef.ID
	seen := map[string]bool{}
	var out []*exchange.Unit
	for _, u := range units {
		if u.Identity.Type != "tkt" {
			continue
		}
		container, workItem := ticketMembership(u, resolve)
		if container != ctrLine || workItem == "" || seen[workItem] {
			continue
		}
		seen[workItem] = true
		wref, err := conformance.ParseReference(workItem, "", "")
		if err != nil {
			continue
		}
		if w := resolve(wref); w != nil {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CanonicalIdentityForm < out[j].CanonicalIdentityForm })
	return out
}

// resolveRepoContext resolves the repository context of a transition/
// note run: the walk-up that carries eka.yaml is the repository root
// (ADR-018; no legacy mode). Not being an EKA repository is a
// deterministic refusal (the caller wraps it in its own refusal type).
func resolveRepoContext(repoPath string) (root string, meta metadata.Metadata, err error) {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", metadata.Metadata{}, fmt.Errorf("cannot resolve the repository path: %w", err)
	}
	abs = filepath.Clean(abs)
	m, mdir, hasMeta, err := metadata.Find(abs)
	if err != nil {
		return "", metadata.Metadata{}, err
	}
	if !hasMeta {
		return "", metadata.Metadata{}, repoContextRefusal(abs)
	}
	return mdir, m, nil
}

// repoContext is the typed refusal of a non-EKA repository (ADR-018; no
// legacy mode). Both transition and note wrap it in their own refusal
// types so the command renders the correct prefix.
type repoContext struct {
	abs string
}

func (e repoContext) Error() string {
	return fmt.Sprintf("%s is not an EKA repository (no eka.yaml)", e.abs)
}

func repoContextRefusal(abs string) error {
	return repoContext{abs: abs}
}
