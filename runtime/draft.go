package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
	"gopkg.in/yaml.v3"
)

// This file implements the draft-publish authoring workflow
// (reference/spec-authoring-publish.md): the Draft lifecycle owned by
// the Authoring API — NewDraft (scaffold), Publish (validate +
// persist + remove), PublishInline (structured input, no file),
// Drafts (backlog listing), DiscardDraft.
//
// The three-state staging model:
//
//	Authoring (repo docs/)  Draft (workspace drafts/)  Published (workspace DB)
//	   markdown                  <project>/<type>-<id>.md        immutable CKO
//	   eka sync                  eka new / edit / publish        eka get / sync push
//
// Draft layout: <workspace>/drafts/<project>/<type>-<id>.json (the v2.0
// JSON-native authoring format; legacy .md drafts remain readable). The
// project directory scopes the draft; a draft always belongs to exactly
// one project. Draft files are mutable and never validated by the
// repository conformance gate — they are validated at publish time with
// the CKO-level validator (conformance.ValidateCKO: location rules
// skipped).
//
// Publish is all-or-nothing (spec §5.1): the CKO insert and the draft
// deletion happen in sequence, and a failed validation or insert leaves
// the draft file untouched. The draft file is the single-use ticket
// (spec §5.2): a second publish of the same draft fails at the read.
//
// Provenance of published objects (documented decision, deviation from
// the spec's open question): published drafts are workspace-native
// knowledge — they have no source repository. The reference provenance
// pair is (project_id, source_repo) with source_repo NOT NULL, so
// workspace-native objects use the documented sentinel
// draftSourceRepo = "runtime". Repository snapshots (sync push) filter
// by (project_id, source_repo), so "runtime"-attributed objects never
// appear in any repository snapshot; they are workspace-native and
// travel only through the workspace database (and future explicit
// export). This also means publish NEVER auto-syncs: there is no
// repository to push, so the spec's PublishOptions.Sync flag is dropped
// in this milestone (spec §4.4/§8 adjusted: PublishOptions carries only
// InstanceVersion); `eka sync` remains the explicit transport step for
// repository-attributed knowledge.

// draftSourceRepo is the documented provenance sentinel of
// workspace-native published objects: the source_repo of every object
// persisted by Publish/PublishInline. Push filters by
// (project_id, source_repo), so these objects never appear in a
// repository snapshot. The name is reserved: workspace.RegisterRepo
// refuses a repository literally named "runtime" (workspace.
// ReservedRepoName), so the sentinel can never collide with a real
// repository's provenance pair.
const draftSourceRepo = workspace.ReservedRepoName

// Draft is one mutable draft of the workspace drafts tree.
type Draft struct {
	// Project is the draft's project scope (its parent directory).
	Project string
	// Namespace is the frontmatter namespace of the draft ("" when the
	// file cannot be classified as an artifact).
	Namespace string
	// Type and ID identify the draft: its file name is <type>-<id>.json
	// (legacy drafts may carry .md).
	Type string
	ID   string
	// Path is the absolute draft file path.
	Path string
	// Updated is the file modification time (RFC3339) — display
	// metadata for `eka draft list`; the draft file itself carries no
	// timestamps (deterministic template).
	Updated string
}

// NewDraftRequest describes one draft to scaffold.
type NewDraftRequest struct {
	// Project is the required project scope (the drafts parent
	// directory).
	Project string
	// Namespace is the required frontmatter namespace of the draft.
	Namespace string
	// Type is a known EKA artifact type token; ID is the draft id.
	Type string
	ID   string
	// Dimension and Phase are optional classification/context
	// frontmatter fields (phase is scp-/plan- only).
	Dimension, Phase string
	// Domain is the optional declared Engineering Domain of the draft
	// (one of the five canonical domains, canonical spelling — e.g.
	// "Architecture"). "" leaves the domain derived from the type token
	// at publish time. cmt- notes use it to declare the Engineering
	// Domain their discussion is contexted in (ADR-019 D8 revised).
	Domain string
	// By is the change-log authority of the draft's initial entries
	// (an empty identity = the default "Engineering" user).
	By conformance.AuthorIdentity
	// Relationships are the draft's authoring references, stored
	// verbatim in the frontmatter (e.g. {Type: "depends-on",
	// Target: "ctr:wave-7"}); they are validated at publish time.
	Relationships []exchange.Relationship
	// ContentFile optionally prepopulates the draft content with a JSON
	// object read from the file (agents): the object is merged into the
	// draft's content (spec-standard-v2 §12 step 5 — a JSON object
	// only; raw text is rejected). "" scaffolds the type's required
	// content keys as empty placeholders.
	ContentFile string
}

// PublishOptions configures one publish run. Documented deviation from
// spec §8: the spec's Sync flag is dropped — workspace-native objects
// have no repository to push, so publish never auto-syncs (see the
// package comment).
type PublishOptions struct {
	// Project is the project scope of the draft: "" resolves the
	// project from the repository registered at the current working
	// directory (the spec §3.2 rule); an explicit value addresses a
	// draft under drafts/<project>/ from anywhere (the CLI --project
	// flag).
	Project string
	// InstanceVersion overrides the auto-assignment: it must exceed
	// the line's highest existing version (forward-only, P7). 0 =
	// auto-assign (max + 1, or 1 for a new line). Precedence: this
	// override, then the draft frontmatter's instance-version when
	// present, then auto-assignment.
	InstanceVersion int
}

// PublishResult reports one successful publish.
type PublishResult struct {
	// Form is the canonical identity form of the published object
	// ("<namespace>/<type>:<id>:<instance-version>").
	Form string
	// InstanceVersion is the assigned instance version.
	InstanceVersion int
	// ObjectHash is the content-derived object hash (SHA-256(unit.json
	// || content)) of the immutable payload.
	ObjectHash string
	// Note is set when the run converged with a concurrent publish of
	// the same draft: the object was persisted by this run, but the
	// draft file was already gone (spec §5.2 single-writer race) —
	// "already published by a concurrent run". "" in the normal case.
	Note string
}

// PublishError reports that the draft failed CKO-level validation; the
// draft file was kept. The Report is carried so the caller renders the
// findings.
type PublishError struct {
	// Target is the draft target as addressed.
	Target string
	// Report is the CKO-level validation report.
	Report *conformance.Report
}

// Error renders the deterministic refusal message.
func (e *PublishError) Error() string {
	return fmt.Sprintf("publish refused: draft %s failed CKO-level validation with %d blocking error(s); the draft was kept",
		e.Target, e.Report.ErrorCount())
}

// DraftNotFoundError reports that the draft file is missing: already
// published (the draft file is the single-use ticket), discarded, or
// simply absent. Project names the project the lookup tried ("" when
// no project could be resolved and no draft matched anywhere).
type DraftNotFoundError struct {
	// Target is the draft target as addressed.
	Target string
	// Project is the resolved project the lookup tried ("" = none).
	Project string
}

// Error renders the deterministic message of the duplicate-publish
// guard (spec §4.4 step 1). When a project was resolved, the message
// names it and points at --project — the draft may live in another
// project (draft identities are project-scoped, spec §2.2).
func (e *DraftNotFoundError) Error() string {
	if e.Project == "" {
		return fmt.Sprintf("draft %s not found in any project (already published or discarded)", e.Target)
	}
	return fmt.Sprintf("draft %s not found in project %s (already published or discarded); pass --project <name> if the draft lives in another project",
		e.Target, e.Project)
}

// --- Draft storage helpers (unexported; drafts live under the
// workspace directory). ---

// draftsRoot returns the workspace drafts root directory.
func draftsRoot(ws *workspace.Workspace) string {
	return filepath.Join(ws.Path(), "drafts")
}

// draftPath returns the draft file path of one draft identity:
// <workspace>/drafts/<project>/<type>-<id>.json (the v2.0 JSON-native
// authoring format).
func draftPath(ws *workspace.Workspace, project, typeToken, id string) string {
	return filepath.Join(draftsRoot(ws), project, typeToken+"-"+id+".json")
}

// parseDraftTarget parses a draft target ("<ns>/<type>:<id>" or
// "<type>:<id>") and refuses canonical published forms (carrying an
// instance-version suffix).
func parseDraftTarget(target string) (conformance.Reference, error) {
	ref, err := conformance.ParseReference(target, "", "")
	if err != nil {
		return ref, fmt.Errorf("invalid draft target %q: %w", target, err)
	}
	if ref.HasVersion {
		return ref, fmt.Errorf("%s is a published knowledge object; drafts only", target)
	}
	return ref, nil
}

// cwdProjectOf resolves the project owning the repository registered
// at the current working directory ("" when the cwd is not inside a
// registered repository — the caller then falls back to scanning every
// project).
func cwdProjectOf(rt *Runtime) string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	repo, found, err := rt.Workspace.FindRepo(cwd)
	if err != nil || !found {
		return ""
	}
	return repo.ProjectID
}

// DraftRef is a resolved draft file: its path, the project it lives
// under, and an optional note (set when the resolution had to fall
// back to a project different from the one the current directory
// resolves to).
type DraftRef struct {
	Path    string
	Project string
	Note    string
}

// resolveDraftFile locates one draft file by identity, project-scoped:
//
//  1. the explicit project hint (the CLI --project flag) when given,
//     else the project of the repository registered at the current
//     working directory;
//  2. if the draft is not there, every project's drafts are scanned
//     (deterministic order): exactly one match resolves with a note
//     naming the project; several matches are refused as ambiguous
//     (the draft identity is project-scoped, so the caller must
//     disambiguate with --project); none matches is *DraftNotFoundError.
//
// The fallback keeps `eka draft list` (which shows every project) and
// the draft operations consistent: a draft visible in the list is
// always addressable from any directory.
func resolveDraftFile(ws *workspace.Workspace, rt *Runtime, projectHint, typeToken, id string) (DraftRef, error) {
	project := projectHint
	if project == "" {
		project = cwdProjectOf(rt)
	}
	if project != "" {
		path := draftPath(ws, project, typeToken, id)
		if _, err := os.Stat(path); err == nil {
			return DraftRef{Path: path, Project: project}, nil
		}
	}

	// Fallback: scan every project's drafts for this identity.
	needleJSON := typeToken + "-" + id + ".json"
	needleMD := typeToken + "-" + id + ".md"
	var found []DraftRef
	root := draftsRoot(ws)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return DraftRef{}, &DraftNotFoundError{Target: typeToken + ":" + id, Project: project}
		}
		return DraftRef{}, fmt.Errorf("cannot scan drafts: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := e.Name()
		path := filepath.Join(root, p, needleJSON)
		if _, err := os.Stat(path); err != nil {
			path = filepath.Join(root, p, needleMD) // legacy .md drafts stay addressable
			if _, err := os.Stat(path); err != nil {
				continue
			}
		}
		note := "draft resolved from project " + p
		if projectHint != "" {
			note = fmt.Sprintf("draft %s not found in project %s; resolved from project %s", typeToken+":"+id, projectHint, p)
		}
		found = append(found, DraftRef{Path: path, Project: p, Note: note})
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return DraftRef{}, &DraftNotFoundError{Target: typeToken + ":" + id, Project: project}
	default:
		projects := make([]string, 0, len(found))
		for _, f := range found {
			projects = append(projects, f.Project)
		}
		sort.Strings(projects)
		return DraftRef{}, fmt.Errorf("ambiguous draft %s: exists in projects %s; pass --project <name>",
			typeToken+":"+id, strings.Join(projects, ", "))
	}
}

// --- NewDraft ---

// NewDraft scaffolds one draft: the deterministic JSON authoring
// template (namespace, type, id, revision 1, the type's owned state
// fields with their initial values, optional dimension/phase/relationship
// fields, the change-log with one "-" -> initial entry per owned domain
// plus the phase context when given,
// plus the type's required content keys as empty placeholders — or the
// ContentFile JSON object merged into the content). The template is
// byte-deterministic for identical inputs within a day (the change-log
// entries carry the creation date, required by the authoring format);
// instance-version is deliberately absent — it is assigned at publish
// time.
func (AuthoringService) NewDraft(rt *Runtime, req NewDraftRequest) (*Draft, error) {
	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	if _, ok := conformance.DomainForToken(req.Type); !ok {
		return nil, fmt.Errorf("authoring: unknown artifact type %q; expected one of the 27 EKA type tokens", req.Type)
	}
	if req.ID == "" {
		return nil, fmt.Errorf("authoring: draft id must be a non-empty string")
	}
	if req.Project == "" {
		return nil, fmt.Errorf("authoring: a draft requires a project")
	}
	if req.Namespace == "" {
		return nil, fmt.Errorf("authoring: a draft requires a namespace")
	}
	if req.Phase != "" && !conformance.IsVersionedType(req.Type) {
		return nil, fmt.Errorf("authoring: `phase` is a context attribute allowed only on scp-/plan- artifacts, not on type %q", req.Type)
	}
	// Rule 8: a ticket is a projection of a container — a ticket draft
	// without a container reference can never publish. Refuse at
	// scaffold time (the same guard pattern as the phase check) so
	// `eka new` always produces a publishable draft.
	if req.Type == "tkt" && !hasContainerReference(req.Relationships) {
		return nil, fmt.Errorf("authoring: ticket drafts require --derives-from with a container (ctr-) reference")
	}
	// Container lifecycle: a container is born from a plan — a ctr-
	// draft without a depends-on plan reference can never publish (its
	// activation locks the plan; protocol §4). Refuse at scaffold time,
	// mirroring the ticket guard.
	if req.Type == "ctr" && !hasPlanReference(req.Relationships) {
		return nil, fmt.Errorf("authoring: container drafts require --depends-on with a plan- reference")
	}
	path := draftPath(ws, req.Project, req.Type, req.ID)

	body, err := draftJSON(req)
	if err != nil {
		return nil, fmt.Errorf("authoring: cannot render draft %s: %w", req.Type+":"+req.ID, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("authoring: cannot create the drafts directory for project %s: %w", req.Project, err)
	}
	// The collision guard and the write are one atomic step:
	// O_CREATE|O_EXCL fails when the draft file already exists, so a
	// concurrent `eka new` of the same identity can never overwrite
	// (spec §2.2 collision rule).
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("authoring: draft %s:%s already exists in project %s", req.Type, req.ID, req.Project)
		}
		return nil, fmt.Errorf("authoring: cannot write draft %s: %w", path, err)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(path) // best-effort cleanup of the partial file we created
		return nil, fmt.Errorf("authoring: cannot write draft %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("authoring: cannot write draft %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("authoring: cannot read draft %s: %w", path, err)
	}
	return &Draft{
		Project:   req.Project,
		Namespace: req.Namespace,
		Type:      req.Type,
		ID:        req.ID,
		Path:      path,
		Updated:   info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

// draftDefaultState returns the initial state value a fresh draft
// carries for one owned state domain, selected for the artifact type
// (the content-state value set is type-dependent): adr- starts at
// proposed (its variant has no "draft"), every other content-state
// type and planning-state start at draft, execution at planned,
// note-state at open (ADR-019 D4), container at planned (containers
// are born planned and activate one at a time — Option B), existence
// at active.
func draftDefaultState(typeToken, domain string) string {
	switch domain {
	case conformance.DomainContentState:
		if typeToken == "adr" {
			return "proposed"
		}
		return "draft"
	case conformance.DomainPlanningState:
		return "draft"
	case conformance.DomainExecutionState:
		return "planned"
	case conformance.DomainNoteState:
		return "open"
	case conformance.DomainContainerState:
		return "planned"
	default: // existence-state
		return "active"
	}
}

// draftDoc is the deterministic §3.2 authoring document of one
// scaffolded draft: identity + revision 1, the owned-state defaults,
// optional dimension/phase/relationship fields, the change-log with one
// "-" -> initial entry per owned domain (plus the phase context when
// given), and the required content keys
// (empty placeholders). Field order is the schema's canonical order
// (spec-standard-v2 §3.2).
type draftDoc struct {
	Namespace string            `json:"namespace"`
	Type      string            `json:"type"`
	ID        string            `json:"id"`
	Revision  int               `json:"revision"`
	State     map[string]string `json:"state,omitempty"`
	// Author is the draft's author identity (nil = absent; a user
	// serializes as the plain name, an agent/worker as {kind, name}).
	Author    *conformance.AuthorIdentity `json:"author,omitempty"`
	Dimension string                      `json:"dimension,omitempty"`
	Phase     string                      `json:"phase,omitempty"`
	// Domain is the declared Engineering Domain (canonical spelling,
	// e.g. "Architecture"); absent = derived from the type token.
	Domain        string              `json:"domain,omitempty"`
	Relationships map[string][]string `json:"relationships,omitempty"`
	ChangeLog     []draftChangeLog    `json:"changeLog,omitempty"`
	Content       map[string]any      `json:"content"`
}

// draftChangeLog is one change-log entry in the §3.2 spelling: domain
// uses the camelCase state domain names (contentState), values unchanged.
type draftChangeLog struct {
	Date   string                     `json:"date"`
	Domain string                     `json:"domain"`
	From   string                     `json:"from"`
	To     string                     `json:"to"`
	By     conformance.AuthorIdentity `json:"by"`
}

// draftJSON renders the full deterministic draft file bytes: the JSON
// authoring document, 2-space indent, trailing newline. The content is
// the type's required section keys as empty placeholders (with the
// projection header as the tkt- template's commands value — rule 8
// requires it, so a scaffolded draft must be publishable without
// edits), merged with the ContentFile JSON object when given (the
// object's keys overwrite/add; raw text files are rejected).
func draftJSON(req NewDraftRequest) ([]byte, error) {
	doc := draftDoc{
		Namespace: req.Namespace,
		Type:      req.Type,
		ID:        req.ID,
		Revision:  1,
		Content:   map[string]any{},
	}
	owned := conformance.OwnedDomains(req.Type) // non-nil: the type was validated
	if len(owned) > 0 {
		state := map[string]string{}
		for _, domain := range owned {
			state[conformance.StateKeyCamel(domain)] = draftDefaultState(req.Type, domain)
		}
		doc.State = state
	}
	if req.Dimension != "" {
		doc.Dimension = req.Dimension
	}
	if req.Phase != "" {
		doc.Phase = req.Phase
	}
	if req.Domain != "" {
		doc.Domain = req.Domain
	}
	// Relationships: canonical field order, deduplicated, targets
	// sorted within each field (byte-deterministic).
	for _, field := range conformance.RelationshipFieldNames() {
		var targets []string
		seen := map[string]bool{}
		for _, rel := range req.Relationships {
			if rel.Type != field || rel.Target == "" || seen[rel.Target] {
				continue
			}
			seen[rel.Target] = true
			targets = append(targets, rel.Target)
		}
		if len(targets) == 0 {
			continue
		}
		sort.Strings(targets)
		if doc.Relationships == nil {
			doc.Relationships = map[string][]string{}
		}
		doc.Relationships[conformance.StateKeyCamel(field)] = targets
	}
	// The change-log covers every owned domain plus, when the draft
	// carries a phase, the phase context (rule 7 requires a change-log
	// entry for every field with a change-log domain — a scaffolded
	// draft must be publishable without edits, so the phase entry is
	// scaffolded alongside the field: "-" -> the given value, by the
	// author, same date as the state entries).
	if len(owned) > 0 || req.Phase != "" {
		today := time.Now().Format("2006-01-02")
		by := req.By
		if by.Name == "" {
			by = conformance.User("Engineering")
		}
		for _, domain := range owned {
			doc.ChangeLog = append(doc.ChangeLog, draftChangeLog{
				Date:   today,
				Domain: conformance.StateKeyCamel(domain),
				From:   "-",
				To:     draftDefaultState(req.Type, domain),
				By:     by,
			})
		}
		if req.Phase != "" {
			doc.ChangeLog = append(doc.ChangeLog, draftChangeLog{
				Date:   today,
				Domain: conformance.DomainPhase,
				From:   "-",
				To:     req.Phase,
				By:     by,
			})
		}
	}
	if req.By.Name != "" {
		doc.Author = &req.By
	}
	// Content: required section keys as empty placeholders; the tkt-
	// template's commands value carries the exact projection header
	// line (rule 8).
	for _, section := range conformance.RequiredSectionsFor(req.Type) {
		key := conformance.SectionKey(section)
		if req.Type == "tkt" && key == "commands" {
			doc.Content[key] = conformance.ProjectionHeader + "\n"
			continue
		}
		doc.Content[key] = ""
	}
	// ContentFile: a JSON object merged into the content (spec-standard
	// -v2 §12 step 5 — raw text is rejected for JSON drafts).
	if req.ContentFile != "" {
		content, err := os.ReadFile(req.ContentFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read content file %s: %w", req.ContentFile, err)
		}
		var merge map[string]any
		if err := json.Unmarshal(content, &merge); err != nil {
			return nil, fmt.Errorf("content file %s is not a valid JSON object (JSON drafts accept a JSON object only; raw text is rejected): %v", req.ContentFile, err)
		}
		for k, v := range merge {
			doc.Content[k] = v
		}
	}
	out, err := json.Marshal(&doc)
	if err != nil {
		return nil, err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, out, "", "  "); err != nil {
		return nil, err
	}
	indented.WriteByte('\n')
	return indented.Bytes(), nil
}

// hasContainerReference reports whether any relationship target
// references a container (ctr-) artifact in the authoring convention.
func hasContainerReference(rels []exchange.Relationship) bool {
	for _, r := range rels {
		if strings.HasPrefix(r.Target, "ctr:") || strings.Contains(r.Target, "/ctr:") {
			return true
		}
	}
	return false
}

// hasPlanReference reports whether any depends-on relationship target
// references a plan (plan-) artifact: the parsed target type token
// decides (conformance.ParseReference with the referrer type "ctr" —
// a bare id therefore resolves to a ctr-, never a plan-). A malformed
// target is treated as not-a-plan.
func hasPlanReference(rels []exchange.Relationship) bool {
	for _, r := range rels {
		if r.Type != "depends-on" {
			continue
		}
		ref, err := conformance.ParseReference(r.Target, "", "ctr")
		if err != nil {
			continue // Malformed: not a plan reference.
		}
		if ref.Type == "plan" {
			return true
		}
	}
	return false
}

// --- Publish ---

// Publish persists one draft as an immutable Canonical Knowledge
// Object. Pipeline (deterministic, spec §4.4):
//
//  1. resolve the draft file (project from opts.Project or the cwd
//     repository, target "<ns>/<type>:<id>" or "<type>:<id>"); missing
//     -> *DraftNotFoundError (the duplicate-publish guard);
//  2. classify the file (conformance.ScanFile); a file that is not a
//     knowledge artifact, or fails the structural classification, is
//     refused with the parse findings; a target namespace, when the
//     target carries one, must equal the draft frontmatter's namespace
//     (mismatch -> deterministic error);
//  3. build the unit exactly like the compiler would (identity from the
//     frontmatter; instance-version precedence: opts.InstanceVersion,
//     then the frontmatter's instance-version when present, then
//     auto-assign max(line)+1 — every explicit version must exceed the
//     line's highest, forward-only P7; relationships stored verbatim);
//  4. run CKO-level validation (conformance.ValidateCKO) with
//     relationship resolution against the canonical store; blocking
//     violations -> *PublishError, the draft is kept;
//  5. insert the immutable payload (store.PutUnit, provenance
//     source_repo = "runtime");
//  6. remove the draft file (same operation; a failed insert leaves
//     the draft untouched; a draft file already gone — a concurrent
//     publish converged — is reported via PublishResult.Note, never an
//     error).
//
// The published object is workspace-native: it never appears in a
// repository snapshot (see the package comment).
func (AuthoringService) Publish(rt *Runtime, target string, opts PublishOptions) (*PublishResult, error) {
	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	st, err := rt.requireStore()
	if err != nil {
		return nil, err
	}
	ref, err := parseDraftTarget(target)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	df, err := resolveDraftFile(ws, rt, opts.Project, ref.Type, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	path := df.Path
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, &DraftNotFoundError{Target: target, Project: df.Project}
		}
		return nil, fmt.Errorf("publish: cannot access draft %s: %w", target, err)
	}
	artifact, err := conformance.ScanFile(path)
	if err != nil {
		// *conformance.ScanError: the draft failed the structural
		// classification; the parse findings travel with the error.
		return nil, fmt.Errorf("publish: %w", err)
	}
	if artifact == nil {
		return nil, fmt.Errorf("publish: draft %s is not a knowledge artifact (missing type/id frontmatter)", target)
	}
	// The target namespace is addressing, not identity: when the target
	// carries one it must agree with the draft's frontmatter namespace
	// (the identity source of truth), so a wrong-namespace target is a
	// deterministic error instead of a silent cross-namespace publish.
	if ref.Namespace != "" && ref.Namespace != artifact.Namespace {
		return nil, fmt.Errorf("publish: target namespace %s does not match draft namespace %s",
			ref.Namespace, artifact.Namespace)
	}
	// The draft's identity is its frontmatter, not its file name: the
	// file name is addressing, the frontmatter is the source of truth,
	// so a target that resolves to a file carrying a DIFFERENT identity
	// would silently publish the frontmatter identity under the
	// target's address. Mismatch is a deterministic error like the
	// namespace check above (rename the file, or publish the draft's
	// own identity).
	if artifact.Type != ref.Type || artifact.ID != ref.ID {
		return nil, fmt.Errorf("publish: draft file %s carries identity %s:%s; expected %s:%s — rename the file or publish the draft's own identity",
			path, artifact.Type, artifact.ID, ref.Type, ref.ID)
	}

	// Instance version (forward-only, P7): the explicit override
	// (opts) wins, then the draft frontmatter's instance-version when
	// present, then auto-assign max(line)+1. Every explicit version
	// must exceed the line's highest.
	version := opts.InstanceVersion
	if version == 0 && artifact.InstanceVersion > 0 {
		version = artifact.InstanceVersion
	}
	max, err := st.MaxInstanceVersion(artifact.Namespace, artifact.Type, artifact.ID)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	if version == 0 {
		version = max + 1
	} else if version <= max {
		return nil, fmt.Errorf("publish: instance version %d must exceed the line's highest (%d)", version, max)
	}

	u := unitFromDraft(artifact, version)
	resolver := newStoreResolver(st)
	report, err := conformance.ValidateCKO(u, conformance.ValidateCKOOptions{
		Resolve: resolver.Resolve,
	})
	if err != nil {
		return nil, fmt.Errorf("publish: validation failed: %w", err)
	}
	// Store failures during resolution surface as findings (naming the
	// underlying error) instead of a silent "unresolved reference".
	report.Results = append(report.Results,
		resolver.Findings(artifact.RelPath, artifact.States[conformance.DomainContentState])...)
	if !report.Pass() {
		return nil, &PublishError{Target: target, Report: report}
	}

	// P6: publish never touches referenced objects' state — the plan a
	// container depends on is locked only when the container ACTIVATES
	// (planned -> active, protocol §4), never at publish. A container
	// publishes like every other type: born planned, no lock.
	res, err := persistPublishedUnit(st, df.Project, u)
	if err != nil {
		return nil, err
	}
	// The draft file is the single-use ticket: removed in the same
	// operation, after the insert committed (spec §5.1/§5.2). A failed
	// insert leaves the draft untouched. A draft file that is already
	// gone means a concurrent publish of the same draft converged: the
	// object is persisted, the run is successful and reports the
	// convergence via Note (spec §5.2 single-writer race).
	alreadyGone, err := removeDraftAfterPublish(path)
	if err != nil {
		return nil, fmt.Errorf("publish: the knowledge object %s was persisted, but the draft file could not be removed: %w", res.Form, err)
	}
	if df.Note != "" {
		res.Note = df.Note
	}
	if alreadyGone {
		if res.Note != "" {
			res.Note += "; already published by a concurrent run"
		} else {
			res.Note = "already published by a concurrent run"
		}
	}
	return res, nil
}

// removeDraftAfterPublish removes the draft file after a successful
// insert. alreadyGone reports that the file was already missing — the
// loser of a concurrent publish of the same draft (both runs insert the
// same immutable payload; the first removal wins).
func removeDraftAfterPublish(path string) (alreadyGone bool, err error) {
	err = os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}

// unitFromDraft builds the exchange.Unit of one draft — identity from
// the frontmatter, the assigned instance-version, relationships stored
// VERBATIM (unlike the repository compiler, which drops unresolvable
// references: a draft's authoring references are part of the published
// object and the CKO-level validator applies draft tolerance).
//
// The content is compiled exactly like the repository compiler's
// (spec-standard-v2 §6): JSON-native drafts carry their content object
// directly; legacy Markdown drafts get their body compiled into it. The
// payload is the canonical compact encoding
// (eka/structured-json/1, §3.3).
func unitFromDraft(a *conformance.Artifact, version int) *exchange.Unit {
	id := exchange.Identity{
		Namespace:       a.Namespace,
		Type:            a.Type,
		ID:              a.ID,
		InstanceVersion: version,
	}
	classification := exchange.Classification{}
	if a.HasDimension {
		classification.Dimension = a.Dimension
	}
	if len(a.DimensionsSecondary) > 0 {
		classification.DimensionsSecondary = a.DimensionsSecondary
	}
	if a.Domain != "" {
		// The declared domain wins (Rule 11 validates it): cmt- notes
		// declare the Engineering Domain their discussion is contexted
		// in — any of the five canonical domains (ADR-019 D8 revised),
		// so the type-derived home domain must not override it.
		classification.Domain = a.Domain
	} else if d, ok := conformance.DomainForToken(a.Type); ok {
		classification.Domain = string(d)
	}
	payload, err := conformance.ContentJSON(conformance.CompileContentObject(a), conformance.RequiredSectionsFor(a.Type))
	if err != nil || payload == nil {
		// Unreachable for parsed drafts (the content object is always
		// encodable); the empty payload keeps the unit coherent.
		payload = []byte("{}")
	}
	u := &exchange.Unit{
		Identity:              id,
		CanonicalIdentityForm: id.CanonicalForm(),
		Revision:              a.Revision,
		Author:                conformance.AuthorIdentity{Kind: a.AuthorKind, Name: a.Author},
		Created:               a.Created,
		Updated:               a.Updated,
		StateVector: exchange.StateVector{
			ContentState:   a.States[conformance.DomainContentState],
			ExecutionState: a.States[conformance.DomainExecutionState],
			PlanningState:  a.States[conformance.DomainPlanningState],
			ContainerState: a.States[conformance.DomainContainerState],
			ExistenceState: a.States[conformance.DomainExistenceState],
			NoteState:      a.States[conformance.DomainNoteState],
		},
		Classification: classification,
		Phase:          a.Phase,
		Content: exchange.ContentRef{
			Representation: exchange.StructuredJSON,
			File:           "content",
		},
		ContentPayload: payload,
		ChangeLog:      []exchange.ChangeLogEntry{},
		Relationships:  []exchange.Relationship{},
	}
	for _, e := range a.ChangeLog {
		u.ChangeLog = append(u.ChangeLog, exchange.ChangeLogEntry{
			Date: e.Date, Domain: e.Domain, From: e.From, To: e.To, By: conformance.AuthorIdentity{Kind: e.ByKind, Name: e.By},
		})
	}
	type relKey struct{ t, target string }
	var ordered []relKey
	for field, raws := range a.Relations {
		for _, raw := range raws {
			ordered = append(ordered, relKey{t: field, target: raw})
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].t != ordered[j].t {
			return ordered[i].t < ordered[j].t
		}
		return ordered[i].target < ordered[j].target
	})
	for _, r := range ordered {
		u.Relationships = append(u.Relationships, exchange.Relationship{Type: r.t, Target: r.target})
	}
	return u
}

// storeResolver is the CKO-level relationship resolution callback over
// the canonical store: a line-level reference resolves when the line
// has instances, a versioned reference only when the exact instance
// exists. Store failures resolve as "unresolved" (the conservative
// answer for the validator: it blocks non-draft publishes instead of
// accepting a reference whose existence cannot be checked) — but they
// are never SILENT: every failed lookup is recorded per referenced
// line (deduplicated) and surfaced by Findings as a report result that
// names the store error, instead of an unexplained "unresolved
// reference".
type storeResolver struct {
	st   *store.Store
	errs map[string]error // referenced line key -> the first store error
}

// newStoreResolver builds the resolver over one canonical store.
func newStoreResolver(st *store.Store) *storeResolver {
	return &storeResolver{st: st}
}

// Resolve implements the conformance resolution callback.
func (r *storeResolver) Resolve(ref conformance.Reference) bool {
	units, err := r.st.UnitsByLine(ref.Namespace, ref.Type, ref.ID)
	if err != nil {
		key := ref.Namespace + "/" + ref.Type + ":" + ref.ID
		if r.errs == nil {
			r.errs = make(map[string]error)
		}
		if _, seen := r.errs[key]; !seen {
			r.errs[key] = err
		}
		return false
	}
	if !ref.HasVersion {
		return len(units) > 0
	}
	for _, u := range units {
		if u.Identity.InstanceVersion == ref.Version {
			return true
		}
	}
	return false
}

// Findings converts the recorded store failures into report results in
// deterministic order (by referenced line). file is the File field of
// the results (the artifact's RelPath — the scanned draft path, or the
// unit's canonical identity form on the inline path); contentState is
// the unit's content-state, mirroring Rule 5's draft tolerance: a
// failed existence check is a warning while content-state is draft, an
// error otherwise (the resolution already failed; this finding names
// the cause instead of leaving a silent unresolved).
func (r *storeResolver) Findings(file, contentState string) []conformance.Result {
	if len(r.errs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.errs))
	for k := range r.errs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]conformance.Result, 0, len(keys))
	for _, k := range keys {
		sev := conformance.SeverityError
		if contentState == "draft" { // Rule 5's draft tolerance
			sev = conformance.SeverityWarning
		}
		out = append(out, conformance.Result{
			File:     file,
			Rule:     conformance.Rule5,
			Severity: sev,
			Message:  fmt.Sprintf("reference %s could not be checked against the store: %v", k, r.errs[k]),
		})
	}
	return out
}

// persistPublishedUnit inserts the immutable payload of one unit with
// the workspace-native provenance (source_repo = "runtime") and returns
// the publish result.
func persistPublishedUnit(st *store.Store, project string, u *exchange.Unit) (*PublishResult, error) {
	unitJSON, err := exchange.MarshalUnit(u)
	if err != nil {
		return nil, fmt.Errorf("publish: cannot serialize %s: %w", u.CanonicalIdentityForm, err)
	}
	hash, _, err := st.PutUnit(unitJSON, u.ContentPayload, store.Ref{
		Form:            u.CanonicalIdentityForm,
		ProjectID:       project,
		SourceRepo:      draftSourceRepo,
		Namespace:       u.Identity.Namespace,
		Type:            u.Identity.Type,
		ID:              u.Identity.ID,
		InstanceVersion: u.Identity.InstanceVersion,
		Revision:        u.Revision,
		Dimension:       u.Classification.Dimension,
		Domain:          u.Classification.Domain,
		Phase:           u.Phase,
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, fmt.Errorf("publish: cannot persist %s: %w", u.CanonicalIdentityForm, err)
	}
	return &PublishResult{
		Form:            u.CanonicalIdentityForm,
		InstanceVersion: u.Identity.InstanceVersion,
		ObjectHash:      hash,
	}, nil
}

// --- PublishInline ---

// inlineCKO is the structured input of PublishInline: the CKO document
// fields in YAML/JSON form, shaped like the v2.0 authoring schema
// (spec-standard-v2 §3.2 — camelCase; the single-prompt path for agents
// and future authoring interfaces). content accepts the structured
// object ({fields: {...}}) or legacy text ({text: "..."}).
type inlineCKO struct {
	Identity struct {
		Namespace       string `yaml:"namespace"`
		Type            string `yaml:"type"`
		ID              string `yaml:"id"`
		InstanceVersion *int   `yaml:"instanceVersion"` // nil = auto-assign
	} `yaml:"identity"`
	Revision       int                        `yaml:"revision"`
	Author         conformance.AuthorIdentity `yaml:"author"`
	Created        string                     `yaml:"created"`
	Updated        string                     `yaml:"updated"`
	State          map[string]string          `yaml:"state"`
	Phase          string                     `yaml:"phase"`
	Classification struct {
		Dimension           string   `yaml:"dimension"`
		DimensionsSecondary []string `yaml:"dimensionsSecondary"`
		Domain              string   `yaml:"domain"`
	} `yaml:"classification"`
	Relationships []exchange.Relationship   `yaml:"relationships"`
	ChangeLog     []exchange.ChangeLogEntry `yaml:"changeLog"`
	Content       struct {
		Fields map[string]any `yaml:"fields"`
		Text   string         `yaml:"text"`
	} `yaml:"content"`
}

// PublishInline persists one canonical unit supplied as structured
// input (YAML/JSON shaped like the v2.0 authoring schema) — the same
// pipeline as Publish without a draft file: parse, build the unit,
// assign the instance version (input wins over opts, opts wins over
// auto), ValidateCKO, immutable insert with the "runtime" provenance.
// A parse failure is a deterministic error; blocking validation findings
// are a *PublishError carrying the report (nothing is persisted).
func (AuthoringService) PublishInline(rt *Runtime, input []byte, opts PublishOptions) (*PublishResult, error) {
	// The workspace must exist: the inline path persists into the
	// canonical store, and requireWorkspace is the initialization gate.
	if _, err := rt.requireWorkspace(); err != nil {
		return nil, err
	}
	st, err := rt.requireStore()
	if err != nil {
		return nil, err
	}
	var doc inlineCKO
	if err := yaml.Unmarshal(input, &doc); err != nil {
		return nil, fmt.Errorf("publish: the inline input is not valid YAML/JSON: %v", err)
	}
	if doc.Identity.Namespace == "" || doc.Identity.Type == "" || doc.Identity.ID == "" {
		return nil, fmt.Errorf("publish: the inline input's identity requires namespace, type and id")
	}
	if doc.Identity.InstanceVersion != nil && *doc.Identity.InstanceVersion < 1 {
		return nil, fmt.Errorf("publish: instance version %d is invalid; versions are >= 1", *doc.Identity.InstanceVersion)
	}
	version := opts.InstanceVersion
	if doc.Identity.InstanceVersion != nil {
		version = *doc.Identity.InstanceVersion
	}
	max, err := st.MaxInstanceVersion(doc.Identity.Namespace, doc.Identity.Type, doc.Identity.ID)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	if version == 0 {
		version = max + 1
	} else if version <= max {
		return nil, fmt.Errorf("publish: instance version %d must exceed the line's highest (%d)", version, max)
	}

	// Content: the structured object ({fields}) compiles into the
	// canonical structured-json payload; the legacy text shape
	// ({text}) stays structured-text.
	representation := exchange.ContentRepresentation
	payload := []byte(doc.Content.Text)
	if doc.Content.Fields != nil {
		representation = exchange.StructuredJSON
		payload, err = conformance.ContentJSON(doc.Content.Fields, conformance.RequiredSectionsFor(doc.Identity.Type))
		if err != nil {
			return nil, fmt.Errorf("publish: cannot encode the inline content: %w", err)
		}
	}

	u := &exchange.Unit{
		Identity: exchange.Identity{
			Namespace:       doc.Identity.Namespace,
			Type:            doc.Identity.Type,
			ID:              doc.Identity.ID,
			InstanceVersion: version,
		},
		Revision: doc.Revision,
		Author:   doc.Author,
		Created:  doc.Created,
		Updated:  doc.Updated,
		StateVector: exchange.StateVector{
			ContentState:   doc.State["contentState"],
			ExecutionState: doc.State["executionState"],
			PlanningState:  doc.State["planningState"],
			ContainerState: doc.State["containerState"],
			ExistenceState: doc.State["existenceState"],
			NoteState:      doc.State["noteState"],
		},
		Phase: doc.Phase,
		Classification: exchange.Classification{
			Dimension:           doc.Classification.Dimension,
			DimensionsSecondary: doc.Classification.DimensionsSecondary,
			Domain:              doc.Classification.Domain,
		},
		Content: exchange.ContentRef{
			Representation: representation,
			File:           "content",
		},
		ContentPayload: payload,
		ChangeLog:      doc.ChangeLog,
		Relationships:  doc.Relationships,
	}
	// The change-log domain follows the §3.2 camelCase schema
	// (contentState); the CKO model carries the kebab domain names.
	for i := range u.ChangeLog {
		u.ChangeLog[i].Domain = conformance.StateKeyKebab(u.ChangeLog[i].Domain)
	}
	if u.ChangeLog == nil {
		u.ChangeLog = []exchange.ChangeLogEntry{}
	}
	if u.Relationships == nil {
		u.Relationships = []exchange.Relationship{}
	}
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()

	resolver := newStoreResolver(st)
	report, err := conformance.ValidateCKO(u, conformance.ValidateCKOOptions{
		Resolve: resolver.Resolve,
	})
	if err != nil {
		return nil, fmt.Errorf("publish: validation failed: %w", err)
	}
	// Store failures during resolution surface as findings (naming the
	// underlying error) instead of a silent "unresolved reference".
	report.Results = append(report.Results,
		resolver.Findings(u.CanonicalIdentityForm, u.StateVector.ContentState)...)
	if !report.Pass() {
		return nil, &PublishError{Target: u.CanonicalIdentityForm, Report: report}
	}
	return persistPublishedUnit(st, "", u)
}

// --- Drafts ---

// Drafts lists the draft backlog: every draft of one project, or of all
// projects when project is "", ordered deterministically (project by
// name, then type, then id — the file name order). Drafts whose file
// fails the single-file classification (or whose type prefix is not a
// known EKA token) are excluded from the backlog; the drafts directory
// is owned by the Authoring API.
func (AuthoringService) Drafts(rt *Runtime, project string) ([]Draft, error) {
	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	root := draftsRoot(ws)
	if project != "" {
		return draftsInProject(root, project)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []Draft{}, nil
		}
		return nil, fmt.Errorf("authoring: cannot list drafts: %w", err)
	}
	var out []Draft
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		got, err := draftsInProject(root, e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	if out == nil {
		out = []Draft{}
	}
	return out, nil
}

// draftsInProject lists the drafts of one project directory, ordered by
// file name (type, then id — ids may contain hyphens; the type token is
// the filename prefix before the first hyphen).
func draftsInProject(root, project string) ([]Draft, error) {
	dir := filepath.Join(root, project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Draft{}, nil
		}
		return nil, fmt.Errorf("authoring: cannot list drafts of project %s: %w", project, err)
	}
	var out []Draft
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		var base string
		switch {
		case strings.HasSuffix(name, ".json"):
			base = strings.TrimSuffix(name, ".json")
		case strings.HasSuffix(name, ".md"):
			base = strings.TrimSuffix(name, ".md") // legacy drafts stay listed
		default:
			continue
		}
		idx := strings.Index(base, "-")
		if idx < 0 {
			continue // not a <type>-<id> draft file
		}
		typeToken, id := base[:idx], base[idx+1:]
		if _, ok := conformance.DomainForToken(typeToken); !ok {
			continue // not a draft of a known EKA type
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("authoring: cannot read draft %s: %w", path, err)
		}
		d := Draft{
			Project: project,
			Type:    typeToken,
			ID:      id,
			Path:    path,
			Updated: info.ModTime().UTC().Format(time.RFC3339),
		}
		if artifact, err := conformance.ScanFile(path); err == nil && artifact != nil {
			d.Namespace = artifact.Namespace
		}
		out = append(out, d)
	}
	if out == nil {
		out = []Draft{}
	}
	return out, nil
}

// --- ValidateDraft ---

// ValidateDraft re-validates ONE draft at CKO level without persisting
// anything: the post-edit re-validation (spec §4.2) and the per-draft
// marker of `eka draft list` (spec §4.3). It shares the publish
// pipeline up to the verdict — resolve the draft, classify, build the
// unit exactly like publish would (frontmatter instance-version when
// present, else the auto-assigned next version), run ValidateCKO with
// the store resolver — and returns the report. Non-destructive: never
// writes, never deletes, never inserts.
//
// Errors carry the publish classes: *DraftNotFoundError (missing
// draft), *conformance.ScanError (structural classification findings).
// The forward-only instance-version check is publish's concern, not a
// rule finding, so it is not surfaced here (documented).
// DraftValidation is the outcome of one draft validation run: the
// CKO-level report plus the resolved draft reference (its Note names
// the project when the resolution fell back across projects).
type DraftValidation struct {
	Ref    DraftRef
	Report *ValidationReport
}

// ValidateDraft re-validates one draft (CKO-level, no location rules):
// the same pipeline as Publish minus persistence (spec §4.2/§4.3 —
// `eka edit` re-validation and the `eka draft list` marker share it).
// projectHint scopes the lookup ("" = the cwd repository's project,
// with the cross-project fallback). It never writes, never deletes,
// never inserts.
//
// Errors carry the publish classes: *DraftNotFoundError (missing
// draft), *conformance.ScanError (structural classification findings).
// The forward-only instance-version check is publish's concern, not a
// rule finding, so it is not surfaced here (documented).
func (AuthoringService) ValidateDraft(rt *Runtime, target string, projectHint string) (*DraftValidation, error) {
	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	st, err := rt.requireStore()
	if err != nil {
		return nil, err
	}
	ref, err := parseDraftTarget(target)
	if err != nil {
		return nil, fmt.Errorf("validate draft: %w", err)
	}
	df, err := resolveDraftFile(ws, rt, projectHint, ref.Type, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("validate draft: %w", err)
	}
	path := df.Path
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, &DraftNotFoundError{Target: target, Project: df.Project}
		}
		return nil, fmt.Errorf("validate draft: cannot access draft %s: %w", target, err)
	}
	artifact, err := conformance.ScanFile(path)
	if err != nil {
		return nil, fmt.Errorf("validate draft: %w", err)
	}
	if artifact == nil {
		return nil, fmt.Errorf("validate draft: draft %s is not a knowledge artifact (missing type/id frontmatter)", target)
	}
	// Identity is the frontmatter, not the file name: a target that
	// resolves to a file carrying a different identity is refused like
	// publish's (rename the file, or validate the draft's own identity).
	if artifact.Type != ref.Type || artifact.ID != ref.ID {
		return nil, fmt.Errorf("validate draft: draft file %s carries identity %s:%s; expected %s:%s — rename the file or publish the draft's own identity",
			path, artifact.Type, artifact.ID, ref.Type, ref.ID)
	}
	version := artifact.InstanceVersion
	if version == 0 {
		max, err := st.MaxInstanceVersion(artifact.Namespace, artifact.Type, artifact.ID)
		if err != nil {
			return nil, fmt.Errorf("validate draft: %w", err)
		}
		version = max + 1
	}
	u := unitFromDraft(artifact, version)
	resolver := newStoreResolver(st)
	report, err := conformance.ValidateCKO(u, conformance.ValidateCKOOptions{
		Resolve: resolver.Resolve,
	})
	if err != nil {
		return nil, fmt.Errorf("validate draft: %w", err)
	}
	// Store failures during resolution surface as findings (naming the
	// underlying error) instead of a silent "unresolved reference".
	report.Results = append(report.Results,
		resolver.Findings(artifact.RelPath, artifact.States[conformance.DomainContentState])...)
	return &DraftValidation{Ref: df, Report: report}, nil
}

// ResolveDraft locates one draft file without validating it: the
// editor path needs the file before any re-validation runs. projectHint
// scopes the lookup like ValidateDraft's ("" = the cwd repository's
// project, with the cross-project fallback). Errors carry
// *DraftNotFoundError.
func (AuthoringService) ResolveDraft(rt *Runtime, target string, projectHint string) (DraftRef, error) {
	ws, err := rt.requireWorkspace()
	if err != nil {
		return DraftRef{}, err
	}
	ref, err := parseDraftTarget(target)
	if err != nil {
		return DraftRef{}, fmt.Errorf("resolve draft: %w", err)
	}
	return resolveDraftFile(ws, rt, projectHint, ref.Type, ref.ID)
}

// --- DiscardDraft ---

// DiscardDraft deletes one draft file without publishing. The TTY
// confirmation is the CLI's concern (this API deletes); a missing draft
// is *DraftNotFoundError. projectHint scopes the draft like Publish's
// opts.Project ("" = the cwd repository's project). force is accepted
// for API symmetry with the CLI contract and has no runtime effect.
// DiscardDraft deletes one draft file without publishing. The TTY
// confirmation is the CLI's concern (this API deletes); a missing draft
// is *DraftNotFoundError. projectHint scopes the lookup like
// ValidateDraft's ("" = the cwd repository's project, with the
// cross-project fallback). force is accepted for API symmetry with the
// CLI contract and has no runtime effect. The returned note names the
// project when the resolution fell back across projects.
func (AuthoringService) DiscardDraft(rt *Runtime, target string, projectHint string, force bool) (string, error) {
	ws, err := rt.requireWorkspace()
	if err != nil {
		return "", err
	}
	ref, err := parseDraftTarget(target)
	if err != nil {
		return "", fmt.Errorf("discard: %w", err)
	}
	df, err := resolveDraftFile(ws, rt, projectHint, ref.Type, ref.ID)
	if err != nil {
		return "", fmt.Errorf("discard: %w", err)
	}
	path := df.Path
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", &DraftNotFoundError{Target: target, Project: df.Project}
		}
		return "", fmt.Errorf("discard: cannot access draft %s: %w", target, err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("discard: cannot remove draft %s: %w", path, err)
	}
	return df.Note, nil
}
