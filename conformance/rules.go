package conformance

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// This file implements Rules R1-R9 from skeleton/docs/exchange/validation.md
// (R10-R12 live in rules_domain.go). Each rule function is self-contained;
// interpretation decisions beyond the literal rule text are marked
// "Interpretation (documented)" and collected in the final report of the
// implementation.

// ---------------------------------------------------------------------------
// Rule 1 — Identity uniqueness
// ---------------------------------------------------------------------------
// No duplicate (namespace, type, id, instance-version) across all artifacts.

func (e *engine) rule1() {
	seen := map[string]string{} // identity key -> first file
	for _, a := range e.artifacts {
		key := identityKey(a.Namespace, a.Type, a.ID) + "\x00" + strconv.Itoa(a.InstanceVersion)
		if first, dup := seen[key]; dup {
			e.add(a, Rule1, SeverityError,
				"duplicate identity (namespace=%q type=%q id=%q instance-version=%d) already used by %s",
				a.Namespace, a.Type, a.ID, a.InstanceVersion, first)
			continue
		}
		seen[key] = a.RelPath
	}
}

// ---------------------------------------------------------------------------
// Rule 2 — Filename consistency
// ---------------------------------------------------------------------------
// Token on the filename == frontmatter `type`; -v<nn> suffix (if present) ==
// `instance-version`; -v<nn> only on scp-/plan- and mandatory there.
// The authoring extension may be .md (legacy) or .json (v2.0 native);
// the filename rule is identical for both.
//
// Interpretation (documented): the filename's id segment is NOT required to
// equal the frontmatter `id` — the rule never asks for it and the filename
// is only a projection (ADR-001). This is a noted gap, not a check.

// authoringExtensions are the accepted authoring file extensions (R2).
var authoringExtensions = []string{".md", ".json"}

// stripAuthoringExtension removes the authoring extension (.md or .json)
// from a file base name.
func stripAuthoringExtension(base string) string {
	for _, ext := range authoringExtensions {
		if strings.HasSuffix(base, ext) {
			return strings.TrimSuffix(base, ext)
		}
	}
	return base
}

func (e *engine) rule2(a *Artifact) {
	if e.skipLocation {
		// CKO-level validation: no file exists, so filename consistency
		// (a file-layout convention) is not applicable.
		return
	}
	if _, ok := typeTokens[a.Type]; !ok {
		// Unknown type: R0 already reported; filename consistency is
		// meaningless without a known token.
		return
	}
	name := stripAuthoringExtension(filepath.Base(a.RelPath))
	p, err := parseFilename(name)
	if err != nil {
		e.add(a, Rule2, SeverityError, "cannot parse filename: %v", err)
		return
	}
	if p.Token != a.Type {
		e.add(a, Rule2, SeverityError,
			"filename token %q does not match frontmatter `type` %q", p.Token, a.Type)
		return
	}
	switch {
	case p.HasVersion && !versionedTypes[a.Type]:
		e.add(a, Rule2, SeverityError,
			"filename carries a -v<nn> suffix but type %q is not versioned (only scp-/plan- may)", a.Type)
	case p.HasVersion && p.Version != a.InstanceVersion:
		e.add(a, Rule2, SeverityError,
			"filename version %d does not match frontmatter `instance-version` %d", p.Version, a.InstanceVersion)
	case !p.HasVersion && versionedTypes[a.Type]:
		e.add(a, Rule2, SeverityError,
			"filename of versioned type %q must carry a -v<nn> suffix (including v1)", a.Type)
	}
}

// ---------------------------------------------------------------------------
// Rule 3 — State value validity
// ---------------------------------------------------------------------------
// Every present state field value must belong to its domain's value set
// (content-state variant selected by artifact type); phase must be a phase
// value and may only appear on scp-/plan-.

func (e *engine) rule3(a *Artifact) {
	for domain, value := range a.States {
		values := domainValues(domain, a.Type)
		if !contains(values, value) {
			e.add(a, Rule3, SeverityError,
				"%s %q is not a valid value for %s (allowed: %s)",
				domain, value, domain, strings.Join(values, ", "))
		}
	}
	if a.HasPhaseKey {
		if !versionedTypes[a.Type] {
			e.add(a, Rule3, SeverityError,
				"`phase` is a context attribute allowed only on scp-/plan- artifacts, not on type %q", a.Type)
			return
		}
		if !contains(phaseValues, a.Phase) {
			e.add(a, Rule3, SeverityError,
				"phase %q is not a valid phase value (allowed: %s)",
				a.Phase, strings.Join(phaseValues, ", "))
		}
	}
}

// ---------------------------------------------------------------------------
// Rule 4 — Owned-set compliance
// ---------------------------------------------------------------------------
// A state field present on a file must be owned by its type; absence of a
// field for a non-owned domain is N/A. A ticket (tkt-) carries no state
// fields at all.
//
// Interpretation (documented): validation.md says the present state fields
// must be "persis sama" (exactly equal) with the owned set, and ADR-002 says
// fields "hanya hadir untuk domain yang dimiliki". We therefore also report
// an owned domain that is absent from the file: an artifact of a type owning
// {content-state, existence-state} must carry both, matching every real
// artifact in the reference implementation.

func (e *engine) rule4(a *Artifact) {
	info, known := typeTokens[a.Type]
	if !known {
		return // R0 already reported.
	}
	for domain := range a.States {
		if !contains(info.Owned, domain) {
			e.add(a, Rule4, SeverityError,
				"state field %s is not owned by type %q (owned: %s)",
				domain, a.Type, strings.Join(info.Owned, ", "))
		}
	}
	for _, domain := range info.Owned {
		if _, ok := a.States[domain]; !ok {
			e.add(a, Rule4, SeverityError,
				"missing owned state field %s on type %q", domain, a.Type)
		}
	}
}

// ---------------------------------------------------------------------------
// Rule 5 — Referential integrity
// ---------------------------------------------------------------------------
// Every reference in amends/supersedes/derives-from/depends-on/validates
// must resolve to an existing artifact. Unresolved references on a
// `content-state: draft` artifact are warnings; everywhere else they are
// errors. Self-references are always errors. In CKO mode (ValidateCKO),
// an unresolved line-level reference whose target exists as a draft of
// the same project (ValidateCKOOptions.Draft) is an allowed
// draft-to-draft authoring reference and produces no finding at all.
//
// Reference grammar (validation.md Rule 5, docs/README.md):
//
//	<type>:<id>[:<instance-version>]        same namespace
//	<namespace>/<type>:<id>[:<version>]     cross namespace
//
// Interpretation (documented): the repository's own ADRs use bare-id
// references (e.g. `depends-on: [001-identity-serialization]`), which do not
// match the documented grammar. Since 6 of the 7 reference ADRs use this
// form, a bare id is accepted as a line reference resolved within the
// referrer's own namespace and type. A reference that cannot be parsed at
// all is a structural violation and is an error regardless of content-state.
// Rule 5's "references are written only on the referring artifact (not
// two-way)" bullet is not mechanically enforced (documented gap).

type reference struct {
	Namespace  string
	Type       string
	ID         string
	Version    int
	HasVersion bool
}

// Reference is the exported form of the parsed reference grammar. It is an
// alias of the internal reference type: exporting it is purely additive and
// changes no rule behavior. The Exchange layer (exchange/) uses
// ParseReference for target parsing and relationship resolution so that
// reference semantics are shared with the validator instead of duplicated.
type Reference = reference

// ParseReference parses a reference string per the validation.md Rule 5
// grammar:
//
//	<type>:<id>[:<instance-version>]      same namespace (defNamespace)
//	<namespace>/<type>:<id>[:<version>]   cross namespace
//
// defNamespace is the referrer's namespace; defType is the referrer's type
// and allows the bare-id form ("" disables it). See parseReference.
func ParseReference(s, defNamespace, defType string) (Reference, error) {
	return parseReference(s, defNamespace, defType)
}

func parseReference(s, defNamespace, defType string) (reference, error) {
	var ref reference
	s = strings.TrimSpace(s)
	if s == "" {
		return ref, fmt.Errorf("empty reference")
	}
	ref.Namespace = defNamespace
	rest := s
	if i := strings.Index(s, "/"); i >= 0 {
		ref.Namespace = s[:i]
		rest = s[i+1:]
		if ref.Namespace == "" {
			return ref, fmt.Errorf("reference %q has an empty namespace", s)
		}
	}
	if i := strings.Index(rest, ":"); i >= 0 {
		ref.Type = rest[:i]
		rest = rest[i+1:]
		if _, ok := typeTokens[ref.Type]; !ok {
			return ref, fmt.Errorf("reference %q uses unknown type token %q", s, ref.Type)
		}
	} else if defType == "" {
		return ref, fmt.Errorf("reference %q must include a type token", s)
	} else {
		ref.Type = defType
	}
	if _, ok := typeTokens[ref.Type]; !ok {
		return ref, fmt.Errorf("reference %q uses unknown type token %q", s, ref.Type)
	}
	// Optional instance-version suffix: `<id>:<digits>`.
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		suffix := rest[i+1:]
		if allDigits(suffix) {
			ver, err := strconv.Atoi(suffix)
			if err != nil {
				return ref, fmt.Errorf("reference %q has invalid instance-version", s)
			}
			ref.Version = ver
			ref.HasVersion = true
			rest = rest[:i]
		}
	}
	if rest == "" || strings.Contains(rest, ":") {
		return ref, fmt.Errorf("reference %q has an invalid id (ids may contain hyphens but not colons)", s)
	}
	ref.ID = rest
	return ref, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// resolve returns the artifact a reference points to, or nil.
func (e *engine) resolve(ref reference) *Artifact {
	bucket := e.byLine[identityKey(ref.Namespace, ref.Type, ref.ID)]
	if len(bucket) == 0 {
		return nil
	}
	if ref.HasVersion {
		for _, a := range bucket {
			if a.InstanceVersion == ref.Version {
				return a
			}
		}
		return nil
	}
	// Line-level reference: resolves if any instance exists; the bucket is
	// sorted by instance-version so the lowest instance is returned.
	return bucket[0]
}

// resolveRef reports whether a parsed reference resolves — the shared
// resolution semantics of Rules 5, 8 and 9. In repository mode it uses
// the engine's artifact index (e.resolve); in CKO mode (ckoResolve set)
// it delegates to the runtime callback, because the CKO index holds
// only the unit being validated and every other reference target lives
// in the runtime store.
func (e *engine) resolveRef(ref reference) bool {
	if e.ckoResolve != nil {
		return e.ckoResolve(ref)
	}
	return e.resolve(ref) != nil
}

func (e *engine) rule5(a *Artifact) {
	for _, field := range relationshipFields {
		for _, raw := range a.Relations[field] {
			ref, err := parseReference(raw, a.Namespace, a.Type)
			if err != nil {
				e.add(a, Rule5, SeverityError,
					"malformed reference %q in `%s`: %v", raw, field, err)
				continue
			}
			if e.ckoResolve != nil {
				// CKO-level validation: resolution is the runtime
				// callback (the engine's index holds only the unit
				// itself). Self-references are detected on the parsed
				// identity: the unit's own line, optionally pinned to
				// its own instance-version.
				if ref.Namespace == a.Namespace && ref.Type == a.Type && ref.ID == a.ID &&
					(!ref.HasVersion || ref.Version == a.InstanceVersion) {
					e.add(a, Rule5, SeverityError,
						"self-reference %q in `%s`", raw, field)
					continue
				}
				if e.resolveRef(ref) {
					continue
				}
				// Draft target tolerance: the unresolved target
				// exists as a draft (unpublished) of the same
				// project — an authoring reference between drafts
				// (the target publishes later), no finding.
				// Versioned references name published instances
				// (drafts never carry instance versions), so they
				// are never tolerated.
				if !ref.HasVersion && e.draftRef != nil && e.draftRef(ref) {
					continue
				}
				if a.States[DomainContentState] == "draft" {
					e.add(a, Rule5, SeverityWarning,
						"unresolved reference %q in `%s` (allowed while content-state is draft)", raw, field)
				} else {
					e.add(a, Rule5, SeverityError,
						"unresolved reference %q in `%s`", raw, field)
				}
				continue
			}
			target := e.resolve(ref)
			if target == nil {
				// Draft target tolerance (CKO mode only: the
				// repository Validate path never sets draftRef):
				// the unresolved target exists as a draft of the
				// same project — an authoring reference between
				// drafts, no finding. Versioned references name
				// published instances (drafts never carry instance
				// versions), so they are never tolerated.
				if !ref.HasVersion && e.draftRef != nil && e.draftRef(ref) {
					continue
				}
				if a.States[DomainContentState] == "draft" {
					e.add(a, Rule5, SeverityWarning,
						"unresolved reference %q in `%s` (allowed while content-state is draft)", raw, field)
				} else {
					e.add(a, Rule5, SeverityError,
						"unresolved reference %q in `%s`", raw, field)
				}
				continue
			}
			if target == a {
				e.add(a, Rule5, SeverityError,
					"self-reference %q in `%s`", raw, field)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Rule 6 — Dimension == folder
// ---------------------------------------------------------------------------
// Knowledge artifacts must carry a valid `dimension` equal to their home
// dimension folder. Work-item artifacts may carry `dimension`
// informationally and are not evaluated. ctr-/tkt-/ses- must not carry
// `dimension` (or `dimensions-secondary`).
//
// Interpretation (documented): "home folder" is the nearest ancestor
// directory whose name is one of the 12 dimension tokens (this makes
// reference/decisions/ and docs/decisions/ both map to `decisions`, and it
// keeps working for artifacts nested below a dimension folder). A knowledge
// artifact whose path has no dimension-folder ancestor is an error, as is a
// knowledge artifact that omits `dimension` entirely (classification is a
// required artifact property per reference-architecture.md §2.5).

func dimensionFolderFor(absPath string) (string, bool) {
	dir := filepath.Dir(absPath)
	for {
		name := filepath.Base(dir)
		if dimensionTokens[name] {
			return name, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func (e *engine) rule6(a *Artifact) {
	if projectionTypes[a.Type] {
		if a.HasDimension {
			e.add(a, Rule6, SeverityError,
				"`dimension` is not allowed on type %q (projection/operating artifact)", a.Type)
		}
		if len(a.DimensionsSecondary) > 0 {
			e.add(a, Rule6, SeverityError,
				"`dimensions-secondary` is not allowed on type %q (projection/operating artifact)", a.Type)
		}
		return
	}
	info, known := typeTokens[a.Type]
	if !known {
		return // R0 already reported.
	}
	if !info.IsKnowledge {
		// Work items: dimension is informational only.
		return
	}
	if !a.HasDimension {
		e.add(a, Rule6, SeverityError, "missing `dimension` on knowledge artifact type %q", a.Type)
		return
	}
	if !dimensionTokens[a.Dimension] {
		e.add(a, Rule6, SeverityError,
			"unknown dimension %q (allowed: %s)",
			a.Dimension, strings.Join(dimensionList(), ", "))
	}
	// The location-shaped part (dimension == home folder) applies only
	// to the repository gate: CKO-level validation (skipLocation) has no
	// folder structure. The classification-validity checks above stay
	// active in both modes.
	if !e.skipLocation {
		folder, ok := dimensionFolderFor(a.AbsPath)
		if !ok {
			e.add(a, Rule6, SeverityError,
				"artifact is not located under a dimension folder (dimension %q cannot be verified)", a.Dimension)
		} else if a.Dimension != folder {
			e.add(a, Rule6, SeverityError,
				"dimension %q does not match home folder %q", a.Dimension, folder)
		}
	}
	for _, d := range a.DimensionsSecondary {
		if !dimensionTokens[d] {
			e.add(a, Rule6, SeverityError, "unknown secondary dimension %q", d)
		}
	}
}

func dimensionList() []string {
	out := make([]string, 0, len(dimensionTokens))
	for d := range dimensionTokens {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Rule 7 — Change-log consistency
// ---------------------------------------------------------------------------
// Every owned domain must have at least one change-log entry and its LAST
// entry (in file order) must equal the current field value. Entries for
// non-owned domains are errors. Phase entries are allowed only on scp-/plan-
// and, when the artifact carries `phase`, must end at the current value.
// Every entry must be well-formed (date, domain, from, to, by) and every
// recorded transition must be legal.
//
// Interpretation (documented):
//   - A snapshot cannot verify "no transition without entry" (validation.md
//     Rule 7, second bullet) because intermediate states are not observable
//     from a static file. We enforce entry existence, last-entry==current,
//     entry well-formedness and transition legality; we do not require
//     entry[i].from == entry[i-1].to (the repository's own ADRs begin their
//     content-state history mid-chain, e.g. proposed -> accepted without a
//     recorded initial proposed).
//   - `from: "-"` marks the initial state and is legal on any domain; it is
//     the convention used by every ADR in the reference implementation.
//   - `to` must be a real value of the domain ("-" is only a from-marker).
//   - Execution State transitions must follow the explicit D1 table
//     (forward adjacent, one-step pull-back, cancel, re-activation —
//     ADR-019 §3); other domains only need to be forward (see
//     isLegalTransition).
//   - Phase is a context attribute, not a state domain: its entries are
//     validated for well-formedness and value validity only, with no
//     ordering constraint (EKA 11.2).
//   - An artifact that owns no domain and carries no phase (tkt-) is not
//     required to have a change-log at all.

func (e *engine) rule7(a *Artifact) {
	info, known := typeTokens[a.Type]
	if !known {
		return
	}

	// Domains that need change-log coverage.
	needed := make([]string, 0, len(info.Owned)+1)
	needed = append(needed, info.Owned...)
	if a.HasPhase {
		needed = append(needed, DomainPhase)
	}

	if len(a.ChangeLog) == 0 {
		if len(needed) > 0 {
			e.add(a, Rule7, SeverityError,
				"missing change-log entries for owned domain(s): %s",
				strings.Join(needed, ", "))
		}
		return
	}

	// Per-entry well-formedness.
	for i, entry := range a.ChangeLog {
		switch entry.Domain {
		case DomainPhase:
			if !versionedTypes[a.Type] {
				e.add(a, Rule7, SeverityError,
					"change-log entry %d: `phase` entries are allowed only on scp-/plan- artifacts, not type %q",
					i+1, a.Type)
			}
		case DomainContentState, DomainExecutionState, DomainPlanningState, DomainContainerState, DomainExistenceState, DomainNoteState:
			if !contains(info.Owned, entry.Domain) {
				e.add(a, Rule7, SeverityError,
					"change-log entry %d: domain %q is not owned by type %q",
					i+1, entry.Domain, a.Type)
			}
		default:
			e.add(a, Rule7, SeverityError,
				"change-log entry %d: unknown domain %q", i+1, entry.Domain)
			continue
		}
		values := domainValues(entry.Domain, a.Type)
		if entry.From != "-" && !contains(values, entry.From) {
			e.add(a, Rule7, SeverityError,
				"change-log entry %d: `from` value %q is not valid for domain %q (allowed: %s)",
				i+1, entry.From, entry.Domain, strings.Join(values, ", "))
		}
		if entry.To == "-" || !contains(values, entry.To) {
			e.add(a, Rule7, SeverityError,
				"change-log entry %d: `to` value %q is not valid for domain %q (allowed: %s)",
				i+1, entry.To, entry.Domain, strings.Join(values, ", "))
		}
		if entry.By == "" {
			e.add(a, Rule7, SeverityError,
				"change-log entry %d: `by` must be a non-empty authority", i+1)
		}
	}

	// Last-entry == current value, one entry minimum, and transition
	// legality per domain.
	for _, domain := range needed {
		entries := entriesForDomain(a.ChangeLog, domain)
		current := ""
		if domain == DomainPhase {
			current = a.Phase
		} else {
			current = a.States[domain]
		}
		if current == "" {
			// Owned domain missing its field: R4 reports it; nothing
			// coherent to compare here.
			continue
		}
		if len(entries) == 0 {
			e.add(a, Rule7, SeverityError,
				"no change-log entry for domain %q (current value %q)", domain, current)
			continue
		}
		last := entries[len(entries)-1]
		if last.To != current {
			e.add(a, Rule7, SeverityError,
				"last change-log entry for %q ends at %q but the field currently holds %q",
				domain, last.To, current)
		}
		for _, entry := range entries {
			if entry.From == "-" {
				continue
			}
			if !isLegalTransition(entry.Domain, a.Type, entry.From, entry.To) {
				e.add(a, Rule7, SeverityError,
					"change-log entry %d: illegal transition %s %q -> %q for domain %q",
					indexOfEntry(a.ChangeLog, entry)+1, domain, entry.From, entry.To, domain)
			}
		}
	}
}

func entriesForDomain(log []ChangeLogEntry, domain string) []ChangeLogEntry {
	var out []ChangeLogEntry
	for _, entry := range log {
		if entry.Domain == domain {
			out = append(out, entry)
		}
	}
	return out
}

func indexOfEntry(log []ChangeLogEntry, target ChangeLogEntry) int {
	for i, entry := range log {
		if entry.Date == target.Date && entry.Domain == target.Domain &&
			entry.From == target.From && entry.To == target.To && entry.By == target.By {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Rule 8 — Single-writer & projections
// ---------------------------------------------------------------------------
// Tickets (tkt-) are pure projections: empty state vector (enforced by R4),
// derives-from the active container, and must carry the projection header.
// Containers whose `## Work Items` section holds a table must carry the
// header too, and each row's projected execution state is compared against
// the owner work item's state: mismatch is a WARNING (owner state is truth;
// projections are validated on read, ADR-003 §5).
//
// Interpretation (documented):
//   - The exact header line may appear anywhere in the file (validation.md
//     requires it "ada pada file proyeksi"); position is not enforced.
//   - A ticket must declare at least one `derives-from` reference that
//     resolves to a ctr- artifact (ADR-003 §3). Referencing the work item
//     itself is conventional but not required (projections/README.md shows
//     both, the rule text only the container).
//   - The `## Work Items` table is a GFM pipe table: a header row, a
//     separator row, then data rows. The first data cell is the work item
//     id or `<type>:<id>` reference; the execution-state column is found by
//     its header (execution-state / execution state / execution_state /
//     status). A table that cannot be parsed (no separator row, no
//     recognizable state column, unresolvable or ambiguous work item,
//     invalid projected value) yields warnings, never blocking errors,
//     because Rule 8 is warning-oriented for table content.

func (e *engine) rule8(a *Artifact) {
	switch a.Type {
	case "tkt":
		if !hasProjectionHeader(a) {
			e.add(a, Rule8, SeverityError,
				"ticket is a projection and must carry the projection header line exactly: %s", projectHeader)
		}
		hasCtr := false
		for _, raw := range a.Relations["derives-from"] {
			ref, err := parseReference(raw, a.Namespace, a.Type)
			if err != nil {
				continue // R5 reports the malformed reference.
			}
			// The reference's own type token identifies the target's
			// line (resolution is keyed by (namespace, type, id)), so a
			// container reference is exactly a resolving "ctr:" target
			// — the same semantics in repository and CKO mode.
			if ref.Type == "ctr" && e.resolveRef(ref) {
				hasCtr = true
			}
		}
		if !hasCtr {
			e.add(a, Rule8, SeverityError,
				"ticket must declare derives-from with at least one reference to a container (ctr-) artifact")
		}
	case "ctr":
		table := e.workItemsTable(a)
		if table == nil {
			return // No `## Work Items` table: nothing to compare.
		}
		if !hasProjectionHeader(a) {
			e.add(a, Rule8, SeverityError,
				"container with a `## Work Items` table is a projection and must carry the projection header line exactly: %s", projectHeader)
		}
		e.compareWorkItemsTable(a, table)
	}
}

// workItemsTable returns the parsed table inside `## Work Items` (or the
// JSON-native `workItems` content value), or nil.
type workItemsTable struct {
	header    []string
	rows      [][]string
	stateCol  int // column index of the execution-state column, or -1
	parseWarn string
}

func (e *engine) workItemsTable(a *Artifact) *workItemsTable {
	if a.ContentFields != nil {
		// JSON-native artifact: the Work Items section is the
		// `workItems` content value (a string holding the pipe table).
		v, ok := a.ContentFields[SectionKey("Work Items")]
		if !ok {
			return nil
		}
		s, ok := v.(string)
		if !ok {
			return nil // Non-string value: nothing to compare.
		}
		return parseWorkItemsLines(strings.Split(s, "\n"))
	}
	start := -1
	for i, line := range a.BodyLines {
		if headingMatches(line, "Work Items") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var pipeLines []string
	for _, line := range a.BodyLines[start+1:] {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			break
		}
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "|") {
			pipeLines = append(pipeLines, t)
		}
	}
	return parseWorkItemsLines(pipeLines)
}

// parseWorkItemsLines parses the GFM pipe table out of a section's lines
// (the shared shape of the Markdown and JSON paths).
func parseWorkItemsLines(lines []string) *workItemsTable {
	var pipeLines []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "|") {
			pipeLines = append(pipeLines, t)
		}
	}
	if len(pipeLines) < 2 {
		return nil // Section exists but has no table rows.
	}
	tbl := &workItemsTable{stateCol: -1}
	tbl.header = splitTableRow(pipeLines[0])
	if !isTableSeparator(pipeLines[1]) {
		tbl.parseWarn = "Work Items table is missing a valid separator row; rows are not compared"
		return tbl
	}
	for i, name := range tbl.header {
		switch normalizeHeader(name) {
		case "execution-state", "execution state", "execution_state", "status":
			tbl.stateCol = i
		}
	}
	if tbl.stateCol < 0 {
		tbl.parseWarn = "Work Items table has no recognizable execution-state column (execution-state/status); rows are not compared"
		return tbl
	}
	for _, line := range pipeLines[2:] {
		tbl.rows = append(tbl.rows, splitTableRow(line))
	}
	return tbl
}

// compareWorkItemsTable validates each row's projected execution state
// against the owner work item artifact. All findings are warnings: the
// owner state is the source of truth (validation.md Rule 8).
func (e *engine) compareWorkItemsTable(a *Artifact, tbl *workItemsTable) {
	if tbl.parseWarn != "" {
		e.add(a, Rule8, SeverityWarning, "%s", tbl.parseWarn)
		return
	}
	for _, row := range tbl.rows {
		if len(row) <= tbl.stateCol {
			e.add(a, Rule8, SeverityWarning,
				"Work Items table row %q has fewer cells than the header; row skipped", strings.Join(row, "|"))
			continue
		}
		idCell := strings.TrimSpace(row[0])
		stateCell := strings.TrimSpace(row[tbl.stateCol])
		if idCell == "" || stateCell == "" {
			continue
		}
		target := e.resolveWorkItemCell(idCell, a.Namespace)
		if target == nil {
			e.add(a, Rule8, SeverityWarning,
				"Work Items table row %q does not resolve to a work item artifact", idCell)
			continue
		}
		if !contains(executionStateValues, stateCell) {
			e.add(a, Rule8, SeverityWarning,
				"Work Items table row %q projects invalid execution state %q", idCell, stateCell)
			continue
		}
		owner := target.States[DomainExecutionState]
		// The owner-state comparison needs the owner artifact's actual
		// state. In CKO mode the resolved row is a synthetic marker
		// (the existence-only callback cannot observe the owner's
		// state), so the comparison is skipped there; repository mode
		// always carries the real owner state.
		if owner != "" && owner != stateCell {
			e.add(a, Rule8, SeverityWarning,
				"Work Items table projects %s for %s but the owner artifact holds %s (owner state is truth; refresh the projection)",
				stateCell, idCell, owner)
		}
	}
}

// resolveWorkItemCell resolves the first table cell of a row: either a
// `<type>:<id>` reference or a bare id searched across the six work item
// types in the container's namespace.
//
// CKO mode (ckoResolve set): resolution goes through the runtime
// callback. A colon cell resolves when its type token is a work item
// and the callback reports it resolves; a bare cell resolves when
// exactly one of the six work item types resolves (the ambiguity rule
// of the index path). The result is a synthetic marker artifact: it
// carries the resolved type but no state, because the owner's
// execution state is not observable through an existence-only
// callback (compareWorkItemsTable skips the owner comparison for it).
func (e *engine) resolveWorkItemCell(cell, ns string) *Artifact {
	if strings.Contains(cell, ":") {
		ref, err := parseReference(cell, ns, "")
		if err != nil {
			return nil
		}
		if !workItemTypes[ref.Type] {
			return nil
		}
		if e.ckoResolve != nil {
			if e.resolveRef(ref) {
				return &Artifact{Type: ref.Type, ID: ref.ID, Namespace: ns, States: map[string]string{}}
			}
			return nil
		}
		target := e.resolve(ref)
		if target != nil {
			return target
		}
		return nil
	}
	if e.ckoResolve != nil {
		var found string
		for t := range workItemTypes {
			if e.resolveRef(reference{Namespace: ns, Type: t, ID: cell}) {
				if found != "" {
					return nil // ambiguous: more than one type resolves
				}
				found = t
			}
		}
		if found == "" {
			return nil
		}
		return &Artifact{Type: found, ID: cell, Namespace: ns, States: map[string]string{}}
	}
	var matches []*Artifact
	for t := range workItemTypes {
		if bucket := e.byLine[identityKey(ns, t, cell)]; len(bucket) > 0 {
			matches = append(matches, bucket...)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return nil // none or ambiguous
}

// hasProjectionHeader reports whether the exact projection header line
// appears in the artifact: in the body for Markdown artifacts, or in any
// string content value for JSON-native artifacts (position is not
// enforced in either adapter — validation.md allows the header anywhere
// in the file).
func hasProjectionHeader(a *Artifact) bool {
	if a.ContentFields != nil {
		for _, v := range a.ContentFields {
			if contentValueHasHeader(v) {
				return true
			}
		}
		return false
	}
	for _, line := range a.BodyLines {
		if strings.TrimRight(line, "\r") == projectHeader {
			return true
		}
	}
	return false
}

// contentValueHasHeader searches a content value recursively (strings and
// nested objects) for the exact projection header line.
func contentValueHasHeader(v any) bool {
	switch t := v.(type) {
	case string:
		for _, line := range strings.Split(t, "\n") {
			if strings.TrimRight(line, "\r") == projectHeader {
				return true
			}
		}
	case map[string]any:
		for _, val := range t {
			if contentValueHasHeader(val) {
				return true
			}
		}
	}
	return false
}

// splitTableRow splits a GFM pipe row into trimmed cells.
func splitTableRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	raw := strings.Split(s, "|")
	cells := make([]string, 0, len(raw))
	for _, c := range raw {
		cells = append(cells, strings.TrimSpace(c))
	}
	return cells
}

// isTableSeparator reports whether a line is a GFM separator row.
func isTableSeparator(line string) bool {
	cells := splitTableRow(line)
	if len(cells) == 0 {
		return false
	}
	any := false
	for _, c := range cells {
		c = strings.Trim(c, ":")
		if c == "" {
			continue
		}
		for _, r := range c {
			if r != '-' {
				return false
			}
		}
		any = true
	}
	return any
}

// normalizeHeader lowercases and collapses whitespace for matching.
func normalizeHeader(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// ---------------------------------------------------------------------------
// Rule 9 — Well-formedness
// ---------------------------------------------------------------------------
// Required content sections per artifact family (validation.md Rule 9
// table), plus: an adr- artifact with content-state `superseded` must be
// referenced by its replacement via `supersedes`.
//
// Interpretation (documented):
//   - validation.md lists fnd- in both the "Knowledge doc" row (Purpose,
//     Content) and its own "Research Finding" row (Purpose, Content,
//     Investigation Summary, Conclusion). The dedicated row wins: it is
//     more specific, and research/README.md confirms the four-section
//     structure. fnd- therefore requires all four sections.
//   - A required section matches a line starting with "## " whose heading
//     text equals the section name or starts with it plus a space (e.g.
//     "## Scope (v2)" counts as Scope).
//   - A superseded ADR's replacement is any artifact whose `supersedes`
//     list contains a reference that resolves to this ADR's identity line;
//     a versioned reference must name the exact instance, a line reference
//     matches any instance.

// RequiredSectionsFor returns the mandatory content sections of an artifact
// type family (Rule 9), or nil for unknown types. Exported for the
// exchange/ import engine, which validates package unit content against the
// same section table before commit.
func RequiredSectionsFor(typeToken string) []string {
	return requiredSectionsFor(typeToken)
}

func requiredSectionsFor(typeToken string) []string {
	switch typeToken {
	case "scp", "epc", "plan", "trc":
		return []string{"Objective", "Scope", "Out of Scope"}
	case "sto", "ts", "ch":
		return []string{"Description", "Acceptance Criteria"}
	case "bug":
		return []string{"Description", "Impact"}
	case "td":
		return []string{"Description", "Acceptance Criteria", "Debt Rationale"}
	case "spk":
		return []string{"Description", "Investigation Notes", "Conclusion"}
	case "adr", "dec":
		return []string{"Context", "Decision", "Consequences", "Alternatives Considered"}
	case "vis", "str", "req", "arc", "spec", "std", "run", "rel", "gls", "mbr":
		return []string{"Purpose", "Content"}
	case "fnd":
		return []string{"Purpose", "Content", "Investigation Summary", "Conclusion"}
	case "rvw":
		return []string{"Purpose", "Content", "Findings", "Action Items"}
	case "ctr":
		return []string{"Objective", "Work Items", "Change Log"}
	case "tkt":
		return []string{"Commands", "Projected Status"}
	case "ses":
		return []string{"Context", "Notes", "Verification"}
	default:
		return nil
	}
}

// headingMatches reports whether line is a level-2 heading for name.
func headingMatches(line, name string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "## ") {
		return false
	}
	text := strings.TrimSpace(strings.TrimPrefix(t, "## "))
	return text == name || strings.HasPrefix(text, name+" ")
}

func (e *engine) rule9(a *Artifact) {
	required := requiredSectionsFor(a.Type)
	if required != nil {
		if a.ContentFields != nil {
			// JSON-native artifact: the required-section check is a key
			// check against the structured content object (spec-standard
			// -v2 §7 — R9 becomes a key check; keys are camelCase per
			// SectionKey).
			for _, section := range required {
				if _, ok := a.ContentFields[SectionKey(section)]; !ok {
					e.add(a, Rule9, SeverityError,
						"missing required content section %q for type %q", SectionKey(section), a.Type)
				}
			}
		} else {
			for _, section := range required {
				found := false
				for _, line := range a.BodyLines {
					if headingMatches(line, section) {
						found = true
						break
					}
				}
				if !found {
					e.add(a, Rule9, SeverityError,
						"missing required content section %q for type %q", section, a.Type)
				}
			}
		}
	}

	// Superseded ADR must have a replacement referencing it.
	if a.Type == "adr" && a.States[DomainContentState] == "superseded" {
		if !e.hasReplacement(a) {
			e.add(a, Rule9, SeverityError,
				"superseded ADR %s must be referenced by a replacement via `supersedes`", a.ID)
		}
	}
}

// hasReplacement reports whether any other artifact's `supersedes` list
// resolves to a's identity. Resolution goes through the shared
// resolveRef semantics (the engine index in repository mode, the
// runtime callback in CKO mode); the identity comparison uses the
// parsed reference, which names the target line exactly like the
// resolved artifact would (resolution is keyed by the reference's
// namespace/type/id).
//
// CKO-mode limitation (documented): the callback is existence-only, so
// replacement candidates can only come from the unit set being
// validated — which in CKO mode holds the unit itself. A superseded
// adr- draft therefore cannot verify its replacement exists in the
// runtime and is conservatively refused; the replacement direction
// (a draft superseding an existing published adr-) is unaffected.
func (e *engine) hasReplacement(a *Artifact) bool {
	for _, b := range e.artifacts {
		if b == a {
			continue
		}
		for _, raw := range b.Relations["supersedes"] {
			ref, err := parseReference(raw, b.Namespace, b.Type)
			if err != nil {
				continue // R5 reports it on b.
			}
			if !e.resolveRef(ref) {
				continue
			}
			if ref.Namespace == a.Namespace && ref.Type == a.Type && ref.ID == a.ID {
				if !ref.HasVersion || ref.Version == a.InstanceVersion {
					return true
				}
			}
		}
	}
	return false
}
