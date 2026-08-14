package conformance

import (
	"strings"
	"testing"
)

// analyzeFile unit tests: artifact classification and identity parsing.

const validADRFrontmatter = `---
namespace: eka-test
type: adr
id: 001-example
instance-version: 1
revision: 1
content-state: accepted
existence-state: active
dimension: decisions
created: 2026-08-05
updated: 2026-08-05
depends-on:
  - 001-other
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
  - date: 2026-08-05
    domain: content-state
    from: proposed
    to: accepted
    by: Engineering
---
`

func TestAnalyzeNoFrontmatterIsConventionDoc(t *testing.T) {
	a, results := analyzeFile("README.md", "/x/README.md", []byte("# Readme\n\nplain markdown\n"))
	if a != nil || len(results) != 0 {
		t.Fatalf("plain markdown: artifact=%v results=%v, want nil/none", a, results)
	}
}

func TestAnalyzeFrontmatterWithoutTypeIDIsConventionDoc(t *testing.T) {
	content := "---\ntitle: some doc\n---\n\n# Doc\n"
	a, results := analyzeFile("protocol.md", "/x/protocol.md", []byte(content))
	if a != nil || len(results) != 0 {
		t.Fatalf("frontmatter without type/id: artifact=%v results=%v, want nil/none", a, results)
	}
}

func TestAnalyzeUnterminatedFrontmatter(t *testing.T) {
	_, results := analyzeFile("bad.md", "/x/bad.md", []byte("---\ntype: adr\n"))
	if len(results) != 1 || results[0].Rule != RuleStructural {
		t.Fatalf("unterminated frontmatter: got %v, want one R0 result", results)
	}
}

func TestAnalyzeBrokenYAML(t *testing.T) {
	_, results := analyzeFile("bad.md", "/x/bad.md", []byte("---\ntype: [unclosed\n---\n"))
	if len(results) != 1 || results[0].Rule != RuleStructural || results[0].Severity != SeverityError {
		t.Fatalf("broken yaml: got %v, want one R0 error", results)
	}
}

func TestAnalyzeTypeXorID(t *testing.T) {
	_, results := analyzeFile("x.md", "/x/x.md", []byte("---\ntype: adr\n---\n"))
	if len(results) != 1 || results[0].Rule != RuleStructural {
		t.Fatalf("type XOR id: got %v, want one R0 result", results)
	}
}

func TestAnalyzeValidArtifact(t *testing.T) {
	a, results := analyzeFile("docs/decisions/adr-001-example.md", "/x/docs/decisions/adr-001-example.md", []byte(validADRFrontmatter+"\n# Title\n\n## Context\n\nctx\n"))
	if len(results) != 0 {
		t.Fatalf("valid artifact produced results: %v", results)
	}
	if a == nil {
		t.Fatal("valid artifact not classified as artifact")
	}
	if a.Namespace != "eka-test" || a.Type != "adr" || a.ID != "001-example" ||
		a.InstanceVersion != 1 || a.Revision != 1 {
		t.Errorf("identity fields wrong: %+v", a)
	}
	if a.States[DomainContentState] != "accepted" || a.States[DomainExistenceState] != "active" {
		t.Errorf("states wrong: %v", a.States)
	}
	if a.Dimension != "decisions" {
		t.Errorf("dimension = %q", a.Dimension)
	}
	if got := a.Relations["depends-on"]; len(got) != 1 || got[0] != "001-other" {
		t.Errorf("depends-on = %v", got)
	}
	if len(a.ChangeLog) != 2 {
		t.Fatalf("change-log entries = %d, want 2", len(a.ChangeLog))
	}
	// Unquoted YAML dates are resolved as timestamps by yaml.v3; the parser
	// must normalize them back to YYYY-MM-DD.
	if a.ChangeLog[0].Date != "2026-08-05" {
		t.Errorf("entry date = %q, want 2026-08-05", a.ChangeLog[0].Date)
	}
	if a.Created != "2026-08-05" || a.Updated != "2026-08-05" {
		t.Errorf("created/updated = %q/%q", a.Created, a.Updated)
	}
}

func TestAnalyzeMissingIdentityFields(t *testing.T) {
	content := strings.Replace(validADRFrontmatter, "namespace: eka-test\n", "", 1)
	_, results := analyzeFile("x.md", "/x/x.md", []byte(content))
	if !hasResult(results, RuleStructural, "namespace") {
		t.Errorf("missing namespace not reported: %v", results)
	}
}

func TestAnalyzeNonIntVersion(t *testing.T) {
	content := strings.Replace(validADRFrontmatter, "instance-version: 1", `instance-version: "1"`, 1)
	_, results := analyzeFile("x.md", "/x/x.md", []byte(content))
	if !hasResult(results, RuleStructural, "instance-version") {
		t.Errorf("quoted instance-version not reported: %v", results)
	}
}

func TestAnalyzeInvalidDate(t *testing.T) {
	content := strings.Replace(validADRFrontmatter, "created: 2026-08-05", "created: 2026-13-99", 1)
	_, results := analyzeFile("x.md", "/x/x.md", []byte(content))
	if !hasResult(results, RuleStructural, "created") {
		t.Errorf("invalid created date not reported: %v", results)
	}
}

func TestAnalyzeUnknownType(t *testing.T) {
	content := strings.Replace(validADRFrontmatter, "type: adr", "type: mystery", 1)
	_, results := analyzeFile("x.md", "/x/x.md", []byte(content))
	if !hasResult(results, RuleStructural, "unknown artifact type") {
		t.Errorf("unknown type not reported: %v", results)
	}
}

func TestAnalyzeChangeLogNotList(t *testing.T) {
	content := strings.Replace(validADRFrontmatter, "change-log:\n  - date:", "change-log: nope\n  - date:", 1)
	// The replacement leaves YAML broken in a different way; build a clean
	// variant instead.
	content = strings.Replace(validADRFrontmatter, "change-log:\n  - date: 2026-08-05\n    domain: existence-state\n    from: \"-\"\n    to: active\n    by: Engineering\n  - date: 2026-08-05\n    domain: content-state\n    from: proposed\n    to: accepted\n    by: Engineering", "change-log: nope", 1)
	_, results := analyzeFile("x.md", "/x/x.md", []byte(content))
	if !hasResult(results, Rule7, "change-log") {
		t.Errorf("non-list change-log not reported: %v", results)
	}
}

func TestAnalyzeMalformedChangeLogEntry(t *testing.T) {
	content := strings.Replace(validADRFrontmatter, "    by: Engineering\n  - date: 2026-08-05\n    domain: content-state", "  - date: 2026-08-05\n    domain: content-state", 1)
	_, results := analyzeFile("x.md", "/x/x.md", []byte(content))
	if !hasResult(results, Rule7, "malformed") {
		t.Errorf("malformed change-log entry not reported: %v", results)
	}
}

func hasResult(results []Result, rule, substring string) bool {
	for _, r := range results {
		if r.Rule == rule && strings.Contains(r.Message, substring) {
			return true
		}
	}
	return false
}
