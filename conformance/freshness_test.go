package conformance

import (
	"encoding/json"
	"strings"
	"testing"
)

// validJSONArtifact is a minimal conformant JSON-native artifact with the
// given extra top-level fields merged in.
func validJSONArtifact(extra map[string]any) []byte {
	doc := map[string]any{
		"namespace":       "eka",
		"type":            "adr",
		"id":              "freshness-probe",
		"instanceVersion": 1,
		"revision":        1,
		"state":           map[string]any{"contentState": "proposed", "existenceState": "active"},
		"changeLog": []any{
			map[string]any{"date": "2026-11-18", "domain": "contentState", "from": "-", "to": "proposed", "by": "Tester"},
		},
		"content": map[string]any{},
	}
	for k, v := range extra {
		doc[k] = v
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return b
}

// TestJSONAdapterAcceptsFreshnessFields: validUntil and lastConfirmed are
// allowlisted top-level fields — without that every JSON-native artifact
// carrying a horizon would be an R0 unknown-field violation (which would
// make the field unusable). The values classify onto the Artifact model.
func TestJSONAdapterAcceptsFreshnessFields(t *testing.T) {
	data := validJSONArtifact(map[string]any{
		"validUntil":    "2027-01-01",
		"lastConfirmed": "2026-11-18",
	})
	a, results := analyzeJSON("x.json", "/x.json", data, true)
	if a == nil {
		t.Fatalf("artifact rejected: %+v", results)
	}
	for _, r := range results {
		if r.Rule == RuleStructural && strings.Contains(r.Message, "unknown top-level field") {
			t.Fatalf("freshness fields must be allowlisted: %s", r.Message)
		}
	}
	if a.ValidUntil != "2027-01-01" || a.LastConfirmed != "2026-11-18" {
		t.Fatalf("classified as validUntil=%q lastConfirmed=%q", a.ValidUntil, a.LastConfirmed)
	}
}

// TestJSONAdapterRejectsMalformedFreshnessDate: the shape is a structural
// concern even though the value never is — a typo must not degrade
// silently into "undeclared".
func TestJSONAdapterRejectsMalformedFreshnessDate(t *testing.T) {
	for _, key := range []string{"validUntil", "lastConfirmed"} {
		data := validJSONArtifact(map[string]any{key: "2026-13-45"})
		_, results := analyzeJSON("x.json", "/x.json", data, true)
		found := false
		for _, r := range results {
			if r.Rule == RuleStructural && strings.Contains(r.Message, "`"+key+"` must be a date in YYYY-MM-DD format") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s malformed date produced no structural finding: %+v", key, results)
		}
	}
}

// TestMarkdownAdapterCarriesFreshness: the frontmatter adapter classifies
// the same two fields (unquoted dates arrive as YAML timestamps and are
// normalised back to YYYY-MM-DD, like created/updated).
func TestMarkdownAdapterCarriesFreshness(t *testing.T) {
	md := []byte("---\nnamespace: eka\ntype: adr\nid: freshness-md\ninstance-version: 1\nrevision: 1\n" +
		"validUntil: 2027-01-01\nlastConfirmed: 2026-11-18\n---\n# x\n")
	a, results := analyzeMarkdown("x.md", "/x.md", md, true)
	if a == nil {
		t.Fatalf("artifact rejected: %+v", results)
	}
	if a.ValidUntil != "2027-01-01" || a.LastConfirmed != "2026-11-18" {
		t.Fatalf("classified as validUntil=%q lastConfirmed=%q", a.ValidUntil, a.LastConfirmed)
	}
}

// TestMarkdownAdapterRejectsMalformedFreshnessDate mirrors the JSON case on
// the frontmatter path: the finding shape matches the `created` one.
func TestMarkdownAdapterRejectsMalformedFreshnessDate(t *testing.T) {
	md := []byte("---\nnamespace: eka\ntype: adr\nid: freshness-bad\ninstance-version: 1\nrevision: 1\n" +
		"validUntil: 2026-13-45\n---\n# x\n")
	_, results := analyzeMarkdown("x.md", "/x.md", md, true)
	for _, r := range results {
		if r.Rule == RuleStructural && strings.Contains(r.Message, "`validUntil` must be a date in YYYY-MM-DD format") {
			return
		}
	}
	t.Fatalf("no structural finding for a malformed validUntil: %+v", results)
}
