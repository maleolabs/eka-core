package conformance

import (
	"fmt"
	"strings"
	"testing"
)

// Fine-grained tests for the Engineering Domain rules (R10-R12) using the
// same temp-dir fixture builder as the R2-R9 unit tests.

// buildGeneric builds a valid knowledge artifact of a Purpose/Content
// family type (vis/str/req/arc/spec/std/run/rel/gls) with the given
// dimension, content state, optional declared `domain` and custom
// relationship block. relations=="" yields empty relationship lists.
func buildGeneric(ns, typ, id, dimension, contentState, declaredDomain, relations string) string {
	if relations == "" {
		relations = "supersedes: []\nderives-from: []\ndepends-on: []\n"
	}
	domainLine := ""
	if declaredDomain != "" {
		domainLine = "domain: " + declaredDomain + "\n"
	}
	return fmt.Sprintf(`---
namespace: %s
type: %s
id: %s
instance-version: 1
revision: 1
content-state: %s
existence-state: active
dimension: %s
%screated: 2026-08-05
updated: 2026-08-05
%schange-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
  - date: 2026-08-05
    domain: content-state
    from: "-"
    to: %s
    by: Engineering
---
# %s %s

## Purpose

p

## Content

c
`, ns, typ, id, contentState, dimension, domainLine, relations, contentState, typ, id)
}

// buildSES builds a valid session artifact (token-exempt under R10).
func buildSES(ns, id string) string {
	return fmt.Sprintf(`---
namespace: %s
type: ses
id: %s
instance-version: 1
revision: 1
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
---
# Session %s

## Context

c

## Notes

n

## Verification

v
`, ns, id, id)
}

// buildADRWithDomain wraps buildADR and injects a declared `domain`
// frontmatter line.
func buildADRWithDomain(ns, id, contentState, declaredDomain, relations string) string {
	s := buildADR(ns, id, contentState, relations, "")
	if declaredDomain == "" {
		return s
	}
	return strings.Replace(s, "created: 2026-08-05",
		"domain: "+declaredDomain+"\ncreated: 2026-08-05", 1)
}

// TestRule11DomainCoherence covers the four R11 verdicts: absent
// (derived, pass), declared-and-matching (pass), unknown domain (error),
// mismatch (error).
func TestRule11DomainCoherence(t *testing.T) {
	cases := []struct {
		name     string
		domain   string
		wantErrs int
		wantSub  string
	}{
		{"absent", "", 0, ""},
		{"valid matching", "Architecture", 0, ""},
		{"unknown domain", "Bogus", 1, "unknown engineering domain"},
		{"mismatch", "Planning", 1, "does not match the home domain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := validateTempRepo(t, map[string]string{
				"docs/decisions/adr-001-x.md": buildADRWithDomain("eka", "001-x", "accepted", c.domain, ""),
			})
			if n := countResults(report, Rule11, SeverityError); n != c.wantErrs {
				t.Errorf("R11 errors = %d, want %d:\n%s", n, c.wantErrs, dumpResults(report))
			}
			if c.wantSub != "" {
				for _, r := range resultsFor(report, Rule11) {
					if !strings.Contains(r.Message, c.wantSub) {
						t.Errorf("R11 message %q must contain %q", r.Message, c.wantSub)
					}
				}
			}
			if report.ErrorCount() != c.wantErrs {
				t.Errorf("total errors = %d, want %d (only R11 may fire):\n%s",
					report.ErrorCount(), c.wantErrs, dumpResults(report))
			}
		})
	}
}

// TestRule11NonStringDomainIsStructural: a non-string `domain` value is
// a frontmatter structural error (R0), not an R11 finding.
func TestRule11NonStringDomainIsStructural(t *testing.T) {
	report := validateTempRepo(t, map[string]string{
		"docs/decisions/adr-001-x.md": strings.Replace(
			buildADR("eka", "001-x", "accepted", "", ""),
			"created: 2026-08-05", "domain: [a, b]\ncreated: 2026-08-05", 1),
	})
	if n := countResults(report, RuleStructural, SeverityError); n != 1 {
		t.Errorf("R0 errors = %d, want 1:\n%s", n, dumpResults(report))
	}
	if n := countResults(report, Rule11, SeverityError); n != 0 {
		t.Errorf("R11 errors = %d, want 0:\n%s", n, dumpResults(report))
	}
}

// TestRule12SupersessionProhibition covers the R12 verdicts: same-stratum
// supersede passes, upward supersede/amends errors, unresolvable targets
// are left to R5, and downward (lower-stratum) targets pass.
func TestRule12SupersessionProhibition(t *testing.T) {
	adr := buildADR("eka", "001-a", "accepted", "", "")
	planBase := buildPlan("eka", "x", 1, "")
	planY := buildPlan("eka", "y", 1, "")

	cases := []struct {
		name     string
		files    map[string]string
		wantErrs int
		wantSub  string
	}{
		{
			name: "same stratum supersede passes",
			files: map[string]string{
				"docs/planning/plan-x-v1.md": planBase,
				"docs/planning/plan-y-v1.md": strings.Replace(planY,
					"supersedes: []", "supersedes:\n  - plan:x", 1),
			},
			wantErrs: 0,
		},
		{
			name: "upward supersede errors",
			files: map[string]string{
				"docs/decisions/adr-001-a.md": adr,
				"docs/planning/plan-x-v1.md": strings.Replace(planBase,
					"supersedes: []", "supersedes:\n  - adr:001-a", 1),
			},
			wantErrs: 1,
			wantSub:  "strictly higher",
		},
		{
			name: "upward amends errors",
			files: map[string]string{
				"docs/decisions/adr-001-a.md": adr,
				"docs/planning/plan-x-v1.md": strings.Replace(planBase,
					"supersedes: []", "amends:\n  - adr:001-a\nsupersedes: []", 1),
			},
			wantErrs: 1,
			wantSub:  "amends",
		},
		{
			name: "unresolvable target no R12 finding",
			files: map[string]string{
				"docs/planning/plan-x-v1.md": strings.Replace(planBase,
					"supersedes: []", "supersedes:\n  - adr:ghost", 1),
			},
			wantErrs: 0, // R5 reports the dangling reference; R12 stays silent.
		},
		{
			name: "downward supersede passes",
			files: map[string]string{
				"docs/planning/plan-x-v1.md": planBase,
				"docs/decisions/adr-002-b.md": buildADR("eka", "002-b", "accepted",
					"supersedes:\n  - plan:x\nderives-from: []\ndepends-on: []\n", ""),
			},
			wantErrs: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := validateTempRepo(t, c.files)
			if n := countResults(report, Rule12, SeverityError); n != c.wantErrs {
				t.Errorf("R12 errors = %d, want %d:\n%s", n, c.wantErrs, dumpResults(report))
			}
			if c.wantSub != "" {
				for _, r := range resultsFor(report, Rule12) {
					if !strings.Contains(r.Message, c.wantSub) {
						t.Errorf("R12 message %q must contain %q", r.Message, c.wantSub)
					}
				}
			}
		})
	}
}

// TestRule10Stratification covers the R10 verdicts: Discovery needs no
// chain; upward chains pass (direct and transitive); isolated non-draft
// artifacts warn; tkt-/ses- and drafts are exempt.
func TestRule10Stratification(t *testing.T) {
	req := buildGeneric("eka", "req", "001-auth", "requirements", "approved", "", "")
	plan := buildPlan("eka", "x", 1, "")
	sto := buildSTO("eka", "s", "todo", `  - date: 2026-08-05
    domain: execution-state
    from: "-"
    to: planned
    by: Engineering
  - date: 2026-08-05
    domain: execution-state
    from: planned
    to: todo
    by: Engineering
`)

	cases := []struct {
		name     string
		files    map[string]string
		wantWarn int
	}{
		{
			name: "discovery needs no chain",
			files: map[string]string{
				"docs/requirements/req-001-auth.md": req,
			},
			wantWarn: 0,
		},
		{
			name: "direct upward chain passes",
			files: map[string]string{
				"docs/requirements/req-001-auth.md": req,
				"docs/planning/plan-x-v1.md": strings.Replace(plan,
					"derives-from: []", "derives-from:\n  - req:001-auth", 1),
			},
			wantWarn: 0,
		},
		{
			name: "transitive chain passes",
			files: map[string]string{
				"docs/requirements/req-001-auth.md": req,
				"docs/planning/plan-x-v1.md": strings.Replace(plan,
					"derives-from: []", "derives-from:\n  - req:001-auth", 1),
				"docs/operating/containers/ctr-c1.md": strings.Replace(buildCTR("eka", "c1", ""),
					"derives-from: []", "derives-from:\n  - plan:x", 1),
				"docs/operating/work-items/stories/sto-s.md": strings.Replace(sto,
					"derives-from: []", "derives-from:\n  - ctr:c1", 1),
			},
			wantWarn: 0,
		},
		{
			name: "isolated non-draft artifact warns",
			files: map[string]string{
				"docs/decisions/adr-001-a.md": buildADR("eka", "001-a", "accepted", "", ""),
			},
			wantWarn: 1,
		},
		{
			name: "isolated work item warns",
			files: map[string]string{
				"docs/operating/work-items/stories/sto-s.md": sto,
			},
			wantWarn: 1,
		},
		{
			name: "ticket token exempt",
			files: map[string]string{
				"docs/requirements/req-001-auth.md": req,
				"docs/planning/plan-x-v1.md": strings.Replace(plan,
					"derives-from: []", "derives-from:\n  - req:001-auth", 1),
				"docs/operating/containers/ctr-c1.md": strings.Replace(buildCTR("eka", "c1", ""),
					"derives-from: []", "derives-from:\n  - plan:x", 1),
				"docs/operating/projections/tkt-t.md":        buildTKT("eka", "t", hdrLine+"\n", "  - ctr:c1\n"),
				"docs/operating/work-items/stories/sto-s.md": sto,
			},
			wantWarn: 1, // only the isolated sto warns; the ticket is exempt.
		},
		{
			name: "session token exempt",
			files: map[string]string{
				"docs/operating/sessions/ses-1.md": buildSES("eka", "s-1"),
			},
			wantWarn: 0,
		},
		{
			name: "draft content state exempt",
			files: map[string]string{
				"docs/specifications/spec-001-d.md": buildGeneric("eka", "spec", "001-d", "specifications", "draft", "", ""),
			},
			wantWarn: 0,
		},
		{
			name: "same stratum chain still warns",
			files: map[string]string{
				"docs/decisions/adr-001-a.md": buildADR("eka", "001-a", "accepted", "", ""),
				"docs/decisions/adr-002-b.md": buildADR("eka", "002-b", "accepted",
					"supersedes: []\nderives-from: []\ndepends-on:\n  - adr:001-a\n", ""),
			},
			wantWarn: 2, // both ADRs: the chain stays inside Architecture.
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := validateTempRepo(t, c.files)
			if n := countResults(report, Rule10, SeverityWarning); n != c.wantWarn {
				t.Errorf("R10 warnings = %d, want %d:\n%s", n, c.wantWarn, dumpResults(report))
			}
			if report.ErrorCount() != 0 {
				t.Errorf("fixture must not produce blocking errors:\n%s", dumpResults(report))
			}
		})
	}
}
