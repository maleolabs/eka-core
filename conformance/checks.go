package conformance

import "github.com/maleolabs/eka-core/v2/pathglob"

// This file implements the composition check of the EKA Standard 1.7
// declared-checks declaration (specification/exchange.md, "Content: declared
// checks (optional)"): the optional content field `checks` must be an array of
// objects, each with a non-empty `command` and an interpretable `scope`.
//
// Rule placement mirrors checkAppliesTo. A malformed declaration is a
// CONTENT-SHAPE failure — the same class as a malformed `appliesTo` — reported
// in the R0 structural bucket, not a new numbered rule: the field is additive
// and absent by default, so a new rule number would imply an obligation the
// standard does not impose. Absence is not a violation — an Artifact that
// declares no `checks` is checklist-only.

// checkChecks reports a malformed `checks` on one artifact. It reads the
// canonical content object — the JSON-native ContentFields, or the compiled
// Markdown content object in the repository path — mirroring the shape the
// exchange layer compiles into the unit payload (and the shape the contexts
// retrieval door reads back).
func (e *engine) checkChecks(a *Artifact) {
	content := e.contentObject(a)
	if content == nil {
		return
	}
	raw, ok := content["checks"]
	if !ok {
		return // Absent: the artifact declares no check — not a violation.
	}
	if _, err := pathglob.InterpretChecks(raw); err != nil {
		e.add(a, RuleStructural, SeverityError, "`checks` %v", err)
	}
}
