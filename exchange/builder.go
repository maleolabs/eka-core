package exchange

import (
	"fmt"
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
)

// This file implements scope resolution and model construction (RSF §13.1
// steps 2-5): the Export Scope is resolved deterministically from the
// target set (Exchange §12.1), the unit set is selected (Exchange §5.1),
// External References are detected and declared (Exchange §12.3), and the
// RSF object model is assembled.
//
// v1 scope semantics (no closure computation — dependency integrity via
// External Reference Declarations, Exchange §12.3/§12.4):
//
//	no targets                    -> Repository scope: all instances of all lines
//	one target  <type>:<id>       -> Line scope: all instances of that line
//	one target  <type>:<id>:<v>   -> Instance scope: exactly one instance
//	several targets               -> Collection scope: union of the resolved
//	                                 lines/instances
//
// Targets use the validation.md rule 5 reference grammar
// (conformance.ParseReference): "<type>:<id>[:<instance-version>]" or
// cross-namespace "<ns>/<type>:<id>[:<version>]". A target without a
// namespace resolves against the repository's lines of that type/id; when
// several namespaces hold the same (type, id) the target is ambiguous and
// rejected.

// built is the assembled model of one export run, before serialization.
type built struct {
	scope       ScopeKind
	seeds       []string
	namespace   string
	label       string
	units       []*Unit // ordered by canonical identity key
	attachments []*Attachment
	externals   []ExternalReference
	unitSet     map[string]bool // canonical identity form -> in package
}

// targetSpec is one parsed export target.
type targetSpec struct {
	raw  string
	ref  conformance.Reference
	line string // line key of the resolved line
}

// UsageError is a deterministic usage error (exit code 2): malformed
// targets, unknown artifact lines, or an explicit Options.Scope that
// contradicts the target-derived scope.
type UsageError struct{ msg string }

func (e *UsageError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return &UsageError{msg: fmt.Sprintf(format, args...)}
}

// build loads the repository and constructs the package model for the
// given targets. The caller (Export) has already run the conformance
// validation gate, so dangling references cannot occur here (they would
// have blocked the export; draft tolerance only produces warnings).
func build(root string, opts Options) (*built, error) {
	repo, err := load(root)
	if err != nil {
		return nil, err
	}

	scope, seeds, unitSet, err := resolveScope(repo, opts)
	if err != nil {
		return nil, err
	}

	b := &built{
		scope:     scope,
		seeds:     seeds,
		unitSet:   unitSet,
		externals: []ExternalReference{}, // never nil: JSON encodes [] not null.
	}

	// Unit selection: canonical identity forms in package order.
	var forms []string
	for form := range unitSet {
		forms = append(forms, form)
	}
	sort.Strings(forms)
	for _, form := range forms {
		la := repo.instanceByForm[form]
		if la == nil {
			return nil, fmt.Errorf("internal: selected unit %s has no loaded artifact", form)
		}
		b.units = append(b.units, toUnit(la))
	}
	// Package label namespace (RSF §4.1).
	namespaces := map[string]bool{}
	for _, u := range b.units {
		namespaces[u.Identity.Namespace] = true
	}
	nsList := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		nsList = append(nsList, ns)
	}
	sort.Strings(nsList)
	b.namespace, err = labelNamespace(nsList)
	if err != nil {
		return nil, err
	}
	b.label = PackageIdentityLabel(scope, b.namespace)

	// Attachments: every non-.md file under docs/ (policy in load.go).
	ids := make([]string, 0, len(repo.attachments))
	for id := range repo.attachments {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b.attachments = append(b.attachments, &Attachment{ID: id, Data: repo.attachments[id]})
	}

	// External Reference detection (Exchange §12.3): every relationship
	// target not carried by the package.
	seen := map[string]bool{}
	for _, u := range b.units {
		for _, rel := range u.Relationships {
			if b.unitSet[rel.Target] {
				continue
			}
			ext := ExternalReference{Source: u.CanonicalIdentityForm, Type: rel.Type, Target: rel.Target}
			key := ext.Source + "\x00" + ext.Type + "\x00" + ext.Target
			if !seen[key] {
				seen[key] = true
				b.externals = append(b.externals, ext)
			}
		}
	}
	sort.Slice(b.externals, func(i, j int) bool {
		if b.externals[i].Source != b.externals[j].Source {
			return b.externals[i].Source < b.externals[j].Source
		}
		if b.externals[i].Type != b.externals[j].Type {
			return b.externals[i].Type < b.externals[j].Type
		}
		return b.externals[i].Target < b.externals[j].Target
	})

	return b, nil
}

// seedEntry is one resolved target's seed contribution: the canonical seed
// spelling plus the line key it belongs to (used for deduplication).
type seedEntry struct {
	// form is the normalized canonical seed spelling.
	form string
	// line is the line key ("<namespace>/<type>:<id>") the seed belongs
	// to; instance seeds and line seeds of the same line share it.
	line string
	// isLine reports whether the seed is a line-level (unversioned)
	// reference; instance seeds are versioned.
	isLine bool
}

// resolveScope derives the scope kind from the targets, resolves each
// target to its line/instance set, and returns the scope, the seed set
// (normalized canonical reference forms, sorted) and the selected unit
// set keyed by canonical identity form.
func resolveScope(repo *loadedRepo, opts Options) (ScopeKind, []string, map[string]bool, error) {
	derived, err := deriveScope(opts.Targets)
	if err != nil {
		return "", nil, nil, err
	}
	if opts.Scope != "" && opts.Scope != derived {
		return "", nil, nil, usageErrorf(
			"declared scope %q contradicts the target-derived scope %q", opts.Scope, derived)
	}

	unitSet := map[string]bool{}
	var seedEntries []seedEntry

	switch derived {
	case ScopeRepository:
		for _, la := range repo.instances() {
			unitSet[la.IdentityForm()] = true
		}
	case ScopeLine, ScopeInstance, ScopeCollection:
		for _, raw := range opts.Targets {
			spec, err := parseTarget(repo, raw)
			if err != nil {
				return "", nil, nil, err
			}
			if spec.ref.HasVersion {
				la := repo.resolveRef(spec.ref)
				if la == nil {
					return "", nil, nil, targetNotFoundError(repo, spec.ref)
				}
				unitSet[la.IdentityForm()] = true
				seedEntries = append(seedEntries, seedEntry{
					form: la.IdentityForm(),
					line: lineSeedForm(spec.ref),
				})
			} else {
				// Line scope: all instances of the line.
				bucket := repo.byLine[spec.line]
				if len(bucket) == 0 {
					return "", nil, nil, targetNotFoundError(repo, spec.ref)
				}
				for _, la := range bucket {
					unitSet[la.IdentityForm()] = true
				}
				seedEntries = append(seedEntries, seedEntry{
					form:   lineSeedForm(spec.ref),
					line:   lineSeedForm(spec.ref),
					isLine: true,
				})
			}
		}
	default:
		return "", nil, nil, usageErrorf("unknown scope %q", derived)
	}

	return derived, normalizeSeeds(seedEntries), unitSet, nil
}

// normalizeSeeds renders the deterministic, deduplicated closure seed set:
// exact duplicates collapse, and when the same line is specified both as a
// line reference and as instance references, the instance forms win — the
// line seed is dropped because every instance seed is more precise and the
// selected unit set is unchanged (the line seed selected the union, the
// instance seeds select its members). Returns a non-nil slice (JSON encodes
// empty seed sets as [], not null).
func normalizeSeeds(entries []seedEntry) []string {
	hasInstance := map[string]bool{}
	for _, e := range entries {
		if !e.isLine {
			hasInstance[e.line] = true
		}
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.isLine && hasInstance[e.line] {
			continue
		}
		out = append(out, e.form)
	}
	sort.Strings(out)
	return dedupeStrings(out)
}

// deriveScope maps the target count to the scope kind (Exchange §5.3).
func deriveScope(targets []string) (ScopeKind, error) {
	switch len(targets) {
	case 0:
		return ScopeRepository, nil
	case 1:
		ref, err := conformance.ParseReference(targets[0], "", "")
		if err != nil {
			return "", usageErrorf("invalid export target %q: %v", targets[0], err)
		}
		if ref.HasVersion {
			return ScopeInstance, nil
		}
		return ScopeLine, nil
	default:
		return ScopeCollection, nil
	}
}

// parseTarget parses one target and resolves it to a line. Targets may be
// same-namespace ("<type>:<id>[:<v>]") or cross-namespace
// ("<ns>/<type>:<id>[:<v>]"). Same-namespace targets are resolved across
// the repository's lines: when several namespaces hold the same (type, id)
// the target is ambiguous and rejected with the candidate list.
func parseTarget(repo *loadedRepo, raw string) (targetSpec, error) {
	ref, err := conformance.ParseReference(raw, "", "")
	if err != nil {
		return targetSpec{}, usageErrorf("invalid export target %q: %v", raw, err)
	}
	if ref.Namespace != "" {
		return targetSpec{raw: raw, ref: ref, line: identityLineKey(ref.Namespace, ref.Type, ref.ID)}, nil
	}
	// Same-namespace target: find the lines with this (type, id).
	var candidates []string
	for _, la := range repo.instances() {
		if la.Type == ref.Type && la.ID == ref.ID {
			candidates = append(candidates, la.Namespace)
		}
	}
	if len(candidates) == 0 {
		return targetSpec{}, targetNotFoundError(repo, ref)
	}
	sort.Strings(candidates)
	candidates = dedupeStrings(candidates)
	if len(candidates) > 1 {
		return targetSpec{}, usageErrorf(
			"export target %q is ambiguous: artifact %s:%s exists in namespaces %s; use <namespace>/<type>:<id>",
			raw, ref.Type, ref.ID, strings.Join(candidates, ", "))
	}
	ref.Namespace = candidates[0]
	return targetSpec{raw: raw, ref: ref, line: identityLineKey(ref.Namespace, ref.Type, ref.ID)}, nil
}

// targetNotFoundError builds the deterministic not-found message, listing
// available artifacts of the target's type when any exist.
func targetNotFoundError(repo *loadedRepo, ref conformance.Reference) error {
	msg := fmt.Sprintf("export target %s:%s:%d does not exist in namespace %q",
		ref.Type, ref.ID, ref.Version, ref.Namespace)
	if ref.Namespace == "" {
		msg = fmt.Sprintf("export target %s:%s does not exist", ref.Type, ref.ID)
	}
	// List available artifacts of that type (helpful diagnostics).
	var ids []string
	seen := map[string]bool{}
	for _, la := range repo.instances() {
		if la.Type == ref.Type {
			form := la.Namespace + "/" + la.Type + ":" + la.ID
			if !seen[form] {
				seen[form] = true
				ids = append(ids, form)
			}
		}
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		msg += "; available " + ref.Type + " artifacts: " + strings.Join(ids, ", ")
	}
	return usageErrorf("%s", msg)
}

// toUnit projects one loaded artifact onto the RSF Unit composition
// (Exchange §4.4, RSF §5.1). Reference resolution uses the repository
// index (repo).
func toUnit(la *loadedArtifact) *Unit {
	id := Identity{
		Namespace:       la.Namespace,
		Type:            la.Type,
		ID:              la.ID,
		InstanceVersion: la.InstanceVersion,
	}
	classification := Classification{}
	if la.HasDimension {
		classification.Dimension = la.Dimension
	}
	if len(la.DimensionsSecondary) > 0 {
		classification.DimensionsSecondary = la.DimensionsSecondary
	}
	// Engineering Domain: derived from the type token via the shared
	// conformance mapping (single source of truth, like ParseReference).
	if d, ok := conformance.DomainForToken(la.Type); ok {
		classification.Domain = string(d)
	}
	u := &Unit{
		Identity:              id,
		CanonicalIdentityForm: id.CanonicalForm(),
		Revision:              la.Revision,
		Author:                conformance.AuthorIdentity{Kind: la.AuthorKind, Name: la.Author},
		Created:               la.Created,
		Updated:               la.Updated,
		StateVector:           stateVectorFrom(la.States),
		Classification:        classification,
		Phase:                 la.Phase,
		// The unit content is the compiled structured-json payload
		// (spec-standard-v2 §3.3, §6); legacy structured-text payloads
		// remain readable at import but are never emitted.
		Content:        ContentRef{Representation: StructuredJSON, File: "content"},
		ContentPayload: la.content,
		// Never nil: empty collections encode as [] in JSON, not null.
		ChangeLog:     []ChangeLogEntry{},
		Relationships: []Relationship{},
	}
	for _, entry := range la.ChangeLog {
		u.ChangeLog = append(u.ChangeLog, ChangeLogEntry{
			Date: entry.Date, Domain: entry.Domain,
			From: entry.From, To: entry.To, By: conformance.AuthorIdentity{Kind: entry.ByKind, Name: entry.By},
		})
	}
	// Relationships: every resolvable reference, ordered by (type, target).
	// Unresolvable references (draft tolerance) carry no resolvable
	// Identity and are therefore not serializable as relationships
	// (Exchange §7.1 requires Identity expression); the validator records
	// them as draft warnings. Documented decision.
	rels := map[string]string{} // dedupe key -> target form
	for field, raws := range la.Relations {
		for _, raw := range raws {
			ref, err := conformance.ParseReference(raw, la.Namespace, la.Type)
			if err != nil {
				continue // The validation gate already reported it (R5).
			}
			target := la.repo.resolveRef(ref)
			if target == nil {
				continue // Draft tolerance: no resolvable identity.
			}
			rels[field+"\x00"+target.IdentityForm()] = target.IdentityForm()
		}
	}
	type relKey struct{ t, target string }
	var ordered []relKey
	for key, target := range rels {
		parts := strings.SplitN(key, "\x00", 2)
		ordered = append(ordered, relKey{t: parts[0], target: target})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].t != ordered[j].t {
			return ordered[i].t < ordered[j].t
		}
		return ordered[i].target < ordered[j].target
	})
	for _, r := range ordered {
		u.Relationships = append(u.Relationships, Relationship{Type: r.t, Target: r.target})
	}
	return u
}

// stateVectorFrom maps the present state domains onto the fixed-order
// StateVector. Only owned domains are present (the validation gate
// guarantees owned-set compliance, rule 4); empty-vector types yield an
// empty vector (RSF §5.1.1).
func stateVectorFrom(states map[string]string) StateVector {
	return StateVector{
		ContentState:   states[conformance.DomainContentState],
		ExecutionState: states[conformance.DomainExecutionState],
		PlanningState:  states[conformance.DomainPlanningState],
		ContainerState: states[conformance.DomainContainerState],
		ExistenceState: states[conformance.DomainExistenceState],
		NoteState:      states[conformance.DomainNoteState],
	}
}

// lineSeedForm is the normalized seed spelling of a line-level target:
// the cross-namespace line reference (RSF discipline: no component is
// defaulted — the namespace is always present).
func lineSeedForm(ref conformance.Reference) string {
	return ref.Namespace + "/" + ref.Type + ":" + ref.ID
}

// dedupeStrings removes duplicates from a sorted slice.
func dedupeStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
