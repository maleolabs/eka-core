package conformance

import (
	"strings"
	"testing"
)

// This file tests the Standard 1.7 declared-checks shape check (checkChecks):
// a malformed `checks` content field is an R0 structural violation; an absent
// or well-formed one is not.

// checksFindings returns the R0 results mentioning the `checks` field.
func checksFindings(report *Report) []Result {
	var out []Result
	for _, r := range resultsFor(report, RuleStructural) {
		if strings.Contains(r.Message, "`checks`") {
			out = append(out, r)
		}
	}
	return out
}

// TestChecksMalformedIsStructural: a non-array value, a non-object element, a
// missing/non-string/empty command, a non-array scope and an uninterpretable
// scope pattern are each a blocking structural violation — never silently
// ignored.
func TestChecksMalformedIsStructural(t *testing.T) {
	cases := []struct {
		name    string
		content map[string]any
	}{
		{"non-array value", map[string]any{"purpose": "p", "content": "c", "checks": "make test"}},
		{"non-object element", map[string]any{"purpose": "p", "content": "c", "checks": []any{"make test"}}},
		{"missing command", map[string]any{"purpose": "p", "content": "c", "checks": []any{map[string]any{"scope": []any{"src/**"}}}}},
		{"non-string command", map[string]any{"purpose": "p", "content": "c", "checks": []any{map[string]any{"command": 42}}}},
		{"empty command", map[string]any{"purpose": "p", "content": "c", "checks": []any{map[string]any{"command": ""}}}},
		{"non-array scope", map[string]any{"purpose": "p", "content": "c", "checks": []any{map[string]any{"command": "make test", "scope": "src/**"}}}},
		{"uninterpretable scope", map[string]any{"purpose": "p", "content": "c", "checks": []any{map[string]any{"command": "make test", "scope": []any{"a**b"}}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := validateTempRepo(t, map[string]string{
				"docs/standards/std-pathscope.json": stdPathJSON(t, c.content),
			})
			findings := checksFindings(report)
			if len(findings) != 1 {
				t.Fatalf("want exactly one `checks` R0 finding, got %d:\n%s", len(findings), dumpResults(report))
			}
			if findings[0].Severity != SeverityError {
				t.Errorf("malformed checks must be a blocking error, got %s", findings[0].Severity)
			}
		})
	}
}

// TestChecksAbsentOrWellFormedIsClean: absence declares no check and a valid
// declaration is accepted — neither produces a `checks` finding.
func TestChecksAbsentOrWellFormedIsClean(t *testing.T) {
	for name, content := range map[string]map[string]any{
		"absent": {"purpose": "p", "content": "c"},
		"well-formed": {"purpose": "p", "content": "c", "checks": []any{
			map[string]any{"command": "make test", "scope": []any{"src/**", "docs/*.md"}},
		}},
		"empty array":  {"purpose": "p", "content": "c", "checks": []any{}},
		"absent scope": {"purpose": "p", "content": "c", "checks": []any{map[string]any{"command": "make test"}}},
		"empty scope":  {"purpose": "p", "content": "c", "checks": []any{map[string]any{"command": "make test", "scope": []any{}}}},
	} {
		t.Run(name, func(t *testing.T) {
			report := validateTempRepo(t, map[string]string{
				"docs/standards/std-pathscope.json": stdPathJSON(t, content),
			})
			if findings := checksFindings(report); len(findings) != 0 {
				t.Errorf("unexpected `checks` finding:\n%s", dumpResults(report))
			}
		})
	}
}
