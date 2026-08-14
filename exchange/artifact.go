package exchange

import (
	"encoding/json"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
)

// This file implements the unit -> artifact projection consumed by the
// CKO-level validator: the read-side counterpart of builder.go toUnit
// (artifact -> unit).
//
// The conformance package must not import this package (exchange
// already imports conformance for DomainForToken; a reciprocal import
// would be an import cycle), so the CKO validator consumes units through
// the conformance.CKOArtifact interface, and this projection lives here
// where both models are importable. Documented deviation from the spec's
// spelling (conformance.ValidateCKO(u *exchange.Unit, opts)) — the
// runtime wires the two together; the contract is unchanged.

// ToArtifact projects the unit onto the conformance Artifact model —
// the shape the rule engine evaluates. Every field is derived losslessly
// from the unit composition:
//
//   - identity, revision, author/created/updated as recorded;
//   - the present state domains (non-empty values) as the States map;
//   - phase, classification, engineering domain as declared;
//   - relationships as the field -> target-list map (only the five
//     canonical relationship types produce entries; a unit carrying
//     another type is reported by ValidateCKO's structural check);
//   - the change log in occurrence order;
//   - the content: structured-json payloads (the v2.0 representation)
//     project onto ContentFields — the structured content object the
//     section rules evaluate (R9 as a key check, R8 content values);
//     legacy structured-text payloads project onto BodyLines.
//
// RelPath carries the unit's canonical identity form so validation
// findings identify the unit deterministically (the location rules that
// would interpret it as a filename are skipped in CKO mode).
func (u *Unit) ToArtifact() *conformance.Artifact {
	a := &conformance.Artifact{
		RelPath:         u.CanonicalIdentityForm,
		Namespace:       u.Identity.Namespace,
		Type:            u.Identity.Type,
		ID:              u.Identity.ID,
		InstanceVersion: u.Identity.InstanceVersion,
		Revision:        u.Revision,
		Author:          u.Author.Name,
		AuthorKind:      u.Author.Kind,
		Created:         u.Created,
		Updated:         u.Updated,
		States:          map[string]string{},
		Relations:       map[string][]string{},
	}
	if u.StateVector.ContentState != "" {
		a.States[conformance.DomainContentState] = u.StateVector.ContentState
	}
	if u.StateVector.ExecutionState != "" {
		a.States[conformance.DomainExecutionState] = u.StateVector.ExecutionState
	}
	if u.StateVector.PlanningState != "" {
		a.States[conformance.DomainPlanningState] = u.StateVector.PlanningState
	}
	if u.StateVector.ContainerState != "" {
		a.States[conformance.DomainContainerState] = u.StateVector.ContainerState
	}
	if u.StateVector.ExistenceState != "" {
		a.States[conformance.DomainExistenceState] = u.StateVector.ExistenceState
	}
	if u.StateVector.NoteState != "" {
		a.States[conformance.DomainNoteState] = u.StateVector.NoteState
	}
	if u.Phase != "" {
		a.HasPhaseKey = true
		a.HasPhase = true
		a.Phase = u.Phase
	}
	if u.Classification.Dimension != "" {
		a.HasDimension = true
		a.Dimension = u.Classification.Dimension
	}
	a.DimensionsSecondary = u.Classification.DimensionsSecondary
	a.Domain = u.Classification.Domain
	for _, rel := range u.Relationships {
		a.Relations[rel.Type] = append(a.Relations[rel.Type], rel.Target)
	}
	for _, e := range u.ChangeLog {
		a.ChangeLog = append(a.ChangeLog, conformance.ChangeLogEntry{
			Date: e.Date, Domain: e.Domain, From: e.From, To: e.To, By: e.By.Name, ByKind: e.By.Kind,
		})
	}
	a.BodyLines = strings.Split(string(u.ContentPayload), "\n")
	if u.Content.Representation == StructuredJSON {
		// The structured content object: the canonical payload parses
		// back into the fields the section rules evaluate. An
		// unparseable payload (hand-built units) degrades to the
		// body-lines shape: R9's heading check reports the missing
		// sections instead of panicking.
		var fields map[string]any
		if err := json.Unmarshal(u.ContentPayload, &fields); err == nil && fields != nil {
			a.ContentFields = fields
		}
	}
	return a
}
