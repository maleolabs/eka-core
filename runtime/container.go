package runtime

import (
	"fmt"
	"time"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/store"
)

// This file implements the container lifecycle of the Authoring API
// (protocol §4 lock-atomic-with-generation, Option B): the plan lock
// that happens atomically with the container ACTIVATION. A container
// (ctr-) derives from a plan (plan-) through its depends-on reference;
// containers are born PLANNED (the draft template's initial
// container-state; publish persists no lock) and ACTIVATE one at a
// time (planned -> active, protocol §3 exactly-one-active). Activating
// a container persists the active container unit AND moves the plan's
// planning-state to immutable (with an appended change-log entry) in
// ONE store transaction (store.PutUnits) — the active container and
// its locked plan land together or not at all. The lock is only legal
// on an approved plan; an already-immutable plan is a valid idempotent
// skip (a previous activation locked it; the new container derives
// from the locked plan). Direct transitions to planning-state
// immutable are refused: the lock belongs to the container activation.

// PlanLockInfo reports the plan lock performed by a container
// activation.
type PlanLockInfo struct {
	// Plan is the canonical line form of the locked plan
	// ("<namespace>/plan:<id>").
	Plan string
	// Hash is the object hash of the locked plan payload — filled by
	// the caller from the batch's second stored hash (put 2) once
	// store.PutUnits returned; "" before the batch is persisted.
	Hash string
	// Locked reports whether the lock was performed this run; false =
	// the plan was already immutable and the lock was skipped
	// (idempotent).
	Locked bool
}

// lockPlanPuts builds the store batch of one container activation:
// put 1 = the container's active payload (the transitioned unit —
// planned -> active with the appended change-log entry), put 2 = the
// locked plan payload (approved -> immutable with the appended
// change-log entry) — in ONE transaction, so the activation and its
// plan lock land together or not at all (store.PutUnits). An
// already-immutable plan is the idempotent skip (put 2 omitted).
// Preconditions (deterministic refusals, TransitionRefusal): the
// container must declare exactly-one depends-on plan reference (else
// refusal), and the plan must be approved (planning-state "approved",
// read from the line's highest instance; else refusal with the hint
// 'approve it first: eka transition plan:<id> approved'). An
// already-immutable plan is a valid state: the lock is skipped (a
// previous activation locked it). Returns the puts and the lock info.
func lockPlanPuts(st *store.Store, project, sourceRepo string, ctr *exchange.Unit, by conformance.AuthorIdentity) ([]store.Put, *PlanLockInfo, error) {
	planLine, err := resolveDependsOnPlan(ctr)
	if err != nil {
		return nil, nil, err
	}
	info := &PlanLockInfo{Plan: planLine.Namespace + "/plan:" + planLine.ID}

	// Put 1: the container's active payload (the transitioned unit —
	// planned -> active with the appended change-log entry).
	unitJSON, err := exchange.MarshalUnit(ctr)
	if err != nil {
		return nil, nil, fmt.Errorf("transition: cannot serialize %s: %w", ctr.CanonicalIdentityForm, err)
	}
	puts := []store.Put{{
		UnitJSON: unitJSON,
		Content:  ctr.ContentPayload,
		Ref: store.Ref{
			Form:            ctr.CanonicalIdentityForm,
			ProjectID:       project,
			SourceRepo:      sourceRepo,
			Namespace:       ctr.Identity.Namespace,
			Type:            ctr.Identity.Type,
			ID:              ctr.Identity.ID,
			InstanceVersion: ctr.Identity.InstanceVersion,
			Revision:        ctr.Revision,
			Dimension:       ctr.Classification.Dimension,
			Domain:          ctr.Classification.Domain,
			Phase:           ctr.Phase,
			UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
		},
	}}

	// The plan's current state: the highest instance of the line.
	line, err := st.UnitsByLine(planLine.Namespace, planLine.Type, planLine.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("transition: %w", err)
	}
	var plan *exchange.Unit
	for _, cand := range line {
		if plan == nil || cand.Identity.InstanceVersion > plan.Identity.InstanceVersion {
			plan = cand
		}
	}
	if plan == nil {
		return nil, nil, &TransitionRefusal{
			Reason: fmt.Sprintf("the plan %s was not found in the workspace store", info.Plan),
			Hint:   "run 'eka sync' first (the workspace store is the reference universe of transitions)",
		}
	}
	state := plan.StateVector.PlanningState

	// Put 2: the lock — only an approved plan locks. An immutable plan
	// is the idempotent skip; any other state refuses with the approve
	// hint.
	if state == "approved" {
		today := time.Now().Format("2006-01-02")
		locked := *plan // shallow copy; the mutable slices below are rebuilt.
		locked.StateVector.PlanningState = "immutable"
		locked.Updated = today
		locked.ChangeLog = append(append([]exchange.ChangeLogEntry{}, plan.ChangeLog...), exchange.ChangeLogEntry{
			Date: today, Domain: conformance.DomainPlanningState, From: state, To: "immutable", By: by,
		})
		planJSON, err := exchange.MarshalUnit(&locked)
		if err != nil {
			return nil, nil, fmt.Errorf("transition: cannot serialize the locked plan %s: %w", locked.CanonicalIdentityForm, err)
		}
		info.Locked = true
		puts = append(puts, store.Put{
			UnitJSON: planJSON,
			Content:  locked.ContentPayload,
			Ref: store.Ref{
				Form:            locked.CanonicalIdentityForm,
				ProjectID:       project,
				SourceRepo:      sourceRepo,
				Namespace:       locked.Identity.Namespace,
				Type:            locked.Identity.Type,
				ID:              locked.Identity.ID,
				InstanceVersion: locked.Identity.InstanceVersion,
				Revision:        locked.Revision,
				Dimension:       locked.Classification.Dimension,
				Domain:          locked.Classification.Domain,
				Phase:           locked.Phase,
				UpdatedAt:       today,
			},
		})
	} else if state != "immutable" {
		return nil, nil, &TransitionRefusal{
			Reason: fmt.Sprintf("the plan %s is not approved (planning-state: %s)", info.Plan, state),
			Hint:   fmt.Sprintf("approve it first: eka transition plan:%s approved", planLine.ID),
		}
	}
	return puts, info, nil
}

// resolveDependsOnPlan resolves the container's depends-on plan
// reference: every depends-on target is parsed (the parsed type token
// decides; a malformed target is skipped), the first plan- target is
// the lock target. None -> refusal (impossible after the scaffold
// guard, but the draft may be edited after scaffold, and docs-authored
// containers may carry no plan); more than one distinct plan reference
// -> refusal (the lock must be unambiguous).
func resolveDependsOnPlan(u *exchange.Unit) (conformance.Reference, error) {
	lines := map[string]conformance.Reference{}
	for _, rel := range u.Relationships {
		if rel.Type != "depends-on" {
			continue
		}
		ref, err := conformance.ParseReference(rel.Target, u.Identity.Namespace, u.Identity.Type)
		if err != nil || ref.Type != "plan" {
			continue
		}
		lines[ref.Namespace+"/"+ref.Type+":"+ref.ID] = ref
	}
	if len(lines) == 0 {
		return conformance.Reference{}, &TransitionRefusal{
			Reason: fmt.Sprintf("the container %s declares no depends-on plan reference", u.Identity.Namespace+"/ctr:"+u.Identity.ID),
			Hint:   "container drafts require --depends-on with a plan- reference",
		}
	}
	if len(lines) > 1 {
		return conformance.Reference{}, &TransitionRefusal{
			Reason: "the container must lock exactly one plan (depends-on)",
			Hint:   "keep a single depends-on plan reference on the container",
		}
	}
	for _, ref := range lines {
		return ref, nil
	}
	panic("unreachable")
}
