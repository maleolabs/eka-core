package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
)

// This file implements the Relate API: the relationship-only edge-add
// of the Authoring API. `eka relate <line> --depends-on X` adds edges
// to an EXISTING artifact WITHOUT a full re-publish — in particular
// without a new instance version (the instance-churn acceptance of
// sto:eka-relate).
//
// Where relationships live (store model): relationships are serialized
// inside the immutable unit payload (unit.json); the v2 schema removed
// the separate relationships table (store/schema.go). Every change to
// an artifact therefore produces a NEW immutable payload row in the
// content-addressed archive — there is no in-place update path for
// payload rows by design (store/payloads.go). The MUTABLE part is the
// reference (object_refs): it may move from one immutable payload to
// another, and its index columns (instance_version, revision) are
// written by the caller, never derived. Transition already uses this
// mechanism: a state transition re-points the reference to a new
// payload with the SAME canonical form and the SAME instance version
// (runtime/transition.go publishStateTransition).
//
// Relate follows the same pattern: the line's reference moves to a new
// payload whose only differences are the added edges (and the Updated
// date), while instance_version AND revision stay unchanged. That is
// the no-churn mechanism: the payload archive gains one immutable row
// (inherent to content-addressing — history is accumulation), but the
// artifact line does NOT advance — no new instance version, no revision
// bump. The old payload remains reachable through the prev_hash lineage
// (the ADR-011 chain of immutable units) and integrity stays intact.
//
// Drafts: relate applies to pending drafts too. A line with no
// published instance that has a pending draft gets its edge added to
// the draft file in place (an edge-add before publish); a published
// line always wins when both exist (the draft is a separate, future
// instance). Legacy Markdown drafts are refused (they have no
// deterministic in-place mutation path; edit the file or migrate to
// the JSON draft format).
//
// Semantics (documented decisions):
//
//   - Unknown relationship type: refused (mirror of Rule 5).
//   - Self-reference: refused (mirror of Rule 5: a target resolving to
//     the artifact's own line, optionally pinned to its own instance).
//   - Duplicate edge: idempotent — edges are a set. A relate whose
//     edges are all already present writes NOTHING (no new payload,
//     zero churn). Partially-present edges add only the missing ones.
//   - Missing target resolution: the published path runs the standard
//     publish validation (ValidateCKO with the store resolver), so the
//     Rule 5 draft tolerance applies unchanged: an unresolved target is
//     a warning while the artifact's content-state is draft, an error
//     otherwise (a pending-draft target is therefore tolerated on a
//     draft-state artifact). The draft path tolerates unresolved targets
//     by design — the edge lands before publish, and publish re-checks.
//   - Change log: no entry is appended — relationships have no
//     change-log domain in the §3.2 schema, and relate is a structural
//     edge update, not a state transition. The unit's Updated date is
//     refreshed (mirrors transition).
//   - Provenance: the existing reference's (project_id, source_repo) is
//     preserved — relate does not change where the object came from.
//   - Target forms: relate addresses the artifact LINE. Canonical
//     published forms (carrying an instance-version suffix) are refused.

// RelateRequest describes one edge-add to an artifact line.
type RelateRequest struct {
	// RepoPath is the directory the repository is addressed from (the
	// walk-up locates the eka.yaml repository root — ADR-018). It
	// resolves the namespace of an UNQUALIFIED target only; a
	// qualified target carries its namespace. "" = the current working
	// directory.
	RepoPath string
	// Target is the artifact line: "<ns>/<type>:<id>" (qualified) or
	// "<type>:<id>" (unqualified — the repository namespace applies).
	// A canonical published form (with an instance-version suffix) is
	// refused.
	Target string
	// Relationships are the edges to add. Duplicates (same type +
	// target) are idempotent. Empty targets are skipped.
	Relationships []exchange.Relationship
}

// RelateResult is the deterministic outcome of one relate run.
type RelateResult struct {
	// Target is the canonical line form of the related artifact
	// ("<namespace>/<type>:<id>").
	Target string
	// State reports what was related: "published" (the line's current
	// instance was re-pointed), "draft" (a pending draft was mutated),
	// or "unchanged" (every requested edge was already present —
	// nothing was written).
	State string
	// InstanceVersion is the instance version of the published
	// artifact AFTER the relate — IDENTICAL to the version before it
	// (relate never bumps the instance; this is the no-churn proof the
	// acceptance asserts). 0 on the draft/unchanged paths.
	InstanceVersion int
	// ObjectHash is the content-derived hash of the new immutable
	// payload the reference now points at ("" when nothing was
	// written).
	ObjectHash string
	// Added lists the edges actually added, in canonical (type,
	// target) order (empty when every requested edge was already
	// present).
	Added []exchange.Relationship
	// DraftValidation is set on the draft path: the post-mutation
	// CKO-level re-validation (the same non-destructive validation
	// `eka edit` runs). Findings are reported, never destructive.
	DraftValidation *DraftValidation
}

// RelateRefusal is a deterministic relate refusal carrying the
// user-facing reason and hint (exit 1 class: self-reference, unknown
// relationship type, malformed reference, missing artifact, unresolved
// namespace, Markdown draft). Nothing was written.
type RelateRefusal struct {
	Reason string
	Hint   string
}

// Error renders the deterministic refusal message.
func (e *RelateRefusal) Error() string {
	return fmt.Sprintf("relate refused: %s; %s", e.Reason, e.Hint)
}

// RelateValidationError reports that the would-be unit failed CKO-level
// validation (the standard publish gate); nothing was written. The
// Report is carried so the caller renders the findings.
type RelateValidationError struct {
	// Target is the line form the relate addressed.
	Target string
	// Report is the CKO-level validation report.
	Report *conformance.Report
}

// Error renders the deterministic refusal message.
func (e *RelateValidationError) Error() string {
	return fmt.Sprintf("relate refused: %s failed CKO-level validation with %d blocking error(s); nothing was changed",
		e.Target, e.Report.ErrorCount())
}

// Relate adds relationship edges to an artifact line. Pipeline
// (deterministic):
//
//  1. resolve the target line form (versioned forms refused) and the
//     namespace (qualified target, else the repository context);
//  2. resolve the line's current state in the canonical store: a
//     published line relates its current instance; else a pending JSON
//     draft relates the draft file; else refusal;
//  3. check the requested edges structurally (unknown type, malformed
//     reference, self-reference — the Rule 5 mirrors); a violation is
//     a deterministic refusal, nothing is written;
//  4. published path: merge the edges into the current unit (set
//     semantics, canonical order), run the standard publish validation
//     (ValidateCKO with the store resolver — the Rule 5 draft
//     tolerance applies), then re-point the reference via store.PutUnit
//     with the SAME canonical form, instance version and revision; the
//     previous payload stays in the archive (prev_hash lineage);
//  5. draft path: merge the edges into the draft file's relationships
//     block (deterministic rewrite), then re-validate the draft at CKO
//     level (non-destructive, mirroring `eka edit`).
//
// A relate whose edges are all already present writes nothing (State =
// "unchanged").
func (AuthoringService) Relate(rt *Runtime, req RelateRequest) (*RelateResult, error) {
	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	st, err := rt.requireStore()
	if err != nil {
		return nil, err
	}
	ref, err := conformance.ParseReference(req.Target, "", "")
	if err != nil {
		return nil, fmt.Errorf("relate: invalid target %q: %w", req.Target, err) // Exit 2: usage.
	}
	if ref.HasVersion {
		return nil, fmt.Errorf("relate: %s is a canonical published form; relate addresses the artifact line", req.Target) // Exit 2: usage.
	}
	ns := ref.Namespace
	if ns == "" {
		// Unqualified target: the namespace resolves from the
		// repository context (the ADR-018 walk-up, then the registered
		// repository's default namespace) — the same resolution `eka
		// new` and `eka transition` use.
		ns, err = relateNamespace(rt, req.RepoPath)
		if err != nil {
			return nil, &RelateRefusal{Reason: err.Error(), Hint: "run inside a registered repository, or qualify the target as <ns>/<type>:<id>"}
		}
		ref.Namespace = ns
	}

	// The line's current state: the published instances first; the
	// pending draft is the fallback (a published line always wins — the
	// draft is a separate, future instance).
	line, err := st.UnitsByLine(ns, ref.Type, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("relate: %w", err)
	}
	if len(line) > 0 {
		return relatePublished(st, ref, line, req.Relationships)
	}
	return relateDraft(ws, rt, ref, ns, req.Relationships)
}

// relateNamespace resolves the namespace of an unqualified relate
// target from the repository context: the walk-up that carries eka.yaml
// (ADR-018), then the registered repository's default namespace.
func relateNamespace(rt *Runtime, repoPath string) (string, error) {
	root, meta, err := resolveRepoContext(repoPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve a namespace here (%v)", err)
	}
	repo, found, err := rt.Workspace.FindRepo(root)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("cannot resolve a namespace here (the repository is not registered in the workspace)")
	}
	if meta.Namespace == "" && repo.Namespace == "" {
		return "", fmt.Errorf("cannot resolve a namespace here (the repository carries no namespace)")
	}
	if meta.Namespace != "" {
		return meta.Namespace, nil
	}
	return repo.Namespace, nil
}

// relatePublished performs the published path of relate: re-point the
// line's current instance to a new immutable payload carrying the added
// edges, with the SAME canonical form, instance version and revision —
// no instance churn.
func relatePublished(st *store.Store, ref conformance.Reference, line []*exchange.Unit, adds []exchange.Relationship) (*RelateResult, error) {
	// The current state of the line: the highest instance.
	current := line[0]
	for _, u := range line {
		if u.Identity.InstanceVersion > current.Identity.InstanceVersion {
			current = u
		}
	}
	lineForm := ref.Namespace + "/" + ref.Type + ":" + ref.ID

	// Structural checks (Rule 5 mirrors): unknown type, malformed
	// reference, self-reference — refused BEFORE anything is written.
	if err := checkRelateEdges(current.Identity.Namespace, current.Identity.Type, current.Identity.ID, current.Identity.InstanceVersion, adds); err != nil {
		return nil, err
	}

	// Merge (set semantics, canonical (type, target) order); added =
	// the requested edges not already present.
	merged, added := mergeRelationships(current.Relationships, adds)
	if len(added) == 0 {
		return &RelateResult{Target: lineForm, State: "unchanged", InstanceVersion: current.Identity.InstanceVersion}, nil
	}

	// The would-be unit: only the relationships change (plus the
	// Updated date, mirroring transition).
	next := *current
	next.Relationships = merged
	next.Updated = time.Now().Format("2006-01-02")

	// The standard publish gate: ValidateCKO with the store resolver,
	// so Rule 5's draft tolerance applies to unresolved targets.
	resolver := newStoreResolver(st)
	report, err := conformance.ValidateCKO(&next, conformance.ValidateCKOOptions{
		Resolve: resolver.Resolve,
	})
	if err != nil {
		return nil, fmt.Errorf("relate: validation failed: %w", err)
	}
	report.Results = append(report.Results,
		resolver.Findings(next.CanonicalIdentityForm, next.StateVector.ContentState)...)
	if !report.Pass() {
		return nil, &RelateValidationError{Target: lineForm, Report: report}
	}

	// The reference is the mutable part: re-point it with the SAME
	// identity, instance version and revision (the no-churn mechanism —
	// see the package comment). Provenance (project_id, source_repo) is
	// preserved from the current reference.
	curRef, ok, err := st.Ref(current.CanonicalIdentityForm)
	if err != nil {
		return nil, fmt.Errorf("relate: %w", err)
	}
	if !ok {
		return nil, &RelateRefusal{Reason: fmt.Sprintf("the reference of %s is missing (store corruption)", current.CanonicalIdentityForm), Hint: "run 'eka integrity check'"}
	}
	unitJSON, err := exchange.MarshalUnit(&next)
	if err != nil {
		return nil, fmt.Errorf("relate: cannot serialize %s: %w", next.CanonicalIdentityForm, err)
	}
	hash, _, err := st.PutUnit(unitJSON, next.ContentPayload, store.Ref{
		Form:            next.CanonicalIdentityForm,
		ProjectID:       curRef.ProjectID,
		SourceRepo:      curRef.SourceRepo,
		Namespace:       next.Identity.Namespace,
		Type:            next.Identity.Type,
		ID:              next.Identity.ID,
		InstanceVersion: next.Identity.InstanceVersion,
		Revision:        next.Revision,
		Dimension:       next.Classification.Dimension,
		Domain:          next.Classification.Domain,
		Phase:           next.Phase,
		UpdatedAt:       next.Updated,
	})
	if err != nil {
		return nil, fmt.Errorf("relate: cannot publish %s: %w", next.CanonicalIdentityForm, err)
	}
	return &RelateResult{
		Target:          lineForm,
		State:           "published",
		InstanceVersion: next.Identity.InstanceVersion,
		ObjectHash:      hash,
		Added:           added,
	}, nil
}

// relateDraft performs the draft path of relate: mutate a pending JSON
// draft's relationships block in place (an edge-add before publish).
// The published path wins when a published instance exists, so this
// runs only for lines with no published instance. Legacy Markdown
// drafts are refused (no deterministic in-place mutation path).
func relateDraft(ws *workspace.Workspace, rt *Runtime, ref conformance.Reference, ns string, adds []exchange.Relationship) (*RelateResult, error) {
	df, err := resolveDraftFile(ws, rt, "", ref.Type, ref.ID)
	if err != nil {
		var dne *DraftNotFoundError
		if errors.As(err, &dne) {
			return nil, &RelateRefusal{
				Reason: fmt.Sprintf("artifact line %s/%s:%s has no published instance and no pending draft", ns, ref.Type, ref.ID),
				Hint:   "run 'eka new <type>:<id>' to scaffold a draft, or publish the pending draft first",
			}
		}
		return nil, fmt.Errorf("relate: %w", err)
	}
	if strings.HasSuffix(df.Path, ".md") {
		return nil, &RelateRefusal{
			Reason: fmt.Sprintf("draft %s:%s is a legacy Markdown draft, which relate cannot mutate deterministically", ref.Type, ref.ID),
			Hint:   "edit the file directly, or migrate the draft to the JSON format ('eka new' scaffolds JSON drafts)",
		}
	}
	artifact, err := conformance.ScanFile(df.Path)
	if err != nil {
		return nil, fmt.Errorf("relate: %w", err)
	}
	if artifact == nil {
		return nil, &RelateRefusal{
			Reason: fmt.Sprintf("draft file %s is not a knowledge artifact (missing type/id frontmatter)", df.Path),
			Hint:   "scaffold the draft with 'eka new'",
		}
	}
	// Namespace agreement (mirrors publish): a resolved namespace must
	// equal the draft frontmatter's namespace — the frontmatter is the
	// identity source of truth.
	if artifact.Namespace != ns {
		return nil, &RelateRefusal{
			Reason: fmt.Sprintf("target namespace %s does not match draft namespace %s", ns, artifact.Namespace),
			Hint:   "qualify the target with the draft's own namespace",
		}
	}

	// Structural checks (Rule 5 mirrors) against the draft identity.
	if err := checkRelateEdges(artifact.Namespace, artifact.Type, artifact.ID, artifact.InstanceVersion, adds); err != nil {
		return nil, err
	}

	// The draft's existing edges: artifact.Relations maps the kebab
	// field names to their raw targets.
	existing := make([]exchange.Relationship, 0)
	for _, field := range conformance.RelationshipFieldNames() {
		for _, raw := range artifact.Relations[field] {
			existing = append(existing, exchange.Relationship{Type: field, Target: raw})
		}
	}
	merged, added := mergeRelationships(existing, adds)
	if len(added) == 0 {
		return &RelateResult{Target: ref.Namespace + "/" + ref.Type + ":" + ref.ID, State: "unchanged"}, nil
	}

	// Rewrite the draft file's relationships block deterministically.
	if err := rewriteDraftRelationships(df.Path, merged); err != nil {
		return nil, err
	}

	// Post-mutation re-validation (non-destructive, mirroring `eka
	// edit`): the draft stays and the findings are reported.
	dv, err := Authoring.ValidateDraft(rt, ref.Type+":"+ref.ID, df.Project)
	if err != nil {
		return nil, fmt.Errorf("relate: %w", err)
	}
	return &RelateResult{
		Target:          ref.Namespace + "/" + ref.Type + ":" + ref.ID,
		State:           "draft",
		Added:           added,
		DraftValidation: dv,
	}, nil
}

// checkRelateEdges runs the structural Rule 5 mirrors over the
// requested edges against the artifact's identity: unknown relationship
// type, malformed reference, and self-reference are deterministic
// refusals (a *RelateRefusal) — nothing is written.
func checkRelateEdges(ns, typ, id string, version int, adds []exchange.Relationship) error {
	known := conformance.RelationshipFieldNames()
	for _, rel := range adds {
		if !containsString(known, rel.Type) {
			return &RelateRefusal{
				Reason: fmt.Sprintf("unknown relationship type %q (expected one of: %s)", rel.Type, strings.Join(known, ", ")),
				Hint:   "use --depends-on/--derives-from/--validates/--supersedes/--amends",
			}
		}
		if strings.TrimSpace(rel.Target) == "" {
			return &RelateRefusal{Reason: "relationship targets must be non-empty", Hint: "pass the target after the relationship flag"}
		}
		ref, err := conformance.ParseReference(rel.Target, ns, typ)
		if err != nil {
			return &RelateRefusal{
				Reason: fmt.Sprintf("malformed reference %q in `%s`: %v", rel.Target, rel.Type, err),
				Hint:   "references use the <ns>/<type>:<id> or <type>:<id> form",
			}
		}
		if ref.Namespace == ns && ref.Type == typ && ref.ID == id &&
			(!ref.HasVersion || ref.Version == version) {
			return &RelateRefusal{
				Reason: fmt.Sprintf("self-reference %q in `%s`", rel.Target, rel.Type),
				Hint:   "an artifact cannot reference itself",
			}
		}
	}
	return nil
}

// containsString reports whether v appears in list.
func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// mergeRelationships computes the set union of two relationship lists:
// edges are a set keyed by (type, target), sorted into the canonical
// (type, target) order. The second return value lists the edges of
// adds NOT already present in existing, in the same canonical order —
// the "actually added" set of a relate run.
func mergeRelationships(existing, adds []exchange.Relationship) (merged, added []exchange.Relationship) {
	type relKey struct{ t, target string }
	seen := make(map[relKey]bool, len(existing)+len(adds))
	keys := make([]relKey, 0, len(existing)+len(adds))
	for _, rel := range existing {
		k := relKey{t: rel.Type, target: rel.Target}
		if seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	for _, rel := range adds {
		k := relKey{t: rel.Type, target: strings.TrimSpace(rel.Target)}
		if k.target == "" || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
		added = append(added, exchange.Relationship{Type: k.t, Target: k.target})
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].t != keys[j].t {
			return keys[i].t < keys[j].t
		}
		return keys[i].target < keys[j].target
	})
	merged = make([]exchange.Relationship, 0, len(keys))
	for _, k := range keys {
		merged = append(merged, exchange.Relationship{Type: k.t, Target: k.target})
	}
	return merged, added
}

// rewriteDraftRelationships deterministically rewrites a JSON draft's
// relationships block: the file is parsed into a generic object, the
// `relationships` key is replaced with the merged edge set (camelCase
// field names, per-field sorted targets — the §3.2 spelling), and the
// file is written back as 2-space-indented JSON with a trailing
// newline (the draftJSON byte shape). Semantic content is preserved; a
// draft is mutable and not content-addressed, so the rewrite is safe
// even though generic JSON marshaling reorders keys alphabetically.
func rewriteDraftRelationships(path string, merged []exchange.Relationship) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("relate: cannot read draft %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("relate: draft %s is not valid JSON: %w", path, err)
	}
	rels := make(map[string][]string)
	for _, field := range conformance.RelationshipFieldNames() {
		var targets []string
		for _, rel := range merged {
			if rel.Type != field {
				continue
			}
			targets = append(targets, rel.Target)
		}
		if len(targets) > 0 {
			rels[conformance.StateKeyCamel(field)] = targets
		}
	}
	if len(rels) > 0 {
		doc["relationships"] = rels
	} else {
		delete(doc, "relationships")
	}
	out, err := json.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("relate: cannot serialize draft %s: %w", path, err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, out, "", "  "); err != nil {
		return fmt.Errorf("relate: cannot serialize draft %s: %w", path, err)
	}
	indented.WriteByte('\n')
	return os.WriteFile(path, indented.Bytes(), 0o644)
}
