package conformance

import (
	"path/filepath"
	"strings"
	"testing"
)

// Tests of the v2.0 JSON-native authoring adapter (spec-standard-v2
// §3, §7): the JSON schema classifies into the same Artifact model, the
// rule engine evaluates it (R9 as a content-key check), and the legacy
// Markdown path stays byte-identical.

// TestSectionKey: the deterministic camelCase derivation of §2.
func TestSectionKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Alternatives Considered", "alternativesConsidered"},
		{"Acceptance Criteria", "acceptanceCriteria"},
		{"Out of Scope", "outOfScope"},
		{"Work Items", "workItems"},
		{"Projected Status", "projectedStatus"},
		{"Change Log", "changeLog"},
		{"Investigation Summary", "investigationSummary"},
		{"Investigation Notes", "investigationNotes"},
		{"Debt Rationale", "debtRationale"},
		{"Action Items", "actionItems"},
		{"Context", "context"},
		{"Objective", "objective"},
		{"Scope", "scope"},
		{"Content", "content"},
		{"Purpose", "purpose"},
		{"Impact", "impact"},
		{"Description", "description"},
		{"Conclusion", "conclusion"},
		{"Findings", "findings"},
		{"Commands", "commands"},
		{"Notes", "notes"},
		{"Verification", "verification"},
		{"Decision", "decision"},
		{"Consequences", "consequences"},
	}
	for _, tc := range cases {
		if got := SectionKey(tc.in); got != tc.want {
			t.Errorf("SectionKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestJSONValidFixtureRepo: the JSON-native fixture validates clean —
// same rule engine, JSON adapter. The ticket exercises the JSON
// projection-header and derives-from paths; the container exercises the
// JSON Work Items table comparison.
func TestJSONValidFixtureRepo(t *testing.T) {
	report, err := Validate(filepath.Join(testdataDir, "json-valid"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if report.FilesScanned != 3 {
		t.Errorf("files scanned = %d, want 3", report.FilesScanned)
	}
	if report.Artifacts != 3 {
		t.Errorf("artifacts = %d, want 3", report.Artifacts)
	}
	if report.ErrorCount() != 0 {
		t.Errorf("errors = %d, want 0:\n%s", report.ErrorCount(), dumpResults(report))
	}
	if !report.Pass() {
		t.Error("json-valid fixture must pass")
	}
}

// TestJSONArtifactModel: the JSON adapter populates the same Artifact
// model (kebab state/relationship domains internally, structured
// content object).
func TestJSONArtifactModel(t *testing.T) {
	artifacts, err := Scan(filepath.Join(testdataDir, "json-valid"))
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 3 {
		t.Fatalf("artifacts = %d, want 3", len(artifacts))
	}
	var adr *Artifact
	for i := range artifacts {
		if artifacts[i].Type == "adr" {
			adr = &artifacts[i]
		}
	}
	if adr == nil {
		t.Fatal("no adr artifact")
	}
	if adr.Namespace != "eka-valid-fixture" || adr.ID != "001-login-serialization" || adr.InstanceVersion != 1 || adr.Revision != 1 {
		t.Errorf("identity = %+v", adr)
	}
	if adr.States[DomainContentState] != "accepted" || adr.States[DomainExistenceState] != "active" {
		t.Errorf("states = %v", adr.States)
	}
	if !adr.HasDimension || adr.Dimension != "decisions" {
		t.Errorf("dimension = %q (has %v)", adr.Dimension, adr.HasDimension)
	}
	if len(adr.ChangeLog) != 2 || adr.ChangeLog[1].Domain != DomainContentState {
		t.Errorf("change log = %+v", adr.ChangeLog)
	}
	if adr.ContentFields == nil {
		t.Fatal("ContentFields must be set for JSON artifacts")
	}
	if adr.ContentFields["context"] != "Context body." {
		t.Errorf("content context = %v", adr.ContentFields["context"])
	}
	if _, ok := adr.ContentFields["alternativesConsidered"].(map[string]any); !ok {
		t.Errorf("nested content value lost: %v", adr.ContentFields["alternativesConsidered"])
	}
	if _, ok := adr.ContentFields["references"]; !ok {
		t.Error("extra content keys must be preserved")
	}
}

// TestJSONUnknownTopLevelField: strict schema — an unknown top-level
// field is an R0 structural finding.
func TestJSONUnknownTopLevelField(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"docs/decisions/adr-001-x.json": `{
			"namespace": "ns", "type": "adr", "id": "001-x", "instanceVersion": 1, "revision": 1,
			"state": {"contentState": "accepted", "existenceState": "active"},
			"dimension": "decisions",
			"bogus": 1,
			"changeLog": [
				{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-05", "domain": "contentState", "from": "proposed", "to": "accepted", "by": "T"}
			],
			"content": {"context": "C", "decision": "D", "consequences": "Co", "alternativesConsidered": "A"}
		}`,
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReportResult(report, RuleStructural, "unknown top-level field") {
		t.Errorf("missing unknown-field finding:\n%s", dumpResults(report))
	}
}

// TestJSONNonObjectContent: `content` must be an object.
func TestJSONNonObjectContent(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"docs/decisions/adr-001-x.json": `{
			"namespace": "ns", "type": "adr", "id": "001-x", "instanceVersion": 1, "revision": 1,
			"state": {"contentState": "accepted", "existenceState": "active"},
			"dimension": "decisions",
			"changeLog": [
				{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-05", "domain": "contentState", "from": "proposed", "to": "accepted", "by": "T"}
			],
			"content": "plain text is not an object"
		}`,
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReportResult(report, RuleStructural, "must be a JSON object") {
		t.Errorf("missing non-object-content finding:\n%s", dumpResults(report))
	}
}

// TestJSONMissingContentKey: R9 becomes a content-key check for JSON
// artifacts.
func TestJSONMissingContentKey(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"docs/decisions/adr-001-x.json": `{
			"namespace": "ns", "type": "adr", "id": "001-x", "instanceVersion": 1, "revision": 1,
			"state": {"contentState": "accepted", "existenceState": "active"},
			"dimension": "decisions",
			"changeLog": [
				{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-05", "domain": "contentState", "from": "proposed", "to": "accepted", "by": "T"}
			],
			"content": {"context": "C", "decision": "D", "consequences": "Co"}
		}`,
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReportResult(report, Rule9, "alternativesConsidered") {
		t.Errorf("missing R9 key finding:\n%s", dumpResults(report))
	}
}

// TestJSONInvalidJSON: unparseable JSON is an R0 structural finding.
func TestJSONInvalidJSON(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"docs/decisions/adr-001-x.json": `{ "namespace": `,
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReportResult(report, RuleStructural, "not valid JSON") {
		t.Errorf("missing invalid-JSON finding:\n%s", dumpResults(report))
	}
}

// TestJSONContentValueType: content values must be strings or nested
// objects.
func TestJSONContentValueType(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"docs/decisions/adr-001-x.json": `{
			"namespace": "ns", "type": "adr", "id": "001-x", "instanceVersion": 1, "revision": 1,
			"state": {"contentState": "accepted", "existenceState": "active"},
			"dimension": "decisions",
			"changeLog": [
				{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-05", "domain": "contentState", "from": "proposed", "to": "accepted", "by": "T"}
			],
			"content": {"context": "C", "decision": "D", "consequences": 42, "alternativesConsidered": "A"}
		}`,
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReportResult(report, RuleStructural, "must be a string or a nested object") {
		t.Errorf("missing content value type finding:\n%s", dumpResults(report))
	}
}

// TestJSONUnknownStateDomain: strict schema — an unknown state domain
// key is a structural finding (values are unchanged, lowercase).
func TestJSONUnknownStateDomain(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"docs/decisions/adr-001-x.json": `{
			"namespace": "ns", "type": "adr", "id": "001-x", "instanceVersion": 1, "revision": 1,
			"state": {"contentState": "accepted", "existenceState": "active", "bogusState": "x"},
			"dimension": "decisions",
			"changeLog": [
				{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-05", "domain": "contentState", "from": "proposed", "to": "accepted", "by": "T"}
			],
			"content": {"context": "C", "decision": "D", "consequences": "Co", "alternativesConsidered": "A"}
		}`,
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReportResult(report, RuleStructural, "unknown state domain") {
		t.Errorf("missing unknown-state-domain finding:\n%s", dumpResults(report))
	}
}

// TestMixedRepoMDAndJSON: a repository with both legacy Markdown and
// JSON-native artifacts validates (the migration-state repo shape).
func TestMixedRepoMDAndJSON(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"docs/decisions/adr-001-md.md": `---
namespace: mixed
type: adr
id: 001-md
instance-version: 1
revision: 1
content-state: accepted
existence-state: active
dimension: decisions
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: T
  - date: 2026-08-05
    domain: content-state
    from: proposed
    to: accepted
    by: T
---

# ADR-001

## Context

C

## Decision

D

## Consequences

Co

## Alternatives Considered

A
`,
		"docs/decisions/adr-002-json.json": `{
			"namespace": "mixed", "type": "adr", "id": "002-json", "instanceVersion": 1, "revision": 1,
			"state": {"contentState": "accepted", "existenceState": "active"},
			"dimension": "decisions",
			"changeLog": [
				{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-05", "domain": "contentState", "from": "proposed", "to": "accepted", "by": "T"}
			],
			"content": {"context": "C", "decision": "D", "consequences": "Co", "alternativesConsidered": "A"}
		}`,
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Artifacts != 2 || report.ErrorCount() != 0 {
		t.Errorf("mixed repo: artifacts = %d, errors = %d:\n%s", report.Artifacts, report.ErrorCount(), dumpResults(report))
	}
}

// TestJSONScanFileDraftTolerance: ScanFile tolerates the absent
// instance-version (drafts assign it at publish time) for JSON drafts.
func TestJSONScanFileDraftTolerance(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "adr-001-x.json")
	writeTree(t, root, map[string]string{
		"adr-001-x.json": `{
			"namespace": "ns", "type": "adr", "id": "001-x", "revision": 1,
			"state": {"contentState": "proposed", "existenceState": "active"},
			"dimension": "decisions",
			"changeLog": [
				{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "proposed", "by": "T"}
			],
			"content": {"context": "", "decision": "", "consequences": "", "alternativesConsidered": ""}
		}`,
	})
	a, err := ScanFile(path)
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if a == nil || a.Type != "adr" || a.ContentFields == nil {
		t.Fatalf("artifact = %+v", a)
	}
}

// hasResult reports whether the report carries a result with the given
// rule whose message contains the substring.
func hasReportResult(report *Report, rule, substring string) bool {
	for _, r := range report.Results {
		if r.Rule == rule && strings.Contains(r.Message, substring) {
			return true
		}
	}
	return false
}
