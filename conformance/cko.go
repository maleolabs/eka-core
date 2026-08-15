package conformance

import (
	"fmt"
	"os"
	"strings"
)

// This file implements the CKO-level validation split of the
// authoring-publish workflow (spec reference/spec-authoring-publish.md
// §6):
//
//	artifact, err := conformance.ScanFile(path)     // one draft file
//	report, err := conformance.ValidateCKO(unit, opts) // one canonical unit
//
// The repository gate (Validate) runs the full R0-R12 rule set over the
// authoring tree, including the location-shaped rules (R2 filename
// consistency, R6 dimension == folder). Publishing has no folder
// structure — a draft is a bare Markdown file, an inline publish has no
// file at all — so the CKO path validates ONE unit with the location
// rules skipped and the remaining rules evaluated over the unit's
// content. The rule table (spec §6):
//
//	CKO-level     identity, state values, owned-set, change-log,
//	              relationship resolution (draft tolerance), phase,
//	              classification validity, required sections
//	location-only R2 filename consistency, R6 dimension == folder
//
// The conformance package does not import the exchange package (exchange
// already imports conformance for DomainForToken; a reciprocal import
// would be a cycle), so ValidateCKO consumes its input through the
// CKOArtifact projection interface instead of the exchange.Unit type
// (documented deviation from the spec's spelling; the runtime wires the
// two together).

// CKOArtifact is the unit view consumed by ValidateCKO: anything that
// can project itself onto the Artifact model of this package.
// *exchange.Unit satisfies it via Unit.ToArtifact
// (exchange/artifact.go) — the read-side counterpart of the
// artifact -> unit construction the compiler performs. The interface
// keeps the engine exchange-free while making the spec's contract
// ("the authoring adapter differs, the contract does not") concrete:
// the same validator runs on both the draft path and the inline path.
type CKOArtifact interface {
	// ToArtifact projects the unit onto the Artifact model consumed
	// by the rule engine.
	ToArtifact() *Artifact
}

// ValidateCKOOptions configures one CKO-level validation run.
type ValidateCKOOptions struct {
	// Resolve reports whether the reference resolves in the runtime
	// (relationship rule 5). Nil = nothing resolves (draft tolerance
	// still applies via the artifact's content-state). The repository
	// path resolves against the engine's own artifact index instead;
	// this callback exists only for the CKO path.
	Resolve func(ref Reference) bool
	// Draft reports whether an unresolved reference target exists as a
	// draft (an unpublished authoring object) of the same project —
	// the CKO path's draft knowledge, owned by the runtime (the drafts
	// tree); conformance only asks. Rule 5 consults it after
	// resolution fails: a line-level reference whose target is a draft
	// is an allowed draft-to-draft authoring reference and produces no
	// finding (the source-side content-state tolerance still applies
	// to genuinely missing targets). Versioned references name
	// published instances (drafts never carry instance versions) and
	// are never tolerated. Nil = no draft knowledge (every unresolved
	// reference is reported).
	Draft func(ref Reference) bool
}

// ValidateCKO validates ONE canonical unit without the location rules:
// the engine runs over the unit's Artifact projection with R2 and the
// location-shaped part of R6 skipped, and Rule 5 resolution delegated to
// opts.Resolve. Structural findings that are file/frontmatter-specific
// cannot occur (the unit is already parsed); the identity checks that
// analyzeFile performs on files are mirrored on the unit's identity
// fields (ckoStructural), so an arbitrary unit can never silently pass
// on an unknown type token, an empty identity component or an invalid
// version.
//
// The returned Report is the caller's verdict: blocking violations are
// errors (Pass() == false); warnings never block.
func ValidateCKO(u CKOArtifact, opts ValidateCKOOptions) (*Report, error) {
	if u == nil {
		return nil, fmt.Errorf("conformance: CKO validation requires a unit")
	}
	a := u.ToArtifact()
	if a == nil {
		return nil, fmt.Errorf("conformance: unit projects to a nil artifact")
	}
	report := &Report{}
	e := &engine{
		report:       report,
		skipLocation: true,
		ckoResolve:   opts.Resolve,
		draftRef:     opts.Draft,
	}
	e.artifacts = []*Artifact{a}
	report.Artifacts = 1

	// Structural identity checks (the R0 bucket of the file path,
	// mirrored for units).
	ckoStructural(e, a)

	e.buildIndex()
	e.rule1()
	for _, art := range e.artifacts {
		e.rule2(art) // skipped via skipLocation (no filename)
		e.rule3(art)
		e.rule4(art)
		e.rule5(art)
		e.rule6(art) // location part skipped via skipLocation
		e.rule7(art)
		e.rule8(art)
		e.rule9(art)
		e.rule10(art)
		e.rule11(art)
		e.rule12(art)
	}
	// The graph pass (R13, ADR-019) at CKO level: the unit being
	// published is a single-artifact set, so the work-item transition
	// gates are skipped (they need the full authoring set — `eka sync`
	// is their source of truth); the cmt- note contract and the
	// discusses resolution (against the runtime store) still apply.
	report.Results = append(report.Results, ValidateGraph(e.artifacts, GraphOptions{
		Resolve:   opts.Resolve,
		SkipGates: true,
	})...)
	return report, nil
}

// ckoStructural runs the structural identity checks that analyzeFile
// performs on repository files, applied to a unit-converted artifact.
// The unit is already parsed, so YAML/frontmatter findings cannot occur;
// the identity fields themselves are arbitrary input and must be
// validated (spec §6: "identity (token valid, id form)").
func ckoStructural(e *engine, a *Artifact) {
	if _, ok := typeTokens[a.Type]; !ok {
		e.add(a, RuleStructural, SeverityError,
			"unknown artifact type %q; expected one of the %d EKA type tokens", a.Type, len(typeTokens))
	}
	if a.ID == "" {
		e.add(a, RuleStructural, SeverityError,
			"artifact id must be a non-empty string")
	}
	if a.Namespace == "" {
		e.add(a, RuleStructural, SeverityError,
			"missing required identity field `namespace`")
	}
	if a.InstanceVersion < 1 {
		e.add(a, RuleStructural, SeverityError,
			"`instance-version` must be >= 1")
	}
	if a.Revision < 1 {
		e.add(a, RuleStructural, SeverityError,
			"`revision` must be >= 1")
	}
	if a.Created != "" && !validDate(a.Created) {
		e.add(a, RuleStructural, SeverityError,
			"`created` must be a date in YYYY-MM-DD format")
	}
	if a.Updated != "" && !validDate(a.Updated) {
		e.add(a, RuleStructural, SeverityError,
			"`updated` must be a date in YYYY-MM-DD format")
	}
	for relType := range a.Relations {
		if !isRelationshipField(relType) {
			e.add(a, Rule5, SeverityError,
				"unknown relationship type %q (expected one of: %s)",
				relType, strings.Join(relationshipFields, ", "))
		}
	}
}

// isRelationshipField reports whether field is one of the eight
// canonical relationship field names (Rule 5).
func isRelationshipField(field string) bool {
	for _, f := range relationshipFields {
		if f == field {
			return true
		}
	}
	return false
}

// ScanError reports that one file failed the authoring adapter's
// structural classification (the R0 bucket of the single-file scan):
// malformed frontmatter, artifact-rule violations, missing or invalid
// identity fields. The findings are carried so the caller can render
// the parse report (the draft-publish edge case "draft file corrupted
// (invalid YAML): publish fails with the parse report, draft kept").
type ScanError struct {
	// Path is the scanned file path.
	Path string
	// Findings are the structural results, in classification order
	// (all errors; warnings never appear in this bucket).
	Findings []Result
}

// Error renders the deterministic refusal message.
func (e *ScanError) Error() string {
	if len(e.Findings) == 0 {
		return fmt.Sprintf("%s is not a valid EKA artifact", e.Path)
	}
	first := e.Findings[0]
	if len(e.Findings) == 1 {
		return fmt.Sprintf("%s is not a valid EKA artifact: %s", e.Path, first.Message)
	}
	return fmt.Sprintf("%s is not a valid EKA artifact (%d structural errors, first: %s)",
		e.Path, len(e.Findings), first.Message)
}

// ScanFile reads and classifies ONE authoring file (.md legacy or .json
// v2.0-native draft) — the single-file form of the authoring adapter
// used by the draft lifecycle (`eka edit` re-validation, `eka draft
// list` markers, `eka publish` parsing).
//
// It returns the parsed artifact, nil for convention documents
// (no frontmatter, or frontmatter without both `type` and `id`), and an
// error when the file cannot be read or fails the structural
// classification (*ScanError carrying the R0 findings). Unlike the
// repository scans, a missing `instance-version` is tolerated: drafts
// deliberately carry none (it is assigned at publish time).
func ScanFile(path string) (*Artifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	artifact, results := analyze(path, path, data, false)
	if len(results) > 0 {
		return nil, &ScanError{Path: path, Findings: results}
	}
	return artifact, nil
}
