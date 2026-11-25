package conformance

import (
	"encoding/json"
	"strings"
	"testing"
)

// This file tests the Standard 1.5 applicability-declaration shape check
// (checkAppliesTo): a malformed `appliesTo` content field is an R0
// structural violation; an absent or well-formed one is not.

// stdPathJSON builds a minimal conformant JSON-native std- artifact with the
// given content object, placed under docs/standards (dimension standards, R6)
// with a matching std- filename (R2).
func stdPathJSON(t *testing.T, content map[string]any) string {
	t.Helper()
	doc := map[string]any{
		"namespace":       "eka-cf",
		"type":            "std",
		"id":              "pathscope",
		"instanceVersion": 1,
		"revision":        1,
		"state":           map[string]any{"contentState": "draft", "existenceState": "active"},
		"dimension":       "standards",
		"changeLog": []any{
			map[string]any{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering"},
			map[string]any{"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering"},
		},
		"content": content,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// appliesToFindings returns the R0 results mentioning the `appliesTo` field.
func appliesToFindings(report *Report) []Result {
	var out []Result
	for _, r := range resultsFor(report, RuleStructural) {
		if strings.Contains(r.Message, "`appliesTo`") {
			out = append(out, r)
		}
	}
	return out
}

// TestAppliesToMalformedIsStructural: a non-array value, a non-string
// element and an uninterpretable pattern are each a blocking structural
// violation — never silently ignored.
func TestAppliesToMalformedIsStructural(t *testing.T) {
	cases := []struct {
		name    string
		content map[string]any
	}{
		{"non-array value", map[string]any{"purpose": "p", "content": "c", "appliesTo": "src/**"}},
		{"non-string element", map[string]any{"purpose": "p", "content": "c", "appliesTo": []any{"ok", 42}}},
		{"uninterpretable pattern", map[string]any{"purpose": "p", "content": "c", "appliesTo": []any{"a**b"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := validateTempRepo(t, map[string]string{
				"docs/standards/std-pathscope.json": stdPathJSON(t, c.content),
			})
			findings := appliesToFindings(report)
			if len(findings) != 1 {
				t.Fatalf("want exactly one `appliesTo` R0 finding, got %d:\n%s", len(findings), dumpResults(report))
			}
			if findings[0].Severity != SeverityError {
				t.Errorf("malformed appliesTo must be a blocking error, got %s", findings[0].Severity)
			}
		})
	}
}

// TestAppliesToAbsentOrWellFormedIsClean: absence binds nothing and a valid
// declaration is accepted — neither produces an `appliesTo` finding.
func TestAppliesToAbsentOrWellFormedIsClean(t *testing.T) {
	for name, content := range map[string]map[string]any{
		"absent":      {"purpose": "p", "content": "c"},
		"well-formed": {"purpose": "p", "content": "c", "appliesTo": []any{"src/**", "docs/*.md", "**/README"}},
	} {
		t.Run(name, func(t *testing.T) {
			report := validateTempRepo(t, map[string]string{
				"docs/standards/std-pathscope.json": stdPathJSON(t, content),
			})
			if findings := appliesToFindings(report); len(findings) != 0 {
				t.Errorf("unexpected `appliesTo` finding:\n%s", dumpResults(report))
			}
		})
	}
}
