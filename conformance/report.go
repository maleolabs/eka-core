package conformance

import (
	"fmt"
	"sort"
)

// This file defines the validation result model shared by the engine and the
// CLI: Severity, Result, Report. Results are sorted deterministically
// (file, then rule, then severity, then message) so repeated runs produce
// byte-identical output.

// Severity of a single validation result.
type Severity string

const (
	// SeverityError is a blocking violation: the repository must be
	// fixed before commit (validation.md "Hasil": any 0 blocks commit).
	SeverityError Severity = "error"
	// SeverityWarning never blocks: the repository may be committed with
	// warnings noted (validation.md "Hasil": only W -> commit allowed).
	SeverityWarning Severity = "warning"
)

// Rule identifiers. R1..R13 are the conformance rules from
// skeleton/docs/exchange/validation.md (R1-R9), the Engineering
// Domain ontology of the EKA v1.1 standard (R10-R12) and the
// graph-aware transition gates of ADR-019 (R13).
const (
	// RuleStructural ("R0") is the structural bucket for failures that
	// precede the numbered rules: unparseable frontmatter, artifacts
	// violating the artifact rule (type XOR id), and missing or invalid
	// identity fields. It is not one of the numbered rules.
	RuleStructural = "R0"
	Rule1          = "R1"
	Rule2          = "R2"
	Rule3          = "R3"
	Rule4          = "R4"
	Rule5          = "R5"
	Rule6          = "R6"
	Rule7          = "R7"
	Rule8          = "R8"
	Rule9          = "R9"
	Rule10         = "R10"
	Rule11         = "R11"
	Rule12         = "R12"
	// Rule13 is the graph-aware pass (ADR-019 D6): the cmt- note
	// contract (role content schema) and the work-item transition gates
	// (in-review requires a resolved implementation note; done requires
	// every note resolved). It runs after the per-file pass.
	Rule13 = "R13"
)

// ruleRank maps a rule id to its sort position.
func ruleRank(rule string) int {
	switch rule {
	case RuleStructural:
		return 0
	case Rule1:
		return 1
	case Rule2:
		return 2
	case Rule3:
		return 3
	case Rule4:
		return 4
	case Rule5:
		return 5
	case Rule6:
		return 6
	case Rule7:
		return 7
	case Rule8:
		return 8
	case Rule9:
		return 9
	case Rule10:
		return 10
	case Rule11:
		return 11
	case Rule12:
		return 12
	case Rule13:
		return 13
	default:
		return 99
	}
}

// severityRank puts errors before warnings in sorted output.
func severityRank(s Severity) int {
	if s == SeverityError {
		return 0
	}
	return 1
}

// Result is a single validation finding.
type Result struct {
	// File is the path relative to the scanned root.
	File string
	// Rule is the rule id ("R0".."R12").
	Rule string
	// Severity is error or warning.
	Severity Severity
	// Message is a human-readable description.
	Message string
}

// String renders one result as a single deterministic line.
func (r Result) String() string {
	return fmt.Sprintf("[%s] %s %s: %s", r.Severity, r.Rule, r.File, r.Message)
}

// Report is the full outcome of one Validate run.
type Report struct {
	// Root is the scanned directory.
	Root string
	// FilesScanned is the number of authoring files examined (.md
	// legacy and .json v2.0-native) in the <root>/docs knowledge tree.
	FilesScanned int
	// Artifacts is the number of classified artifacts found.
	Artifacts int
	// Skipped is the deterministic skip reason when validation was
	// skipped because <root>/docs does not exist (ADR-018 Decision 2);
	// empty when a real scan ran. A skipped report is a clean PASS with
	// zero findings.
	Skipped string
	// Results holds every finding, unsorted; use SortedResults.
	Results []Result
}

// ErrorCount returns the number of blocking errors.
func (r *Report) ErrorCount() int {
	n := 0
	for _, res := range r.Results {
		if res.Severity == SeverityError {
			n++
		}
	}
	return n
}

// WarningCount returns the number of non-blocking warnings.
func (r *Report) WarningCount() int {
	n := 0
	for _, res := range r.Results {
		if res.Severity == SeverityWarning {
			n++
		}
	}
	return n
}

// Pass reports whether the repository is compliant: no blocking errors.
// Warnings do not affect the result.
func (r *Report) Pass() bool {
	return r.ErrorCount() == 0
}

// SortedResults returns the results in deterministic order:
// file path, then rule (R0, R1..R12), then severity (error, warning),
// then message.
func (r *Report) SortedResults() []Result {
	out := make([]Result, len(r.Results))
	copy(out, r.Results)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if ruleRank(a.Rule) != ruleRank(b.Rule) {
			return ruleRank(a.Rule) < ruleRank(b.Rule)
		}
		if a.Severity != b.Severity {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}
		return a.Message < b.Message
	})
	return out
}
