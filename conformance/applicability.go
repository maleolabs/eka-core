package conformance

import "github.com/maleolabs/eka-core/pathglob"

// This file implements the composition check of the EKA Standard 1.5
// applicability declaration (specification/exchange.md, "Content:
// applicability declaration (optional)"): the optional content field
// `appliesTo` must be an array of interpretable path patterns.
//
// Rule placement (documented). A malformed declaration is a CONTENT-SHAPE
// failure — the same class as "content value must be a string or nested
// object" and the capture-envelope date shapes, none of which carry a
// numbered rule. It is therefore reported in the R0 structural bucket, the
// engine's home for pre-rule structural findings, rather than as a new
// numbered rule: R1-R13 are obligations of normative knowledge, while this
// field is additive and absent by default, so a new rule number would imply
// an obligation the standard does not impose. Absence is not a violation —
// an Artifact that declares no `appliesTo` binds no path. The repo-level
// warning "a well-formed pattern matches no path" is a separate, later
// increment and is deliberately not implemented here.

// checkAppliesTo reports a malformed `appliesTo` on one artifact. It reads
// the canonical content object — the JSON-native ContentFields, or the
// compiled Markdown content object in the repository path — mirroring the
// shape the exchange layer compiles into the unit payload (and the shape
// the contexts retrieval door reads back).
func (e *engine) checkAppliesTo(a *Artifact) {
	content := e.contentObject(a)
	if content == nil {
		return
	}
	raw, ok := content["appliesTo"]
	if !ok {
		return // Absent: the artifact binds no path — not a violation.
	}
	if _, err := pathglob.Interpret(raw); err != nil {
		e.add(a, RuleStructural, SeverityError, "`appliesTo` %v", err)
	}
}

// contentObject returns the structured content object the applicability
// declaration lives in: the JSON-native content fields when present,
// otherwise (repository path only) the compiled Markdown content object.
// In CKO mode a legacy structured-text unit carries no structured content
// object — its payload is not a JSON object — so there is nothing to read
// and nothing to report, which is exactly what the payload-based retrieval
// door observes for the same unit.
func (e *engine) contentObject(a *Artifact) map[string]any {
	if a.ContentFields != nil {
		return a.ContentFields
	}
	if !e.skipLocation {
		return CompileContentObject(a)
	}
	return nil
}
