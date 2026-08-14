package conformance

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Fine-grained per-rule tests using a temp-dir fixture builder, so each
// scenario asserts exact result counts.

const emDash = "\u2014"
const hdrLine = "> Generated " + emDash + " State Projection. Do NOT edit state here; refresh on read."

func validateTempRepo(t *testing.T, files map[string]string) *Report {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, files)
	report, err := Validate(root)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return report
}

func countResults(report *Report, rule string, sev Severity) int {
	n := 0
	for _, r := range report.Results {
		if r.Rule == rule && r.Severity == sev {
			n++
		}
	}
	return n
}

func resultsFor(report *Report, rule string) []Result {
	var out []Result
	for _, r := range report.Results {
		if r.Rule == rule {
			out = append(out, r)
		}
	}
	return out
}

// --- minimal valid artifact builders ---

const adrBody = `
## Context

ctx

## Decision

dec

## Consequences

cons

## Alternatives Considered

alt
`

func buildADR(ns, id, contentState string, relations, extraChangeLog string) string {
	if relations == "" {
		relations = "supersedes: []\nderives-from: []\ndepends-on: []\n"
	}
	return fmt.Sprintf(`---
namespace: %s
type: adr
id: %s
instance-version: 1
revision: 1
content-state: %s
existence-state: active
dimension: decisions
created: 2026-08-05
updated: 2026-08-05
%schange-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
  - date: 2026-08-05
    domain: content-state
    from: proposed
    to: %s
    by: Engineering
%s---
# ADR %s
%s`, ns, id, contentState, relations, contentState, extraChangeLog, id, adrBody)
}

// TestRule2ExactCounts verifies each filename violation fires exactly once.
func TestRule2ExactCounts(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "token mismatch",
			files: map[string]string{
				"docs/decisions/arc-001-x.md": buildADR("eka", "001-x", "accepted", "", ""),
			},
		},
		{
			name: "forbidden version suffix",
			files: map[string]string{
				"docs/decisions/adr-001-x-v1.md": buildADR("eka", "001-x", "accepted", "", ""),
			},
		},
		{
			name: "version mismatch",
			files: map[string]string{
				"docs/planning/plan-x-v2.md": buildPlan("eka", "x", 1, ""),
			},
		},
		{
			name: "missing version on plan",
			files: map[string]string{
				"docs/planning/plan-x.md": buildPlan("eka", "x", 1, ""),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := validateTempRepo(t, c.files)
			if n := countResults(report, Rule2, SeverityError); n != 1 {
				t.Errorf("R2 errors = %d, want 1:\n%s", n, dumpResults(report))
			}
		})
	}
}

// buildPlan builds a valid plan artifact (dimension planning, phase discovery).
func buildPlan(ns, id string, version int, extra string) string {
	return fmt.Sprintf(`---
namespace: %s
type: plan
id: %s
instance-version: %d
revision: 1
content-state: draft
planning-state: draft
existence-state: active
phase: discovery
dimension: planning
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on: []
%schange-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
  - date: 2026-08-05
    domain: content-state
    from: "-"
    to: draft
    by: Engineering
  - date: 2026-08-05
    domain: planning-state
    from: "-"
    to: draft
    by: Engineering
  - date: 2026-08-05
    domain: phase
    from: "-"
    to: discovery
    by: Engineering
---
# Plan %s

## Objective

obj

## Scope

scope

## Out of Scope

oos
`, ns, id, version, extra, id)
}

// TestRule5DraftSeverity verifies the draft exception: unresolved references
// on draft artifacts are warnings, on non-draft artifacts errors.
func TestRule5DraftSeverity(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		"docs/specifications/spec-001-draft.md": fmt.Sprintf(`---
namespace: eka
type: spec
id: 001-draft
instance-version: 1
revision: 1
content-state: draft
existence-state: active
dimension: specifications
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on:
  - sto:ghost
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
  - date: 2026-08-05
    domain: content-state
    from: "-"
    to: draft
    by: Engineering
---
# Spec

## Purpose

p

## Content

c
`),
	})
	if n := countResults(report, Rule5, SeverityWarning); n != 1 {
		t.Errorf("R5 warnings = %d, want 1:\n%s", n, dumpResults(report))
	}
	if n := countResults(report, Rule5, SeverityError); n != 0 {
		t.Errorf("R5 errors = %d, want 0:\n%s", n, dumpResults(report))
	}
}

// TestRule5CrossNamespaceAndVersionResolution exercises resolution of
// cross-namespace references and versioned references.
func TestRule5CrossNamespaceAndVersionResolution(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		"docs/decisions/adr-001-a.md": buildADR("ns-a", "001-a", "accepted", "", ""),
		"docs/decisions/adr-002-b.md": fmt.Sprintf(`---
namespace: ns-b
type: adr
id: 002-b
instance-version: 1
revision: 1
content-state: accepted
existence-state: active
dimension: decisions
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on:
  - ns-a/adr:001-a
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
# ADR

%s`, adrBody),
		// Versioned reference to a specific instance.
		"docs/planning/plan-x-v1.md": buildPlan("ns-a", "x", 1, ""),
		"docs/planning/plan-x-v2.md": buildPlan("ns-a", "x", 2, ""),
		"docs/decisions/adr-003-c.md": buildADR("ns-a", "003-c", "accepted",
			"supersedes: []\nderives-from: []\ndepends-on:\n  - plan:x:2\n", ""),
	})
	if n := countResults(report, Rule5, SeverityError); n != 0 {
		t.Errorf("R5 errors = %d, want 0:\n%s", n, dumpResults(report))
	}
}

// TestRule5VersionedReferenceToMissingInstance verifies that a versioned
// reference to a non-existent instance does not resolve.
func TestRule5VersionedReferenceToMissingInstance(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		"docs/planning/plan-x-v1.md": buildPlan("eka", "x", 1, ""),
		"docs/decisions/adr-001-a.md": buildADR("eka", "001-a", "accepted",
			"supersedes: []\nderives-from: []\ndepends-on:\n  - plan:x:2\n", ""),
	})
	if n := countResults(report, Rule5, SeverityError); n != 1 {
		t.Errorf("R5 errors = %d, want 1:\n%s", n, dumpResults(report))
	}
}

// TestRule7ExactCounts verifies each change-log violation fires exactly once.
func TestRule7ExactCounts(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "missing change-log on state owner",
			files: map[string]string{
				"docs/decisions/adr-001-x.md": strings.Replace(
					buildADR("eka", "001-x", "accepted", "", ""),
					"change-log:", "no-log:", 1),
			},
		},
		{
			name: "last entry mismatch",
			files: map[string]string{
				"docs/decisions/adr-001-x.md": func() string {
					s := buildADR("eka", "001-x", "accepted", "", "")
					s = strings.Replace(s, "    from: proposed", "    from: \"-\"", 1)
					return strings.Replace(s, "    to: accepted", "    to: proposed", 1)
				}(),
			},
		},
		{
			name: "execution skip",
			files: map[string]string{
				"docs/operating/work-items/stories/sto-x.md": buildSTO("eka", "x", "in-progress", `  - date: 2026-08-05
    domain: execution-state
    from: "-"
    to: planned
    by: Engineering
  - date: 2026-08-05
    domain: execution-state
    from: planned
    to: in-progress
    by: Engineering
`),
			},
		},
		{
			name: "foreign domain entry",
			files: map[string]string{
				"docs/decisions/adr-001-x.md": strings.Replace(
					buildADR("eka", "001-x", "accepted", "", ""),
					"change-log:", `change-log:
  - date: 2026-08-05
    domain: execution-state
    from: planned
    to: todo
    by: Engineering
`, 1),
			},
		},
		{
			name: "ticket needs no change-log",
			files: map[string]string{
				"docs/operating/projections/tkt-x.md": buildTKT("eka", "x", "", ""),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := validateTempRepo(t, c.files)
			switch c.name {
			case "ticket needs no change-log":
				if n := countResults(report, Rule7, SeverityError); n != 0 {
					t.Errorf("R7 errors = %d, want 0 (ticket owns no domain):\n%s", n, dumpResults(report))
				}
			default:
				if n := countResults(report, Rule7, SeverityError); n != 1 {
					t.Errorf("R7 errors = %d, want 1:\n%s", n, dumpResults(report))
				}
			}
		})
	}
}

// TestRule7AllowedValueEnumeration verifies that R7 `from`/`to` value
// errors enumerate the domain's allowed values, mirroring the R3
// `(allowed: ...)` diagnostic — the user must not guess valid values.
func TestRule7AllowedValueEnumeration(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		wantSub string
	}{
		{
			name: "adr content-state from variant",
			files: map[string]string{
				"docs/decisions/adr-001-x.md": strings.Replace(
					buildADR("eka", "001-x", "accepted", "", ""),
					"    from: proposed", "    from: bogus", 1),
			},
			wantSub: "change-log entry 2: `from` value \"bogus\" is not valid for domain \"content-state\" (allowed: proposed, accepted, superseded)",
		},
		{
			name: "adr content-state to variant",
			files: map[string]string{
				"docs/decisions/adr-001-x.md": strings.Replace(
					buildADR("eka", "001-x", "accepted", "", ""),
					"    to: accepted", "    to: bogus", 1),
			},
			wantSub: "change-log entry 2: `to` value \"bogus\" is not valid for domain \"content-state\" (allowed: proposed, accepted, superseded)",
		},
		{
			name: "sto execution-state from",
			files: map[string]string{
				"docs/operating/work-items/stories/sto-x.md": buildSTO("eka", "x", "in-progress", `  - date: 2026-08-05
    domain: execution-state
    from: bogus
    to: in-progress
    by: Engineering
`),
			},
			wantSub: "`from` value \"bogus\" is not valid for domain \"execution-state\" (allowed: planned, todo, in-progress, in-review, done, canceled)",
		},
		{
			name: "plan phase from",
			files: map[string]string{
				"docs/planning/plan-x-v1.md": strings.Replace(
					buildPlan("eka", "x", 1, ""),
					"    from: \"-\"\n    to: discovery", "    from: bogus\n    to: discovery", 1),
			},
			wantSub: "`from` value \"bogus\" is not valid for domain \"phase\" (allowed: discovery, mvp, milestone, release, growth, maturity, sunset)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := validateTempRepo(t, c.files)
			var msgs []string
			for _, r := range report.Results {
				if r.Rule == Rule7 && strings.Contains(r.Message, "is not valid for domain") {
					msgs = append(msgs, r.Message)
				}
			}
			if len(msgs) != 1 {
				t.Fatalf("R7 value errors = %d, want exactly 1:\n%s", len(msgs), dumpResults(report))
			}
			if !strings.Contains(msgs[0], "allowed:") {
				t.Errorf("R7 message %q must enumerate allowed values (allowed: ...)", msgs[0])
			}
			if !strings.Contains(msgs[0], c.wantSub) {
				t.Errorf("R7 message %q must contain %q", msgs[0], c.wantSub)
			}
		})
	}
}

// buildSTO builds a valid story with the given execution state and custom
// execution-state change-log entries (the caller supplies the entries).
func buildSTO(ns, id, execState, execEntries string) string {
	return fmt.Sprintf(`---
namespace: %s
type: sto
id: %s
instance-version: 1
revision: 1
execution-state: %s
existence-state: active
dimension: requirements
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on: []
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
%s---
# Story %s

## Description

desc

## Acceptance Criteria

- ac
`, ns, id, execState, execEntries, id)
}

// buildTKT builds a ticket; pass header=="" to omit the projection header
// and derivesFrom=="" to omit derives-from.
func buildTKT(ns, id, header, derivesFrom string) string {
	return fmt.Sprintf(`---
namespace: %s
type: tkt
id: %s
instance-version: 1
revision: 1
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from:
%sdepends-on: []
---

%s# Ticket %s

## Commands

- run tests.

## Projected Status

| Field | Value |
|---|---|
| execution-state | todo |
`, ns, id, derivesFrom, header, id)
}

// TestRule8TicketHeaderAndDerivation verifies tkt- header/derivation
// requirements with exact counts.
func TestRule8TicketHeaderAndDerivation(t *testing.T) {
	cases := []struct {
		name   string
		header string
		derive string
	}{
		{"valid", hdrLine + "\n", "  - ctr:wave-1\n"},
		{"missing header", "", "  - ctr:wave-1\n"},
		{"missing container derivation", hdrLine + "\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{
				"docs/operating/containers/ctr-wave-1.md": buildCTR("eka", "wave-1", ""),
				"docs/operating/projections/tkt-x.md":     buildTKT("eka", "x", c.header, c.derive),
			}
			report := validateTempRepo(t, files)
			want := 0
			if c.name == "missing header" || c.name == "missing container derivation" {
				want = 1
			}
			if n := countResults(report, Rule8, SeverityError); n != want {
				t.Errorf("R8 errors = %d, want %d:\n%s", n, want, dumpResults(report))
			}
		})
	}
}

// buildCTR builds a valid container; pass table=="" for no Work Items table.
func buildCTR(ns, id, table string) string {
	if table == "" {
		table = "none yet."
	} else {
		table = "\n" + table
	}
	return fmt.Sprintf(`---
namespace: %s
type: ctr
id: %s
instance-version: 1
revision: 1
container-state: active
existence-state: active
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on: []
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
  - date: 2026-08-05
    domain: container-state
    from: "-"
    to: active
    by: Engineering
---
# Container %s

## Objective

obj

## Work Items

%s

## Change Log

- opened.
`, ns, id, id, table)
}

// TestRule8ContainerTableMismatchIsWarningOnly verifies the mismatch
// severity contract (owner state is truth; mismatch = warning).
func TestRule8ContainerTableMismatchIsWarningOnly(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		"docs/operating/work-items/stories/sto-a.md": buildSTO("eka", "a", "done", `  - date: 2026-08-05
    domain: execution-state
    from: "-"
    to: planned
    by: Engineering
  - date: 2026-08-05
    domain: execution-state
    from: planned
    to: todo
    by: Engineering
  - date: 2026-08-05
    domain: execution-state
    from: todo
    to: in-progress
    by: Engineering
  - date: 2026-08-05
    domain: execution-state
    from: in-progress
    to: in-review
    by: Engineering
  - date: 2026-08-05
    domain: execution-state
    from: in-review
    to: done
    by: Engineering
`),
		"docs/operating/containers/ctr-c1.md": buildCTR("eka", "c1", hdrLine+"\n\n| Work Item | Ringkasan | Execution State |\n|---|---|---|\n| sto:a | a | planned |\n"),
	})
	if n := countResults(report, Rule8, SeverityWarning); n != 1 {
		t.Errorf("R8 warnings = %d, want 1:\n%s", n, dumpResults(report))
	}
	if n := countResults(report, Rule8, SeverityError); n != 0 {
		t.Errorf("R8 errors = %d, want 0 (mismatch is a warning):\n%s", n, dumpResults(report))
	}
	if !report.Pass() {
		t.Error("a container table mismatch must not block the repository")
	}
}

// TestRule9SupersededADRWithReplacement verifies the positive case: a
// superseded ADR referenced by a replacement passes.
func TestRule9SupersededADRWithReplacement(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		"docs/decisions/adr-001-old.md": buildADR("eka", "001-old", "superseded",
			"", `  - date: 2026-08-05
    domain: content-state
    from: accepted
    to: superseded
    by: Engineering
`),
		"docs/decisions/adr-002-new.md": buildADR("eka", "002-new", "accepted",
			"supersedes:\n  - adr:001-old\nderives-from: []\ndepends-on: []\n", ""),
	})
	if n := countResults(report, Rule9, SeverityError); n != 0 {
		t.Errorf("R9 errors = %d, want 0:\n%s", n, dumpResults(report))
	}
}

// TestRule9VersionedReplacementMustNameInstance: a supersedes reference with
// a wrong instance-version does not count as a replacement.
func TestRule9VersionedReplacementMustNameInstance(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		"docs/decisions/adr-001-old.md": buildADR("eka", "001-old", "superseded",
			"", `  - date: 2026-08-05
    domain: content-state
    from: accepted
    to: superseded
    by: Engineering
`),
		"docs/decisions/adr-002-new.md": buildADR("eka", "002-new", "accepted",
			"supersedes:\n  - adr:001-old:2\nderives-from: []\ndepends-on: []\n", ""),
	})
	if n := countResults(report, Rule9, SeverityError); n != 1 {
		t.Errorf("R9 errors = %d, want 1:\n%s", n, dumpResults(report))
	}
}

// TestDimensionFolderResolution verifies the nearest-ancestor rule for
// knowledge artifacts living below a dimension folder or in a non-docs zone.
func TestDimensionFolderResolution(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		// Nested below the dimension folder: still `decisions`.
		"docs/decisions/2026/adr-001-x.md": buildADR("eka", "001-x", "accepted", "", ""),
		// Outside the skeleton layout, like reference/decisions/ in the
		// canonical repo: nearest ancestor is still `decisions`.
		"reference/decisions/adr-002-y.md": buildADR("eka", "002-y", "accepted", "", ""),
	})
	if n := countResults(report, Rule6, SeverityError); n != 0 {
		t.Errorf("R6 errors = %d, want 0:\n%s", n, dumpResults(report))
	}
}

// TestPhaseValidation verifies phase placement and values.
func TestPhaseValidation(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		// Valid phase on a plan.
		"docs/planning/plan-x-v1.md": buildPlan("eka", "x", 1, ""),
		// Invalid phase value on a plan.
		"docs/planning/plan-y-v1.md": strings.Replace(buildPlan("eka", "y", 1, ""), "phase: discovery", "phase: someday", 1),
	})
	if n := countResults(report, Rule3, SeverityError); n != 1 {
		t.Errorf("R3 errors = %d, want 1 (invalid phase value):\n%s", n, dumpResults(report))
	}
}

// TestUnknownTypeIsStructural verifies unknown type tokens are R0 errors and
// no numbered rules run on them.
func TestUnknownTypeIsStructural(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		"docs/decisions/mystery-abc.md": `---
namespace: eka
type: mystery
id: abc
instance-version: 1
revision: 1
---
# x
`,
	})
	if n := countResults(report, RuleStructural, SeverityError); n != 1 {
		t.Errorf("R0 errors = %d, want 1:\n%s", n, dumpResults(report))
	}
	if n := countResults(report, Rule2, SeverityError); n != 0 {
		t.Errorf("R2 errors = %d, want 0 (no filename rule on unknown types):\n%s", n, dumpResults(report))
	}
}

// TestReportCounts verifies FilesScanned/Artifacts accounting.
func TestReportCounts(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"README.md":                   "# r\n",
		"docs/decisions/adr-001-x.md": buildADR("eka", "001-x", "accepted", "", ""),
		"notes.txt":                   "not markdown\n",
		"docs/decisions/README.md":    "# decisions\n",
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesScanned != 2 {
		t.Errorf("FilesScanned = %d, want 2 (docs tree only; the root README is not scanned)", report.FilesScanned)
	}
	if report.Artifacts != 1 {
		t.Errorf("Artifacts = %d, want 1", report.Artifacts)
	}
}

// TestReportPassSemantics verifies warnings do not affect Pass.
func TestReportPassSemantics(t *testing.T) {
	report := &Report{Results: []Result{
		{File: "a.md", Rule: Rule5, Severity: SeverityWarning, Message: "w"},
	}}
	if !report.Pass() {
		t.Error("warnings-only report must pass")
	}
	report.Results = append(report.Results, Result{File: "a.md", Rule: Rule5, Severity: SeverityError, Message: "e"})
	if report.Pass() {
		t.Error("report with an error must not pass")
	}
}

// TestRelPathIsRootRelative ensures results use root-relative paths.
func TestRelPathIsRootRelative(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		"docs/decisions/adr-001-x.md": buildADR("eka", "001-x", "accepted",
			"supersedes: []\nderives-from: []\ndepends-on:\n  - sto:ghost\n", ""),
	})
	for _, r := range report.Results {
		if filepath.IsAbs(r.File) || strings.HasPrefix(r.File, "..") {
			t.Errorf("result path %q is not root-relative", r.File)
		}
	}
}
