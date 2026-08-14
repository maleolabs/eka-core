package conformance

import (
	"strings"
	"unicode"
)

// This file implements the v2.0 naming helpers shared by the JSON
// authoring adapter, the rule engine, the compiler and the draft
// template (spec-standard-v2 §2):
//
//	SectionKey        camelCase derivation of section registry names
//	stateKeyToKebab   JSON state domain spelling -> internal kebab form
//	relationshipKeyToKebab  JSON relationship spelling -> internal kebab
//
// The engine is v1.1-shaped (kebab keys everywhere internally); the JSON
// authoring spelling is camelCase, so the adapters convert on the way in
// and the SectionKey helper converts on the way to the content keys.

// SectionKey derives the camelCase content key of a section registry
// name (spec-standard-v2 §2): lowercase the first word, capitalize
// subsequent words, drop non-alphanumeric separators. Deterministic:
//
//	"Alternatives Considered" -> "alternativesConsidered"
//	"Acceptance Criteria"      -> "acceptanceCriteria"
//	"Out of Scope"             -> "outOfScope"
//	"Work Items"               -> "workItems"
//	"Projected Status"         -> "projectedStatus"
//	"Change Log"               -> "changeLog"
//	"Investigation Summary"    -> "investigationSummary"
//	"Context"                  -> "context"
func SectionKey(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	var b strings.Builder
	for i, w := range words {
		if i == 0 {
			b.WriteString(strings.ToLower(w))
			continue
		}
		r := []rune(w)
		if len(r) == 0 {
			continue
		}
		b.WriteRune(unicode.ToUpper(r[0]))
		b.WriteString(strings.ToLower(string(r[1:])))
	}
	return b.String()
}

// IsKnownType reports whether token is a registered artifact type token
// (the type table of state.go). Exported for the view package's
// document-target parsing ("<type>-<id>" file forms).
func IsKnownType(token string) bool {
	_, ok := typeTokens[token]
	return ok
}

// jsonStateKeyToDomain maps the JSON authoring state domain keys
// (spec-standard-v2 §3.2) to the internal kebab domain names the rule
// engine evaluates. State VALUES are unchanged — only the keys are
// camelCase.
var jsonStateKeyToDomain = map[string]string{
	"contentState":   DomainContentState,
	"executionState": DomainExecutionState,
	"planningState":  DomainPlanningState,
	"containerState": DomainContainerState,
	"existenceState": DomainExistenceState,
	"noteState":      DomainNoteState,
	"phase":          DomainPhase,
}

// jsonRelKeyToField maps the JSON authoring relationship type keys to the
// internal kebab field names (spec-standard-v2 §2 relationship type
// names: dependsOn, derivesFrom, validates, supersedes, amends).
var jsonRelKeyToField = map[string]string{
	"amends":      "amends",
	"supersedes":  "supersedes",
	"derivesFrom": "derives-from",
	"dependsOn":   "depends-on",
	"validates":   "validates",
}

// stateKeyToKebab converts a JSON authoring state domain key to its
// internal kebab form. Known domains map exactly; unknown keys degrade
// deterministically to the generic camelCase -> kebab conversion, so the
// rule engine reports them as unknown domains instead of silently
// dropping them.
func stateKeyToKebab(key string) string {
	if d, ok := jsonStateKeyToDomain[key]; ok {
		return d
	}
	return camelToKebab(key)
}

// StateKeyKebab renders the internal kebab form of a JSON authoring
// state domain or relationship key (contentState -> content-state,
// dependsOn -> depends-on; phase stays phase). Exported for the inline
// publish adapter, whose input follows the §3.2 camelCase schema while
// the CKO model carries the kebab domains.
func StateKeyKebab(key string) string {
	return stateKeyToKebab(key)
}

// StateKeyCamel renders the JSON authoring spelling of an internal
// state domain or relationship field name (content-state ->
// contentState; depends-on -> dependsOn; phase stays phase). Exported
// for the authoring layers (the draft template and the import writer
// emit the camelCase schema).
func StateKeyCamel(domain string) string {
	parts := strings.Split(domain, "-")
	out := parts[0]
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		out += strings.ToUpper(p[:1]) + p[1:]
	}
	return out
}

// relationshipKeyToKebab converts a JSON authoring relationship type key
// to its internal kebab field name. Unknown keys degrade to the generic
// conversion; the JSON adapter reports them before they reach the engine.
func relationshipKeyToKebab(key string) string {
	if f, ok := jsonRelKeyToField[key]; ok {
		return f
	}
	return camelToKebab(key)
}

// camelToKebab converts camelCase to kebab-case (lowercased, "-" before
// uppercase boundaries).
func camelToKebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('-')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// isStateField reports whether f is one of the five owned state domain
// names.
func isStateField(f string) bool {
	for _, d := range stateFields {
		if d == f {
			return true
		}
	}
	return false
}
