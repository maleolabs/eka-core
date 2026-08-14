package exchange

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
)

// This file implements the import entry point:
//
//	result, err := exchange.Import(pkgPath, opts)
//
// Import is the reference implementation of the Exchange §11 pipeline
// (RSF §13.2): integrity verification, ordered phases 1-8 against the
// package, atomic commit (phase 9), post-commit revalidation with rollback
// (phase 10). Analysis runs fully before any write; a blocking failure
// aborts before commit and the repository stays byte-identical.
//
// Error contract (deterministic; "eka: " prefix rendered by the CLI):
//
//   - *ImportValidationError: the target repository failed the validation
//     gate — before the import (Phase "pre") or after the commit (Phase
//     "post", rollback already performed) — exit 1.
//   - *ConflictError: package content conflicts with existing repository
//     content (reject-by-default, Exchange §11.2) — exit 1. The carried
//     detail list names every conflicting artifact and its differences.
//   - *RelationshipError: a non-draft package relationship cannot be
//     resolved (declared external reference not present in the target;
//     draft tolerance is a warning, not an error) — exit 1.
//   - *PackageError: the package is unreadable, malformed, not
//     self-consistent, fails integrity verification, uses unsupported
//     versions or features, or violates package-side validation phases
//     1-4 — exit 2. *ContentError (identity charset) is mapped to exit 2
//     via the same mechanism.
//   - any other error: filesystem or internal failure (exit 2).

// ImportOptions configures one Import run. Named ImportOptions rather than
// Options: the export engine already declares Options (model.go), and the
// two option sets serve different pipelines (deviation from the task
// spelling, recorded in the implementation report).
type ImportOptions struct {
	// Root is the target repository root; "" means the current directory.
	Root string
	// Validate overrides the validation stage (default:
	// conformance.Validate). Injectable for tests: the same function runs
	// the pre-import gate (phase 0) and the post-commit revalidation
	// (phase 10).
	Validate func(root string) (*conformance.Report, error)
}

// ImportResult reports one Import run (the Import-Manifest-like summary of
// Exchange §11.3: accepted / skipped / conflict verdicts per artifact).
type ImportResult struct {
	// Root is the target repository root.
	Root string
	// PackageLabel is the Package Identity Label of the imported package.
	PackageLabel string
	// ImportedArtifacts lists the accepted unit identities in canonical
	// order.
	ImportedArtifacts []string
	// SkippedArtifacts lists the no-op duplicate identities (sorted).
	SkippedArtifacts []string
	// Conflicts lists the rejected artifacts with their differences
	// (empty on success).
	Conflicts []ConflictDetail
	// Warnings lists the non-blocking findings (draft tolerance), sorted.
	Warnings []string
	// AttachmentsImported lists the written attachment IDs (sorted).
	AttachmentsImported []string
	// AttachmentsSkipped lists the no-op duplicate attachment IDs.
	AttachmentsSkipped []string
	// PreValidation is the phase-0 validation report of the target
	// repository (always passing when Import succeeds).
	PreValidation *conformance.Report
	// Validation is the phase-10 post-commit revalidation report (always
	// passing when Import succeeds).
	Validation *conformance.Report
}

// ImportValidationError reports that the target repository failed the
// conformance validation gate. Phase "pre" means the import was refused
// before any write; Phase "post" means the commit happened and was fully
// rolled back (Exchange §11.1 phase 10). Both map to exit code 1.
type ImportValidationError struct {
	// Phase is "pre" or "post".
	Phase string
	// Report carries the full findings.
	Report *conformance.Report
}

func (e *ImportValidationError) Error() string {
	if e.Report == nil {
		// The no-docs-tree refusal (requireEKARepo). Deliberately NOT
		// worded "not an EKA repository": that phrasing belongs to the
		// eka.yaml repository-context gate (ADR-018) — a directory
		// carrying eka.yaml but no docs/ tree still reaches this
		// engine-level check.
		return "import refused: the target directory has no docs/ knowledge tree; no changes written"
	}
	switch e.Phase {
	case "post":
		return fmt.Sprintf("import failed: post-commit revalidation found %d blocking error(s); all imported files were rolled back and the repository is unchanged",
			e.Report.ErrorCount())
	default:
		return fmt.Sprintf("import refused: repository validation failed with %d blocking error(s); no changes written",
			e.Report.ErrorCount())
	}
}

// RelationshipError reports that a non-draft package relationship cannot
// be resolved at the target (Exchange §7.4 failure of all three
// resolution steps; no draft tolerance). Mapped to exit code 1.
type RelationshipError struct {
	// Details lists the failing relationships, deterministically ordered.
	Details []string
}

func (e *RelationshipError) Error() string {
	return fmt.Sprintf("import refused: %d unresolved relationship(s) outside draft tolerance; no changes written",
		len(e.Details))
}

// Import validates the target repository, reads and validates the package
// (Exchange §11.1 phases 1-8), commits the accepted content atomically
// (phase 9) and revalidates the repository (phase 10, rollback on
// failure). Deterministic for identical inputs: identical package +
// identical target repository -> identical result.
func Import(pkgPath string, opts ImportOptions) (*ImportResult, error) {
	root := opts.Root
	if root == "" {
		root = "."
	}
	validate := opts.Validate
	if validate == nil {
		validate = conformance.Validate
	}

	// Phase 0 gate: the target must be an EKA repository (docs/ tree)
	// and must validate (Exchange §12.5, validation-before-commit). A
	// docs-less destination is refused with exit code 1 (documented
	// CLI decision). The eka.yaml repository-context gate (ADR-018)
	// runs earlier, at the CLI level.
	if err := requireEKARepo(root); err != nil {
		return nil, err
	}
	preReport, err := validate(root)
	if err != nil {
		return nil, fmt.Errorf("import failed: validation error: %w", err)
	}
	if !preReport.Pass() {
		return nil, &ImportValidationError{Phase: "pre", Report: preReport}
	}

	// Package read + integrity verification + self-consistency (before
	// phase 1; Exchange §17.1, RSF §13.2).
	pkg, err := loadPackage(pkgPath)
	if err != nil {
		return nil, err
	}

	// Phase 1: contract validation.
	if err := checkContract(pkg); err != nil {
		return nil, err
	}
	// Phase 2: identity validation (canonical + unique within package).
	if err := checkUnitIdentities(pkg); err != nil {
		return nil, err
	}
	// Phase 3: state validation.
	if err := checkUnitStates(pkg); err != nil {
		return nil, err
	}
	// Phase 4: structural validation (content well-formedness per type).
	if err := checkUnitStructure(pkg); err != nil {
		return nil, err
	}

	// Target repository index (one read, reused by phases 5-7).
	target, err := newIdentityResolver(root)
	if err != nil {
		return nil, err
	}

	// Phase 5: referential validation (Exchange §7.4 order; draft
	// tolerance).
	warnings, err := checkReferences(pkg, target)
	if err != nil {
		return nil, err
	}

	// Phases 6-7: conflict detection + duplicate detection (conservative
	// merge).
	cl := classify(pkg, target)
	if len(cl.conflicts) > 0 || len(cl.conflictAttachment) > 0 {
		all := append(append([]ConflictDetail{}, cl.conflicts...), cl.conflictAttachment...)
		sort.Slice(all, func(i, j int) bool { return all[i].Identity < all[j].Identity })
		return nil, &ConflictError{Conflicts: all}
	}

	// Phase 8: dependency resolution (commit order) + deterministic write
	// plan (repository reconstruction).
	order := commitOrder(pkg, cl)
	ops, err := buildWritePlan(pkg, cl, order, root)
	if err != nil {
		return nil, err
	}

	// Phase 9: commit (staged, atomic per file; rollback on any failure).
	writer := newRepoWriter(root)
	for _, op := range ops {
		if err := writer.write(op); err != nil {
			return nil, rollbackOrReport(err, writer.rollback())
		}
	}

	// Phase 10: post-commit revalidation (Exchange §11.1 phase 10, §13.2.5);
	// a failing repository is rolled back to its pre-import state.
	postReport, err := validate(root)
	if err != nil {
		return nil, rollbackOrReport(
			fmt.Errorf("import failed: post-commit validation error: %w", err),
			writer.rollback())
	}
	if !postReport.Pass() {
		return nil, rollbackOrReport(
			&ImportValidationError{Phase: "post", Report: postReport},
			writer.rollback())
	}

	result := &ImportResult{
		Root:                root,
		PackageLabel:        pkg.header.PackageIdentityLabel,
		PreValidation:       preReport,
		Validation:          postReport,
		Warnings:            warnings,
		Conflicts:           []ConflictDetail{},
		AttachmentsImported: attachmentIDs(cl.newAttachments),
		AttachmentsSkipped:  append([]string{}, cl.duplicateAttachment...),
	}
	for _, u := range cl.newUnits {
		result.ImportedArtifacts = append(result.ImportedArtifacts, u.CanonicalIdentityForm)
	}
	result.SkippedArtifacts = append(result.SkippedArtifacts, cl.duplicateForms...)
	return result, nil
}

// requireEKARepo verifies the target repository carries the docs/
// knowledge tree (the legacy authoring adapter import writes into).
// Refused with exit code 1 (documented CLI decision: a docs-less
// destination is a repository-side failure, not a usage error). Note:
// the eka.yaml repository-context gate (ADR-018) runs earlier at the
// CLI level; this engine-level check covers the docs tree only. The
// carried report is nil: there is no validation finding — the
// directory simply has no docs/ knowledge tree.
func requireEKARepo(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("import failed: cannot access target repository: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("import failed: target repository %s is not a directory", root)
	}
	docs := filepath.Join(root, "docs")
	dinfo, err := os.Stat(docs)
	if err != nil {
		return &ImportValidationError{Phase: "pre", Report: nil}
	}
	if !dinfo.IsDir() {
		return &ImportValidationError{Phase: "pre", Report: nil}
	}
	return nil
}

// checkUnitStates (phase 3) validates every unit's state vector: value
// validity per domain (validation.md Aturan 3), owned-set compliance per
// type (Aturan 4), classification validity (rule 6), and change-log
// consistency (Aturan 7: last entry per owned domain equals the current
// value).
func checkUnitStates(pkg *loadedPackage) error {
	for _, u := range pkg.units {
		info := conformance.TypeInfoFor(u.Identity.Type)
		if info == nil {
			// Unknown type: the unit identity check reports it; refuse
			// here as well (deterministic single path).
			return packageErrorf(
				"import refused: unit %s uses unknown artifact type %q; this importer implements the 26 EKA type tokens",
				u.CanonicalIdentityForm, u.Identity.Type)
		}

		// Owned-set compliance (Aturan 4): exactly the owned domains, with
		// non-empty values.
		owned := map[string]bool{}
		for _, d := range info.Owned {
			owned[d] = true
		}
		for _, d := range []struct {
			domain string
			value  string
		}{
			{conformance.DomainContentState, u.StateVector.ContentState},
			{conformance.DomainExecutionState, u.StateVector.ExecutionState},
			{conformance.DomainPlanningState, u.StateVector.PlanningState},
			{conformance.DomainContainerState, u.StateVector.ContainerState},
			{conformance.DomainExistenceState, u.StateVector.ExistenceState},
		} {
			if d.value == "" {
				if owned[d.domain] {
					return packageErrorf(
						"import refused: unit %s is missing owned state field %s (owned set of type %q: %s)",
						u.CanonicalIdentityForm, d.domain, u.Identity.Type, strings.Join(info.Owned, ", "))
				}
				continue
			}
			if !owned[d.domain] {
				return packageErrorf(
					"import refused: unit %s carries state field %s which is not owned by type %q (owned set: %s)",
					u.CanonicalIdentityForm, d.domain, u.Identity.Type, strings.Join(info.Owned, ", "))
			}
			if !conformance.ValidStateValue(d.domain, u.Identity.Type, d.value) {
				return packageErrorf(
					"import refused: unit %s carries %s %q which is not a valid value for the domain (allowed values for type %q: %s)",
					u.CanonicalIdentityForm, d.domain, d.value, u.Identity.Type,
					strings.Join(stateValuesFor(d.domain, u.Identity.Type), ", "))
			}
		}

		// Phase context (Aturan 3): valid value, scp-/plan- only.
		if u.Phase != "" {
			if !conformance.IsVersionedType(u.Identity.Type) {
				return packageErrorf(
					"import refused: unit %s carries phase %q but phase is a context attribute allowed only on scp-/plan- artifacts",
					u.CanonicalIdentityForm, u.Phase)
			}
			if !conformance.ValidPhaseValue(u.Phase) {
				return packageErrorf(
					"import refused: unit %s carries phase %q which is not a valid phase value (allowed: discovery, mvp, milestone, release, growth, maturity, sunset)",
					u.CanonicalIdentityForm, u.Phase)
			}
		}

		// Classification validity (rule 6): knowledge types must declare a
		// valid primary dimension; projections must not carry one; work
		// items carry dimension informationally (not evaluated).
		switch {
		case info.IsKnowledge:
			if u.Classification.Dimension == "" {
				return packageErrorf(
					"import refused: unit %s (knowledge artifact type %q) declares no primary knowledge dimension (classification is a required artifact property)",
					u.CanonicalIdentityForm, u.Identity.Type)
			}
			if !conformance.IsDimensionToken(u.Classification.Dimension) {
				return packageErrorf(
					"import refused: unit %s declares unknown knowledge dimension %q",
					u.CanonicalIdentityForm, u.Classification.Dimension)
			}
		case isProjectionType(u.Identity.Type):
			if u.Classification.Dimension != "" || len(u.Classification.DimensionsSecondary) > 0 {
				return packageErrorf(
					"import refused: unit %s (projection type %q) must not carry a dimension or secondary dimensions",
					u.CanonicalIdentityForm, u.Identity.Type)
			}
		}

		// Engineering Domain coherence (Rule 11 mirror): a declared
		// `domain` in the unit classification must be canonical and must
		// equal the type token's home domain. Absent = OK: the domain is
		// derived from the token (packages written before the field
		// existed keep importing).
		if u.Classification.Domain != "" {
			if !conformance.IsDomain(u.Classification.Domain) {
				return packageErrorf(
					"import refused: unit %s declares unknown engineering domain %q (canonical domains: %s)",
					u.CanonicalIdentityForm, u.Classification.Domain,
					strings.Join(conformance.DomainNames(), ", "))
			}
			home, _ := conformance.DomainForToken(u.Identity.Type) // type already checked above
			if u.Classification.Domain != string(home) {
				return packageErrorf(
					"import refused: unit %s declares domain %q which does not match the home domain %q of type %q",
					u.CanonicalIdentityForm, u.Classification.Domain, home, u.Identity.Type)
			}
		}

		// Change-log consistency (Aturan 7): every owned domain (and phase
		// when present) must have entries whose last entry equals the
		// current value; entries must be well-formed and use owned domains.
		needed := append([]string{}, info.Owned...)
		if u.Phase != "" {
			needed = append(needed, conformance.DomainPhase)
		}
		for _, domain := range needed {
			current := ""
			if domain == conformance.DomainPhase {
				current = u.Phase
			} else {
				current = u.StateVectorValue(domain)
			}
			if current == "" {
				continue
			}
			entries := changeLogEntriesFor(u.ChangeLog, domain)
			if len(entries) == 0 {
				return packageErrorf(
					"import refused: unit %s has no change-log entry for domain %q (current value %q)",
					u.CanonicalIdentityForm, domain, current)
			}
			if last := entries[len(entries)-1]; last.To != current {
				return packageErrorf(
					"import refused: unit %s last change-log entry for %q ends at %q but the state vector holds %q (change-log inconsistency)",
					u.CanonicalIdentityForm, domain, last.To, current)
			}
		}
		for i, e := range u.ChangeLog {
			if !isKnownStateDomain(e.Domain) {
				return packageErrorf(
					"import refused: unit %s change-log entry %d uses unknown domain %q",
					u.CanonicalIdentityForm, i+1, e.Domain)
			}
			if e.Domain == conformance.DomainPhase {
				if !conformance.IsVersionedType(u.Identity.Type) {
					return packageErrorf(
						"import refused: unit %s change-log entry %d records phase but phase is allowed only on scp-/plan- artifacts",
						u.CanonicalIdentityForm, i+1)
				}
			} else if !owned[e.Domain] {
				return packageErrorf(
					"import refused: unit %s change-log entry %d records domain %q which is not owned by type %q",
					u.CanonicalIdentityForm, i+1, e.Domain, u.Identity.Type)
			}
			if e.From != "-" && !conformance.ValidStateValue(e.Domain, u.Identity.Type, e.From) {
				return packageErrorf(
					"import refused: unit %s change-log entry %d records invalid from-value %q for domain %q",
					u.CanonicalIdentityForm, i+1, e.From, e.Domain)
			}
			if e.To == "-" || !conformance.ValidStateValue(e.Domain, u.Identity.Type, e.To) {
				return packageErrorf(
					"import refused: unit %s change-log entry %d records invalid to-value %q for domain %q",
					u.CanonicalIdentityForm, i+1, e.To, e.Domain)
			}
			if e.By.Name == "" {
				return packageErrorf(
					"import refused: unit %s change-log entry %d has an empty authority (by)",
					u.CanonicalIdentityForm, i+1)
			}
			if e.Date == "" {
				return packageErrorf(
					"import refused: unit %s change-log entry %d has an empty date",
					u.CanonicalIdentityForm, i+1)
			}
		}
	}
	return nil
}

// StateVectorValue returns the value of one domain in the vector ("" when
// absent).
func (u *Unit) StateVectorValue(domain string) string {
	switch domain {
	case conformance.DomainContentState:
		return u.StateVector.ContentState
	case conformance.DomainExecutionState:
		return u.StateVector.ExecutionState
	case conformance.DomainPlanningState:
		return u.StateVector.PlanningState
	case conformance.DomainContainerState:
		return u.StateVector.ContainerState
	case conformance.DomainExistenceState:
		return u.StateVector.ExistenceState
	}
	return ""
}

// isKnownStateDomain reports whether d is one of the five owned state
// domains or the phase context attribute.
func isKnownStateDomain(d string) bool {
	switch d {
	case conformance.DomainContentState, conformance.DomainExecutionState,
		conformance.DomainPlanningState, conformance.DomainContainerState,
		conformance.DomainExistenceState, conformance.DomainPhase:
		return true
	}
	return false
}

// stateValuesFor renders the allowed value list of a domain for a type
// (deterministic diagnostics; validation.md Aturan 3). The value sets are
// NOT mirrored here: they come from conformance.DomainValues, the single
// exported source of truth behind ValidStateValue/ValidPhaseValue — the
// diagnostics can never drift from what the validator accepts.
func stateValuesFor(domain, typeToken string) []string {
	return conformance.DomainValues(domain, typeToken)
}

// changeLogEntriesFor returns the change-log entries of one domain in
// occurrence order.
func changeLogEntriesFor(log []ChangeLogEntry, domain string) []ChangeLogEntry {
	var out []ChangeLogEntry
	for _, e := range log {
		if e.Domain == domain {
			out = append(out, e)
		}
	}
	return out
}

// isProjectionType reports whether the type is a projection/operating
// type that must not carry classification (ctr-/tkt-/ses-).
func isProjectionType(token string) bool {
	switch token {
	case "ctr", "tkt", "ses":
		return true
	}
	return false
}

// checkUnitStructure (phase 4) validates content well-formedness per type
// family (Aturan 9): the content body must be present and carry every
// required section. The check is representation-aware: structured-json
// content (v2.0) is verified by content key (spec-standard-v2 §7 — R9
// becomes a key check); legacy structured-text content keeps the
// heading parse.
func checkUnitStructure(pkg *loadedPackage) error {
	for _, u := range pkg.units {
		if len(u.ContentPayload) == 0 {
			return packageErrorf(
				"import refused: unit %s carries an empty content body", u.CanonicalIdentityForm)
		}
		required := conformance.RequiredSectionsFor(u.Identity.Type)
		if len(required) == 0 {
			continue
		}
		var missing []string
		if u.Content.Representation == StructuredJSON {
			fields := map[string]any{}
			if err := json.Unmarshal(u.ContentPayload, &fields); err == nil {
				for _, section := range required {
					if _, ok := fields[conformance.SectionKey(section)]; !ok {
						missing = append(missing, conformance.SectionKey(section))
					}
				}
			} else {
				missing = append(missing, "content is not a valid JSON object")
			}
		} else {
			for _, section := range required {
				if !bodyHasSection(u.ContentPayload, section) {
					missing = append(missing, section)
				}
			}
		}
		if len(missing) > 0 {
			return packageErrorf(
				"import refused: unit %s content is missing required section(s) for type %q: %s",
				u.CanonicalIdentityForm, u.Identity.Type, strings.Join(missing, ", "))
		}
	}
	return nil
}

// bodyHasSection mirrors the validator's section matching: a line starting
// with "## " whose heading equals the section name or starts with it plus
// a space.
func bodyHasSection(body []byte, section string) bool {
	for _, line := range strings.Split(string(body), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "## ") {
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(t, "## "))
		if text == section || strings.HasPrefix(text, section+" ") {
			return true
		}
	}
	return false
}

// checkReferences (phase 5) resolves every package relationship in the
// Exchange §7.4 order (local -> global -> external). Failures of all three
// steps block, except under draft tolerance (content-state draft), which
// produces a warning. An undeclared external target makes the package
// invalid (RSF §12.3.1 declaration is mandatory): *PackageError, exit 2.
func checkReferences(pkg *loadedPackage, target *IdentityResolver) ([]string, error) {
	resolver := newRelationshipResolver(pkg, target)
	var warnings []string
	var failures []string
	for _, u := range pkg.units {
		for _, rel := range u.Relationships {
			res := resolver.resolve(u.CanonicalIdentityForm, rel.Type, rel.Target)
			if res.ok {
				continue
			}
			if !res.declared {
				return nil, packageErrorf(
					"import refused: package unit %s references %s %s, which is not carried by the package, not present in the target repository, and not declared as an External Reference (declaration is mandatory, Exchange §12.3); the package is invalid",
					u.CanonicalIdentityForm, rel.Type, rel.Target)
			}
			if isDraft(u) {
				warnings = append(warnings, fmt.Sprintf(
					"%s: declared external reference %s %s is unresolved; allowed while content-state is draft (draft tolerance, rule 5)",
					u.CanonicalIdentityForm, rel.Type, rel.Target))
				continue
			}
			failures = append(failures, fmt.Sprintf(
				"%s -> %s %s (declared external reference does not resolve in the target repository)",
				u.CanonicalIdentityForm, rel.Type, rel.Target))
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return nil, &RelationshipError{Details: failures}
	}
	sort.Strings(warnings)
	return warnings, nil
}

// attachmentIDs extracts the sorted ID list of new attachments.
func attachmentIDs(atts []*Attachment) []string {
	ids := make([]string, 0, len(atts))
	for _, a := range atts {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	return ids
}
