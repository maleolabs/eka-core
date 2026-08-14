package runtime

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file tests the draft-publish workflow of the Authoring API:
// NewDraft (template determinism, validation, collision), Publish
// (end-to-end persistence, version assignment, validation refusal,
// duplicate-publish guard), PublishInline, Drafts ordering and
// DiscardDraft.

// draftRuntime returns a Runtime plus a registered repository whose
// directory becomes the test's working directory — the cwd-repo
// resolution rule of Publish/DiscardDraft (project = the cwd
// repository's project).
func draftRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	r := testRuntime(t)
	repoDir := t.TempDir()
	project, _, _, err := r.Workspace.RegisterRepo(repoDir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoDir)
	return r, project.ID
}

// stoContent is a publishable sto- body: the two required sections.
const stoContent = "# my-item\n\n## Description\n\nd\n\n## Acceptance Criteria\n\nac\n"

// newSTODraft scaffolds a sto- draft (the smoke-test shape) in the
// given project and returns the draft.
func newSTODraft(t *testing.T, r *Runtime, project, ns, id string, rels []exchange.Relationship) *Draft {
	t.Helper()
	content := filepath.Join(t.TempDir(), "content.json")
	if err := os.WriteFile(content, []byte(`{"description": "d", "acceptanceCriteria": "ac"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:       project,
		Namespace:     ns,
		Type:          "sto",
		ID:            id,
		Relationships: rels,
		ContentFile:   content,
	})
	if err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	return d
}

// --- NewDraft ----------------------------------------------------------

// TestNewDraftCreatesDraft: the scaffold writes
// <workspace>/drafts/<project>/<type>-<id>.md with the deterministic
// template: identity, owned state fields with initial values, the
// change-log covering every owned domain, and the type's required
// section headings — no instance-version (assigned at publish).
func TestNewDraftCreatesDraft(t *testing.T) {
	r, project := draftRuntime(t)
	d := newSTODraft(t, r, project, "feather", "my-item", nil)

	if d.Project != project || d.Namespace != "feather" || d.Type != "sto" || d.ID != "my-item" {
		t.Errorf("Draft = %+v, want project %s feather/sto:my-item", d, project)
	}
	if d.Updated == "" {
		t.Error("Updated must carry the file modification time")
	}
	path := filepath.Join(r.Path(), "drafts", project, "sto-my-item.json")
	if d.Path != path {
		t.Errorf("Path = %q, want %q", d.Path, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("draft file missing: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"namespace": "feather"`,
		`"type": "sto"`,
		`"id": "my-item"`,
		`"revision": 1`,
		`"executionState": "planned"`,
		`"existenceState": "active"`,
		`"domain": "existenceState"`,
		`"domain": "executionState"`,
		`"description": "d"`,
		`"acceptanceCriteria": "ac"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("draft template missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "instanceVersion") {
		t.Errorf("draft must not carry instanceVersion:\n%s", text)
	}
	if strings.Contains(text, `"contentState"`) {
		t.Errorf("sto- drafts must not carry contentState (not an owned domain):\n%s", text)
	}
}

// TestNewDraftTemplateDeterminism: identical scaffold requests produce
// byte-identical draft files (the change-log date is the only
// time-derived value, so two calls on the same day agree).
func TestNewDraftTemplateDeterminism(t *testing.T) {
	r, project := draftRuntime(t)
	content := filepath.Join(t.TempDir(), "content.json")
	if err := os.WriteFile(content, []byte(`{"description": "d", "acceptanceCriteria": "ac"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	req := NewDraftRequest{
		Project:       project,
		Namespace:     "feather",
		Type:          "sto",
		ID:            "x",
		Relationships: []exchange.Relationship{{Type: "depends-on", Target: "ctr:wave-7"}},
		ContentFile:   content,
	}
	d1, err := Authoring.NewDraft(r, req)
	if err != nil {
		t.Fatal(err)
	}
	// The same request under a different project: the project is not
	// part of the draft bytes, so the files must be byte-identical.
	req2 := req
	req2.Project = "proj-other"
	d2, err := Authoring.NewDraft(r, req2)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(d1.Path)
	b, _ := os.ReadFile(d2.Path)
	if string(a) != string(b) {
		t.Errorf("identical requests must produce byte-identical drafts:\n---\n%s\n---\n%s", a, b)
	}
	if !strings.Contains(string(a), `"dependsOn": [`) || !strings.Contains(string(a), `"ctr:wave-7"`) {
		t.Errorf("the relationship field must be in the template:\n%s", a)
	}
}

// TestNewDraftValidation: unknown type tokens, empty identity
// components, missing project/namespace and a phase on a non-versioned
// type are refused without writing anything.
func TestNewDraftValidation(t *testing.T) {
	r, project := draftRuntime(t)
	cases := []struct {
		name string
		req  NewDraftRequest
		want string
	}{
		{"unknown type", NewDraftRequest{Project: project, Namespace: "ns", Type: "bogus", ID: "x"}, "unknown artifact type"},
		{"empty id", NewDraftRequest{Project: project, Namespace: "ns", Type: "sto", ID: ""}, "non-empty"},
		{"empty project", NewDraftRequest{Project: "", Namespace: "ns", Type: "sto", ID: "x"}, "project"},
		{"empty namespace", NewDraftRequest{Project: project, Namespace: "", Type: "sto", ID: "x"}, "namespace"},
		{"phase on sto", NewDraftRequest{Project: project, Namespace: "ns", Type: "sto", ID: "x", Phase: "mvp"}, "scp-/plan-"},
	}
	for _, c := range cases {
		if _, err := Authoring.NewDraft(r, c.req); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want a refusal naming %q", c.name, err, c.want)
		}
	}
	// Nothing was written (a missing drafts dir is an empty backlog).
	entries, err := os.ReadDir(filepath.Join(r.Path(), "drafts", project))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("cannot inspect drafts dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("refused scaffolds must not write drafts: %v", entries)
	}
}

// TestNewDraftPhaseChangeLogEntry: a scaffolded draft carrying a phase
// covers the phase context with a change-log entry ("-" -> the given
// value, by the author, same date as the state entries) — rule 7
// requires a change-log entry for every field with a change-log domain,
// so a plan draft scaffolded with --phase must validate clean without
// edits (R6 is orthogonal: knowledge artifacts need --dimension).
func TestNewDraftPhaseChangeLogEntry(t *testing.T) {
	r, project := draftRuntime(t)
	d, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:   project,
		Namespace: "feather",
		Type:      "plan",
		ID:        "roadmap-v2",
		Dimension: "planning",
		Phase:     "mvp",
		By:        conformance.User("Ada Lovelace"),
	})
	if err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	data, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"phase": "mvp"`, // the phase field itself
		// The phase change-log entry, scaffolded alongside the field.
		`"domain": "phase"`,
		`"from": "-"`,
		`"to": "mvp"`,
		`"by": "Ada Lovelace"`,
		// The owned-domain entries stay.
		`"domain": "contentState"`,
		`"domain": "planningState"`,
		`"domain": "existenceState"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("draft template missing %q:\n%s", want, text)
		}
	}
	// R7: the change-log covers the phase context, so the scaffolded
	// draft validates clean — the acceptance criterion (`eka new
	// plan:x --phase mvp`, then `eka draft validate` passes).
	dv, err := Authoring.ValidateDraft(r, "feather/plan:roadmap-v2", "")
	if err != nil {
		t.Fatalf("ValidateDraft: %v", err)
	}
	if !dv.Report.Pass() {
		t.Errorf("a phase-carrying scaffolded draft must validate clean, got %d errors: %+v",
			dv.Report.ErrorCount(), dv.Report.SortedResults())
	}
}

// TestNewDraftCollision: a second draft with the same project/type/id
// is refused.
func TestNewDraftCollision(t *testing.T) {
	r, project := draftRuntime(t)
	newSTODraft(t, r, project, "feather", "my-item", nil)
	_, err := Authoring.NewDraft(r, NewDraftRequest{Project: project, Namespace: "feather", Type: "sto", ID: "my-item"})
	if err == nil {
		t.Fatal("a colliding draft must be refused")
	}
	if !strings.Contains(err.Error(), "already exists in project") {
		t.Errorf("collision error = %v, want the already-exists message", err)
	}
}

// TestNewDraftContentFile: the content file JSON object is merged into
// the draft's content (the template's required keys stay); a missing
// file and a raw-text file are refused.
func TestNewDraftContentFile(t *testing.T) {
	r, project := draftRuntime(t)
	content := filepath.Join(t.TempDir(), "content.json")
	if err := os.WriteFile(content, []byte(`{"description": "custom", "extra": "kept"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "sto", ID: "custom", ContentFile: content,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"description": "custom"`) {
		t.Errorf("the draft content must carry the merged object:\n%s", text)
	}
	if !strings.Contains(text, `"extra": "kept"`) {
		t.Errorf("extra content keys from the file must be merged:\n%s", text)
	}
	if !strings.Contains(text, `"acceptanceCriteria"`) {
		t.Errorf("the template's required content keys must stay:\n%s", text)
	}
	// A missing content file is refused.
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "sto", ID: "nope", ContentFile: filepath.Join(t.TempDir(), "missing.json"),
	}); err == nil {
		t.Error("a missing content file must be refused")
	}
	// Raw text is rejected for JSON drafts (JSON object only).
	raw := filepath.Join(t.TempDir(), "raw.txt")
	if err := os.WriteFile(raw, []byte("plain text body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "sto", ID: "raw", ContentFile: raw,
	}); err == nil || !strings.Contains(err.Error(), "JSON object only") {
		t.Errorf("raw text content file = %v, want the JSON-object-only refusal", err)
	}
}

// --- Publish -----------------------------------------------------------

// TestPublishEndToEnd: new -> publish -> the CKO is readable through
// Knowledge.Object, the draft file is gone, and a second publish fails
// at the read (the duplicate-publish guard).
func TestPublishEndToEnd(t *testing.T) {
	r, project := draftRuntime(t)
	d := newSTODraft(t, r, project, "feather", "my-item", nil)

	res, err := Authoring.Publish(r, "feather/sto:my-item", PublishOptions{})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Form != "feather/sto:my-item:1" {
		t.Errorf("Form = %q, want feather/sto:my-item:1", res.Form)
	}
	if res.InstanceVersion != 1 {
		t.Errorf("InstanceVersion = %d, want 1 (auto-assigned for a new line)", res.InstanceVersion)
	}
	if res.ObjectHash == "" {
		t.Error("ObjectHash must be non-empty")
	}
	if _, err := os.Stat(d.Path); !os.IsNotExist(err) {
		t.Errorf("the draft file must be removed by publish, stat err = %v", err)
	}

	u, ok, err := r.Knowledge.Object("feather/sto:my-item:1")
	if err != nil || !ok {
		t.Fatalf("Knowledge.Object = %v, %v; want the published CKO", ok, err)
	}
	if u.Identity.Namespace != "feather" || u.Identity.Type != "sto" || u.Identity.ID != "my-item" {
		t.Errorf("published unit = %+v", u.Identity)
	}
	if u.Digest != res.ObjectHash {
		t.Errorf("unit digest = %q, want the returned object hash %q", u.Digest, res.ObjectHash)
	}
	if u.Classification.Domain != "Execution" {
		t.Errorf("domain = %q, want Execution (derived from the token)", u.Classification.Domain)
	}

	// Second publish: the draft file is the single-use ticket.
	_, err = Authoring.Publish(r, "feather/sto:my-item", PublishOptions{})
	var dne *DraftNotFoundError
	if !errors.As(err, &dne) {
		t.Fatalf("second publish error = %v, want *DraftNotFoundError", err)
	}
	if !strings.Contains(err.Error(), "already published or discarded") {
		t.Errorf("error must carry the duplicate-publish guard message: %v", err)
	}
}

// TestPublishAutoInstanceVersion: publishing the same line again (from
// a second project's draft) auto-assigns max+1.
func TestPublishAutoInstanceVersion(t *testing.T) {
	r, projectA := draftRuntime(t)
	newSTODraft(t, r, projectA, "feather", "line", nil)
	res1, err := Authoring.Publish(r, "feather/sto:line", PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res1.InstanceVersion != 1 {
		t.Errorf("first instance version = %d, want 1", res1.InstanceVersion)
	}

	// A second draft of the SAME line in another project (different
	// drafts directory, no collision), published from a repository of
	// that project (the cwd resolution rule).
	projectB := "proj-b"
	repoB := t.TempDir()
	if _, _, _, err := r.Workspace.RegisterRepo(repoB, projectB); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoB)
	newSTODraft(t, r, projectB, "feather", "line", nil)
	res2, err := Authoring.Publish(r, "feather/sto:line", PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.InstanceVersion != 2 {
		t.Errorf("second instance version = %d, want 2 (max 1 + 1)", res2.InstanceVersion)
	}
	if _, ok, err := r.Knowledge.Object("feather/sto:line:2"); err != nil || !ok {
		t.Errorf("the v2 instance must be readable: %v, %v", ok, err)
	}
}

// TestPublishInstanceVersionOverride: an explicit instance version
// beyond the line's highest is honored; one at or below it is refused.
func TestPublishInstanceVersionOverride(t *testing.T) {
	r, project := draftRuntime(t)
	newSTODraft(t, r, project, "feather", "ovr", nil)
	res, err := Authoring.Publish(r, "feather/sto:ovr", PublishOptions{InstanceVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Form != "feather/sto:ovr:5" {
		t.Errorf("Form = %q, want feather/sto:ovr:5", res.Form)
	}

	// Draft a new instance of the same line (second project), then
	// override with a version at or below the highest (5) — refused.
	projectB := "proj-b"
	repoB := t.TempDir()
	if _, _, _, err := r.Workspace.RegisterRepo(repoB, projectB); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoB)
	newSTODraft(t, r, projectB, "feather", "ovr", nil)
	_, err = Authoring.Publish(r, "feather/sto:ovr", PublishOptions{InstanceVersion: 5})
	if err == nil || !strings.Contains(err.Error(), "must exceed the line's highest") {
		t.Errorf("an override at the line's highest must be refused, got %v", err)
	}
	// The draft survives the refused override.
	if _, err := os.Stat(draftPathAt(r, projectB, "sto-ovr.json")); err != nil {
		t.Errorf("the draft must survive a refused override: %v", err)
	}
}

func draftPathAt(r *Runtime, project, name string) string {
	return filepath.Join(r.Path(), "drafts", project, name)
}

// TestPublishValidationFailureKeepsDraft: a draft that fails CKO-level
// validation is refused with *PublishError carrying the report, the
// draft file stays, and nothing is persisted.
func TestPublishValidationFailureKeepsDraft(t *testing.T) {
	r, project := draftRuntime(t)
	// Hand-write a spec- draft with an invalid content-state value.
	dir := filepath.Join(r.Path(), "drafts", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "spec-broken.md")
	bad := "---\nnamespace: feather\ntype: spec\nid: broken\nrevision: 1\n"
	bad += "content-state: bogus\nexistence-state: active\n"
	bad += "change-log:\n  - date: 2026-08-07\n    domain: existence-state\n    from: \"-\"\n    to: active\n    by: Engineering\n  - date: 2026-08-07\n    domain: content-state\n    from: \"-\"\n    to: bogus\n    by: Engineering\n"
	bad += "---\n# broken\n\n## Purpose\n\np\n\n## Content\n\nc\n"
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Authoring.Publish(r, "feather/spec:broken", PublishOptions{})
	var pe *PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("Publish error = %v, want *PublishError", err)
	}
	if pe.Report == nil || pe.Report.Pass() {
		t.Error("the PublishError must carry the failing report")
	}
	if pe.Report.ErrorCount() == 0 {
		t.Error("the report must carry blocking errors")
	}
	if !strings.Contains(err.Error(), "the draft was kept") {
		t.Errorf("error must state the draft was kept: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the draft file must survive a refused publish: %v", err)
	}
	if _, ok, err := r.Knowledge.Object("feather/spec:broken:1"); err != nil || ok {
		t.Errorf("a refused publish must persist nothing: %v, %v", ok, err)
	}
}

// TestPublishUnresolvedRelationship: an unresolved reference on a
// non-draft unit blocks the publish (R5 error, draft kept); on a
// content-state draft unit it is tolerated (warning, published).
func TestPublishUnresolvedRelationship(t *testing.T) {
	r, project := draftRuntime(t)
	// sto- drafts carry no content-state: unresolved -> blocking.
	newSTODraft(t, r, project, "feather", "blocked", []exchange.Relationship{
		{Type: "depends-on", Target: "feather/ctr:ghost"},
	})
	_, err := Authoring.Publish(r, "feather/sto:blocked", PublishOptions{})
	var pe *PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("a non-draft unresolved reference must block, got %v", err)
	}
	found := false
	for _, res := range pe.Report.Results {
		if res.Rule == "R5" && res.Severity == conformance.SeverityError {
			found = true
		}
	}
	if !found {
		t.Errorf("the report must carry the R5 unresolved-reference error: %+v", pe.Report.SortedResults())
	}
	if _, err := os.Stat(draftPathAt(r, project, "sto-blocked.json")); err != nil {
		t.Errorf("the blocked draft must be kept: %v", err)
	}

	// A knowledge-type draft with the template's content-state: draft
	// tolerates the same unresolved reference (warning only) and
	// publishes.
	newSpecDraft(t, r, project, "feather", "tolerated", []exchange.Relationship{
		{Type: "depends-on", Target: "feather/ctr:ghost"},
	})
	res, err := Authoring.Publish(r, "feather/spec:tolerated", PublishOptions{})
	if err != nil {
		t.Fatalf("draft tolerance must publish, got %v", err)
	}
	if res.Form != "feather/spec:tolerated:1" {
		t.Errorf("Form = %q", res.Form)
	}
}

// newSpecDraft scaffolds a spec- draft (content-state: draft from the
// template) with the required Purpose/Content sections.
func newSpecDraft(t *testing.T, r *Runtime, project, ns, id string, rels []exchange.Relationship) *Draft {
	t.Helper()
	content := filepath.Join(t.TempDir(), "content.json")
	if err := os.WriteFile(content, []byte(`{"purpose": "p", "content": "c"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:       project,
		Namespace:     ns,
		Type:          "spec",
		ID:            id,
		Dimension:     "specifications",
		Relationships: rels,
		ContentFile:   content,
	})
	if err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	return d
}

// TestPublishDraftNotFound: publishing a nonexistent draft is the
// duplicate-publish guard (*DraftNotFoundError).
func TestPublishDraftNotFound(t *testing.T) {
	r, _ := draftRuntime(t)
	_, err := Authoring.Publish(r, "feather/sto:ghost", PublishOptions{})
	var dne *DraftNotFoundError
	if !errors.As(err, &dne) {
		t.Fatalf("Publish of a missing draft = %v, want *DraftNotFoundError", err)
	}
}

// TestPublishNotAKnowledgeArtifact: a draft file without type/id
// frontmatter is refused as not-a-knowledge-artifact; a malformed one
// fails with the structural scan error.
func TestPublishNotAKnowledgeArtifact(t *testing.T) {
	r, project := draftRuntime(t)
	dir := filepath.Join(r.Path(), "drafts", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Convention document shape: no type/id.
	if err := os.WriteFile(filepath.Join(dir, "sto-notes.md"), []byte("# Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Authoring.Publish(r, "feather/sto:notes", PublishOptions{})
	if err == nil || !strings.Contains(err.Error(), "is not a knowledge artifact") {
		t.Errorf("a convention document draft = %v, want the not-a-knowledge-artifact refusal", err)
	}
	// Malformed frontmatter.
	if err := os.WriteFile(filepath.Join(dir, "sto-broken.md"), []byte("---\nnamespace: x\nnot: [yaml\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Authoring.Publish(r, "feather/sto:broken", PublishOptions{})
	var se *conformance.ScanError
	if !errors.As(err, &se) {
		t.Fatalf("a malformed draft = %v, want *conformance.ScanError", err)
	}
	if len(se.Findings) == 0 {
		t.Error("the scan error must carry the parse findings")
	}
}

// TestPublishPublishedFormRefused: a canonical published form (with an
// instance version) is not a draft target.
func TestPublishPublishedFormRefused(t *testing.T) {
	r, _ := draftRuntime(t)
	_, err := Authoring.Publish(r, "feather/sto:my-item:1", PublishOptions{})
	if err == nil || !strings.Contains(err.Error(), "drafts only") {
		t.Errorf("a published form must be refused, got %v", err)
	}
}

// --- PublishInline -----------------------------------------------------

// inlineDoc renders a publishable inline sto- document (the v2.0
// camelCase schema, spec-standard-v2 §3.2).
func inlineDoc(ns, typ, id string, version *int) []byte {
	doc := "identity:\n  namespace: " + ns + "\n  type: " + typ + "\n  id: " + id + "\n"
	if version != nil {
		doc += "  instanceVersion: " + strconv.Itoa(*version) + "\n"
	}
	doc += "revision: 1\n"
	doc += "state:\n  executionState: planned\n  existenceState: active\n"
	doc += "changeLog:\n  - date: 2026-08-07\n    domain: existenceState\n    from: \"-\"\n    to: active\n    by: Engineering\n  - date: 2026-08-07\n    domain: executionState\n    from: \"-\"\n    to: planned\n    by: Engineering\n"
	doc += "relationships: []\n"
	doc += "content:\n  fields:\n    description: d\n    acceptanceCriteria: ac\n"
	return []byte(doc)
}

// TestPublishInlineEndToEnd: structured input becomes an immutable CKO
// in the store with an auto-assigned instance version and the "runtime"
// provenance.
func TestPublishInlineEndToEnd(t *testing.T) {
	r := testRuntime(t)
	res, err := Authoring.PublishInline(r, inlineDoc("feather", "sto", "inline", nil), PublishOptions{})
	if err != nil {
		t.Fatalf("PublishInline: %v", err)
	}
	if res.Form != "feather/sto:inline:1" || res.InstanceVersion != 1 {
		t.Errorf("result = %+v, want feather/sto:inline:1 v1", res)
	}
	u, ok, err := r.Knowledge.Object("feather/sto:inline:1")
	if err != nil || !ok {
		t.Fatalf("Knowledge.Object = %v, %v", ok, err)
	}
	if string(u.ContentPayload) == "" || u.Revision != 1 {
		t.Errorf("inline unit = %+v", u)
	}
	// The provenance is the "runtime" sentinel: the object is
	// workspace-native and appears under no repository.
	refs, err := r.ws.Store().Refs("", "runtime")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Form != "feather/sto:inline:1" {
		t.Errorf("runtime refs = %+v, want the inline object", refs)
	}
	if units, err := r.Knowledge.Units("proj-any", "repo-any"); err != nil || len(units) != 0 {
		t.Errorf("an inline object must not appear under any repository, got %v, %v", units, err)
	}
}

// TestPublishInlineInputVersionWins: the input's instance_version wins
// over the auto-assignment and over opts.
func TestPublishInlineInputVersionWins(t *testing.T) {
	r := testRuntime(t)
	v := 7
	res, err := Authoring.PublishInline(r, inlineDoc("feather", "sto", "inline", &v), PublishOptions{InstanceVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Form != "feather/sto:inline:7" {
		t.Errorf("Form = %q, want the input's version 7 (input wins)", res.Form)
	}
	// The input version must still exceed the line's highest: a second
	// document for the same line pinned to 7 (the current highest) is
	// refused.
	if _, err := Authoring.PublishInline(r, inlineDoc("feather", "sto", "inline", &v), PublishOptions{}); err == nil {
		t.Error("an input version at the line's highest must be refused")
	}
}

// TestPublishInlineValidationFailure: an invalid state value blocks the
// inline publish with *PublishError; nothing is persisted.
func TestPublishInlineValidationFailure(t *testing.T) {
	r := testRuntime(t)
	doc := strings.Replace(string(inlineDoc("feather", "sto", "bad", nil)),
		"executionState: planned", "executionState: bogus", 1)
	_, err := Authoring.PublishInline(r, []byte(doc), PublishOptions{})
	var pe *PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("PublishInline of an invalid state = %v, want *PublishError", err)
	}
	if pe.Report.ErrorCount() == 0 {
		t.Error("the report must carry blocking errors")
	}
	if n, err := r.ws.Store().RefCount(); err != nil || n != 0 {
		t.Errorf("a refused inline publish must persist nothing: %d refs, %v", n, err)
	}
}

// TestPublishInlineParseFailure: invalid YAML is a deterministic parse
// error, not a validation report.
func TestPublishInlineParseFailure(t *testing.T) {
	r := testRuntime(t)
	_, err := Authoring.PublishInline(r, []byte("identity: [broken"), PublishOptions{})
	if err == nil || !strings.Contains(err.Error(), "not valid YAML/JSON") {
		t.Errorf("parse failure = %v, want the deterministic parse error", err)
	}
	var pe *PublishError
	if errors.As(err, &pe) {
		t.Error("a parse failure must not be a PublishError")
	}
}

// TestPublishInlineMissingIdentity: the identity tuple is required.
func TestPublishInlineMissingIdentity(t *testing.T) {
	r := testRuntime(t)
	_, err := Authoring.PublishInline(r, []byte("revision: 1\n"), PublishOptions{})
	if err == nil || !strings.Contains(err.Error(), "identity requires") {
		t.Errorf("missing identity = %v, want the identity refusal", err)
	}
}

// --- Drafts ------------------------------------------------------------

// TestDraftsOrdering: the backlog is deterministic — project, then
// type, then id — and filters by project.
func TestDraftsOrdering(t *testing.T) {
	r, project := draftRuntime(t)
	newSTODraft(t, r, project, "atrium-web", "z-last", nil)
	newSTODraft(t, r, project, "atrium-web", "a-first", nil)
	newSpecDraft(t, r, project, "atrium-api", "spec-item", nil)
	// A second project.
	projectB := "proj-b"
	repoB := t.TempDir()
	if _, _, _, err := r.Workspace.RegisterRepo(repoB, projectB); err != nil {
		t.Fatal(err)
	}
	newSTODraft(t, r, projectB, "atrium-mobile", "mob", nil)

	drafts, err := Authoring.Drafts(r, "")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, d := range drafts {
		keys = append(keys, d.Project+"/"+d.Type+":"+d.ID)
	}
	// Ordering is by file name: "spec-..." < "sto-..." (type), then id.
	want := []string{
		project + "/spec:spec-item",
		project + "/sto:a-first",
		project + "/sto:z-last",
		projectB + "/sto:mob",
	}
	if len(keys) != len(want) {
		t.Fatalf("Drafts = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("Drafts[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
	// Project filter.
	one, err := Authoring.Drafts(r, projectB)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].Project != projectB {
		t.Errorf("Drafts(proj-b) = %+v", one)
	}
	// Namespace is read from the frontmatter.
	byID := map[string]Draft{}
	for _, d := range drafts {
		byID[d.ID] = d
	}
	if byID["spec-item"].Namespace != "atrium-api" {
		t.Errorf("spec draft namespace = %q, want atrium-api", byID["spec-item"].Namespace)
	}
	if byID["a-first"].Namespace != "atrium-web" {
		t.Errorf("sto draft namespace = %q, want atrium-web", byID["a-first"].Namespace)
	}
	// Empty backlog.
	none, err := Authoring.Drafts(r, "proj-empty")
	if err != nil {
		t.Fatal(err)
	}
	if none == nil || len(none) != 0 {
		t.Errorf("Drafts(proj-empty) = %v, want empty non-nil", none)
	}
}

// --- DiscardDraft ------------------------------------------------------

// TestDiscardDraft: the draft file is removed; a second discard (or a
// discard of a missing draft) is *DraftNotFoundError.
func TestDiscardDraft(t *testing.T) {
	r, project := draftRuntime(t)
	d := newSTODraft(t, r, project, "feather", "discard-me", nil)
	if _, err := Authoring.DiscardDraft(r, "feather/sto:discard-me", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.Path); !os.IsNotExist(err) {
		t.Errorf("the draft file must be gone: %v", err)
	}
	_, err := Authoring.DiscardDraft(r, "feather/sto:discard-me", "", true)
	var dne *DraftNotFoundError
	if !errors.As(err, &dne) {
		t.Fatalf("second discard = %v, want *DraftNotFoundError", err)
	}
}

// TestDiscardDraftPublishedFormRefused: canonical published forms are
// not draft targets for discard either.
func TestDiscardDraftPublishedFormRefused(t *testing.T) {
	r, _ := draftRuntime(t)
	if _, err := Authoring.DiscardDraft(r, "feather/sto:x:1", "", true); err == nil {
		t.Error("a published form must be refused for discard")
	}
}

// --- M2: rule 8 / rule 9 resolve through the runtime callback --------

// seedApprovedPlan publishes a plan draft and approves it through the
// Authoring transition pipeline (the container lifecycle path): the
// plan line exists in the store as approved, ready to be locked by a
// container publish. The transition resolves the repository context
// from the cwd; the draftRuntime repo carries no eka.yaml, so the
// identity file is written first.
func seedApprovedPlan(t *testing.T, r *Runtime, project, ns, id string) {
	t.Helper()
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: ns, Type: "plan", ID: id, Dimension: "planning",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Authoring.Publish(r, ns+"/plan:"+id, PublishOptions{}); err != nil {
		t.Fatalf("plan publish: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	writeRuntimeEKAFile(t, cwd, project, filepath.Base(cwd), ns)
	if _, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".",
		Target:   "plan:" + id,
		To:       "approved",
		By:       "test-agent",
	}); err != nil {
		t.Fatalf("plan approve: %v", err)
	}
}

// TestPublishTicketDerivingFromContainer (M2): a conformant tkt- draft
// whose derives-from references an EXISTING container publishes —
// rule 8's container check resolves through the same callback
// semantics as rule 5.
func TestPublishTicketDerivingFromContainer(t *testing.T) {
	r, project := draftRuntime(t)
	// The container must exist in the runtime: publish a plan draft,
	// approve it, then publish a ctr- draft deriving from it (the
	// template: container-state planned, existence-state active,
	// Objective/Work Items/Change Log sections; publish performs no
	// lock — the plan locks at activation, Option B).
	seedApprovedPlan(t, r, project, "feather", "roadmap-v1")
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:       project,
		Namespace:     "feather",
		Type:          "ctr",
		ID:            "wave-7",
		Relationships: []exchange.Relationship{{Type: "depends-on", Target: "feather/plan:roadmap-v1"}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := Authoring.Publish(r, "feather/ctr:wave-7", PublishOptions{})
	if err != nil {
		t.Fatalf("container publish: %v", err)
	}
	if res.Form != "feather/ctr:wave-7:1" {
		t.Fatalf("container form = %q", res.Form)
	}

	// The ticket draft: the JSON template carries the projection header
	// in the commands content value (rule 8) and the two required
	// content keys (commands / projectedStatus) — publishable without
	// edits.
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:       project,
		Namespace:     "feather",
		Type:          "tkt",
		ID:            "t1",
		Relationships: []exchange.Relationship{{Type: "derives-from", Target: "feather/ctr:wave-7"}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err = Authoring.Publish(r, "feather/tkt:t1", PublishOptions{})
	if err != nil {
		t.Fatalf("ticket publish must resolve its container through the callback, got %v", err)
	}
	if res.Form != "feather/tkt:t1:1" {
		t.Errorf("ticket form = %q", res.Form)
	}
}

// TestPublishTicketMissingContainerBlocked (M2): the same ticket with
// an unresolvable container reference is refused (tkt- carries no
// content-state, so no draft tolerance applies).
func TestPublishTicketMissingContainerBlocked(t *testing.T) {
	r, project := draftRuntime(t)
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:       project,
		Namespace:     "feather",
		Type:          "tkt",
		ID:            "t1",
		Relationships: []exchange.Relationship{{Type: "derives-from", Target: "feather/ctr:ghost"}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := Authoring.Publish(r, "feather/tkt:t1", PublishOptions{})
	var pe *PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("a ticket with an unresolvable container = %v, want *PublishError", err)
	}
	found := false
	for _, res := range pe.Report.Results {
		if res.Rule == "R8" && strings.Contains(res.Message, "container (ctr-) artifact") {
			found = true
		}
	}
	if !found {
		t.Errorf("R8 container finding missing: %+v", pe.Report.SortedResults())
	}
	if _, err := os.Stat(draftPathAt(r, project, "tkt-t1.json")); err != nil {
		t.Errorf("the blocked ticket draft must be kept: %v", err)
	}
}

// TestPublishSupersedingADR (M2): an adr- draft whose supersedes points
// at an EXISTING published adr- publishes (rule 9's replacement check
// resolves through the callback; the draft is the replacement, so its
// own content-state is not superseded). With the target missing, draft
// tolerance applies; a non-draft superseding adr is blocked.
func TestPublishSupersedingADR(t *testing.T) {
	r, project := draftRuntime(t)
	// The superseded adr-001 exists in the runtime (published first).
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "adr", ID: "001",
		Dimension: "decisions",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Authoring.Publish(r, "feather/adr:001", PublishOptions{}); err != nil {
		t.Fatalf("adr-001 publish: %v", err)
	}

	adrDraft := func(id string, rels []exchange.Relationship) *Draft {
		t.Helper()
		body := filepath.Join(t.TempDir(), "adr.json")
		if err := os.WriteFile(body, []byte(`{"context": "c", "decision": "d", "consequences": "c", "alternativesConsidered": "a"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		d, err := Authoring.NewDraft(r, NewDraftRequest{
			Project: project, Namespace: "feather", Type: "adr", ID: id,
			Dimension: "decisions", Relationships: rels, ContentFile: body,
		})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	// The replacement (template content-state: proposed for adr-)
	// supersedes the existing adr-001: resolves -> publishes.
	adrDraft("002", []exchange.Relationship{{Type: "supersedes", Target: "feather/adr:001"}})
	if _, err := Authoring.Publish(r, "feather/adr:002", PublishOptions{}); err != nil {
		t.Fatalf("a superseding adr with a resolving target must publish, got %v", err)
	}

	// Missing target + content-state draft (a spec- draft — draft is a
	// valid content-state there): tolerated (warning), publishes.
	newSpecDraft(t, r, project, "feather", "tol", []exchange.Relationship{
		{Type: "supersedes", Target: "feather/adr:ghost"},
	})
	if _, err := Authoring.Publish(r, "feather/spec:tol", PublishOptions{}); err != nil {
		t.Fatalf("draft tolerance must publish with a missing target, got %v", err)
	}

	// Missing target + non-draft content-state: blocked (R5 error).
	d := adrDraft("004", []exchange.Relationship{{Type: "supersedes", Target: "feather/adr:ghost"}})
	// Promote the draft's content-state to accepted (frontmatter +
	// change-log must agree, rule 7).
	data, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), `"contentState": "proposed"`, `"contentState": "accepted"`, 1)
	text = strings.Replace(text, `"to": "proposed"`, `"to": "accepted"`, 1)
	if err := os.WriteFile(d.Path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Authoring.Publish(r, "feather/adr:004", PublishOptions{})
	var pe *PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("a non-draft superseding adr with a missing target = %v, want *PublishError", err)
	}
	found := false
	for _, res := range pe.Report.Results {
		if res.Rule == "R5" && res.Severity == conformance.SeverityError {
			found = true
		}
	}
	if !found {
		t.Errorf("R5 unresolved finding missing: %+v", pe.Report.SortedResults())
	}
}

// TestPublishSelfReference: a draft whose depends-on points at itself
// is refused with the R5 self-reference error.
func TestPublishSelfReference(t *testing.T) {
	r, project := draftRuntime(t)
	newSTODraft(t, r, project, "feather", "self", []exchange.Relationship{
		{Type: "depends-on", Target: "feather/sto:self"},
	})
	_, err := Authoring.Publish(r, "feather/sto:self", PublishOptions{})
	var pe *PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("a self-referencing draft = %v, want *PublishError", err)
	}
	found := false
	for _, res := range pe.Report.Results {
		if res.Rule == "R5" && strings.Contains(res.Message, "self-reference") {
			found = true
		}
	}
	if !found {
		t.Errorf("R5 self-reference finding missing: %+v", pe.Report.SortedResults())
	}
}

// TestPublishMissingSections: a content-file draft without the type's
// required sections fails publish with the R9 report and the draft is
// kept.
func TestPublishMissingSections(t *testing.T) {
	r, project := draftRuntime(t)
	dir := filepath.Join(r.Path(), "drafts", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sto-nosections.json")
	doc := `{
  "namespace": "feather",
  "type": "sto",
  "id": "nosections",
  "revision": 1,
  "state": {
    "executionState": "planned",
    "existenceState": "active"
  },
  "changeLog": [
    {"date": "2026-08-07", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering"},
    {"date": "2026-08-07", "domain": "executionState", "from": "-", "to": "planned", "by": "Engineering"}
  ],
  "content": {
    "foo": "bar"
  }
}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Authoring.Publish(r, "feather/sto:nosections", PublishOptions{})
	var pe *PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("a section-less draft = %v, want *PublishError", err)
	}
	found := false
	for _, res := range pe.Report.Results {
		if res.Rule == "R9" && strings.Contains(res.Message, "missing required content section") {
			found = true
		}
	}
	if !found {
		t.Errorf("R9 section finding missing: %+v", pe.Report.SortedResults())
	}
	if _, err := os.Stat(draftPathAt(r, project, "sto-nosections.json")); err != nil {
		t.Errorf("the section-less draft must be kept: %v", err)
	}
}

// --- M1: publish/discard project hint --------------------------------

// TestPublishProjectHint (M1): PublishOptions.Project addresses a draft
// under drafts/<project>/ from anywhere; without the hint the cwd
// repository's project applies first, and the cross-project fallback
// resolves the draft from the project that actually holds it (with a
// note naming it).
func TestPublishProjectHint(t *testing.T) {
	r, _ := draftRuntime(t)
	// The draft lives in project "foo" — NOT the cwd repo's project.
	newSTODraft(t, r, "foo", "feather", "remote", nil)
	// Without the hint the cwd project misses, the fallback resolves
	// the draft from project foo and the note says so.
	res, err := Authoring.Publish(r, "feather/sto:remote", PublishOptions{})
	if err != nil {
		t.Fatalf("publish without the hint (cross-project fallback): %v", err)
	}
	if res.Form != "feather/sto:remote:1" {
		t.Errorf("Form = %q", res.Form)
	}
	if !strings.Contains(res.Note, "resolved from project foo") {
		t.Errorf("Note must name the fallback project, got %q", res.Note)
	}
	// With the hint the same identity publishes (new instance: the
	// fallback run already consumed the draft).
	newSTODraft(t, r, "foo", "feather", "remote", nil)
	res, err = Authoring.Publish(r, "feather/sto:remote", PublishOptions{Project: "foo"})
	if err != nil {
		t.Fatalf("publish with --project hint: %v", err)
	}
	if res.Form != "feather/sto:remote:2" {
		t.Errorf("Form = %q, want :2 (forward-only auto-assign)", res.Form)
	}
}

// TestDiscardDraftProjectHint (M1): DiscardDraft accepts the project
// hint too.
func TestDiscardDraftProjectHint(t *testing.T) {
	r, _ := draftRuntime(t)
	d := newSTODraft(t, r, "foo", "feather", "remote", nil)
	if _, err := Authoring.DiscardDraft(r, "feather/sto:remote", "foo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.Path); !os.IsNotExist(err) {
		t.Errorf("the draft file must be gone: %v", err)
	}
	// Without the hint the cwd project applies: not found.
	if _, err := Authoring.DiscardDraft(r, "feather/sto:remote", "", true); err == nil {
		t.Error("discard without the hint must not find the draft in the cwd project")
	}
}

// --- m5b: publish target namespace mismatch ---------------------------

// TestPublishTargetNamespaceMismatch (m5b): a target namespace that
// differs from the draft frontmatter's namespace is a deterministic
// error.
func TestPublishTargetNamespaceMismatch(t *testing.T) {
	r, project := draftRuntime(t)
	newSTODraft(t, r, project, "feather", "nsx", nil)
	_, err := Authoring.Publish(r, "other/sto:nsx", PublishOptions{})
	if err == nil || !strings.Contains(err.Error(), "target namespace other does not match draft namespace feather") {
		t.Errorf("namespace mismatch = %v, want the deterministic mismatch error", err)
	}
	// The matching namespace publishes; the draft is untouched by the
	// refused attempt.
	if _, err := Authoring.Publish(r, "feather/sto:nsx", PublishOptions{}); err != nil {
		t.Errorf("the matching namespace must publish, got %v", err)
	}
}

// TestPublishIdentityMismatch: the draft's identity is its frontmatter,
// not its file name — a target that resolves to a file carrying a
// DIFFERENT identity is refused deterministically (the file name is
// addressing, the frontmatter is the source of truth), never a silent
// publish of the frontmatter identity under the target's address.
func TestPublishIdentityMismatch(t *testing.T) {
	r, project := draftRuntime(t)
	d := newSTODraft(t, r, project, "feather", "setup-ci-web", nil)
	// Tamper the frontmatter identity: the file name says setup-ci-web,
	// the frontmatter says setup-ci-pipelines.
	data, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.ReplaceAll(data, []byte(`"id": "setup-ci-web"`), []byte(`"id": "setup-ci-pipelines"`))
	if err := os.WriteFile(d.Path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	// The file-name identity is refused with the deterministic error.
	_, err = Authoring.Publish(r, "feather/sto:setup-ci-web", PublishOptions{})
	if err == nil || !strings.Contains(err.Error(), "carries identity sto:setup-ci-pipelines; expected sto:setup-ci-web") ||
		!strings.Contains(err.Error(), "rename the file or publish the draft's own identity") {
		t.Errorf("identity mismatch = %v, want the deterministic mismatch error", err)
	}
	// The refused publish leaves the draft untouched.
	if _, err := os.Stat(d.Path); err != nil {
		t.Errorf("the refused publish must keep the draft: %v", err)
	}
	// The frontmatter identity is NOT addressable: the file name is the
	// addressing identity, so that target cannot resolve a draft.
	_, err = Authoring.Publish(r, "feather/sto:setup-ci-pipelines", PublishOptions{})
	var dne *DraftNotFoundError
	if !errors.As(err, &dne) {
		t.Errorf("the frontmatter identity target = %v, want *DraftNotFoundError", err)
	}

	// Restore the file's identity: the matching publish succeeds.
	if err := os.WriteFile(d.Path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Authoring.Publish(r, "feather/sto:setup-ci-web", PublishOptions{})
	if err != nil {
		t.Fatalf("the matching identity must publish, got %v", err)
	}
	if res.Form != "feather/sto:setup-ci-web:1" {
		t.Errorf("Form = %q, want feather/sto:setup-ci-web:1", res.Form)
	}
}

// TestValidateDraftIdentityMismatch: ValidateDraft applies the same
// identity check as Publish — a target whose file carries a different
// frontmatter identity is refused, never validated under the target's
// address.
func TestValidateDraftIdentityMismatch(t *testing.T) {
	r, project := draftRuntime(t)
	d := newSTODraft(t, r, project, "feather", "setup-ci-web", nil)
	data, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.ReplaceAll(data, []byte(`"id": "setup-ci-web"`), []byte(`"id": "setup-ci-pipelines"`))
	if err := os.WriteFile(d.Path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Authoring.ValidateDraft(r, "feather/sto:setup-ci-web", "")
	if err == nil || !strings.Contains(err.Error(), "carries identity sto:setup-ci-pipelines; expected sto:setup-ci-web") {
		t.Errorf("identity mismatch = %v, want the deterministic mismatch error", err)
	}
	// The matching identity validates normally.
	if err := os.WriteFile(d.Path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	dv, err := Authoring.ValidateDraft(r, "feather/sto:setup-ci-web", "")
	if err != nil {
		t.Fatalf("the matching identity must validate, got %v", err)
	}
	if dv.Report == nil || !dv.Report.Pass() {
		t.Errorf("the matching identity must validate clean, got %+v", dv)
	}
}

// TestStoreResolverReportsFailures: a store failure during relationship
// resolution is recorded and surfaced by Findings instead of a silent
// "unresolved" — a warning under content-state draft (Rule 5's draft
// tolerance), an error otherwise; a plain absence records nothing, and
// repeated lookups of the same line deduplicate.
func TestStoreResolverReportsFailures(t *testing.T) {
	r := testRuntime(t)

	// A healthy store: an absent line resolves as unresolved WITHOUT a
	// finding (plain absence is not a failure).
	resolver := newStoreResolver(r.st)
	absent := conformance.Reference{Namespace: "feather", Type: "ctr", ID: "ghost"}
	if resolver.Resolve(absent) {
		t.Error("an absent line must resolve as unresolved")
	}
	if got := resolver.Findings("feather/ctr:ghost.json", "accepted"); len(got) != 0 {
		t.Errorf("a healthy lookup must record no findings, got %+v", got)
	}

	// Break the store: every lookup now fails (a non-busy error), which
	// must be recorded, not swallowed.
	if err := r.st.Close(); err != nil {
		t.Fatal(err)
	}
	if resolver.Resolve(absent) {
		t.Error("a broken store must resolve as unresolved")
	}
	// Draft tolerance: a warning while content-state is draft.
	got := resolver.Findings("feather/ctr:ghost.json", "draft")
	if len(got) != 1 {
		t.Fatalf("Findings(draft) = %d results, want 1", len(got))
	}
	if got[0].Severity != conformance.SeverityWarning {
		t.Errorf("draft tolerance: severity = %s, want warning", got[0].Severity)
	}
	if !strings.Contains(got[0].Message, "could not be checked against the store") {
		t.Errorf("the finding must name the store failure, got %q", got[0].Message)
	}
	// Non-draft: the same failure is an error.
	got = resolver.Findings("feather/ctr:ghost.json", "accepted")
	if len(got) != 1 || got[0].Severity != conformance.SeverityError {
		t.Errorf("non-draft findings = %+v, want 1 error", got)
	}
	// Deduplication: a repeated lookup of the same line stays one finding.
	resolver.Resolve(absent)
	if got := resolver.Findings("feather/ctr:ghost.json", "accepted"); len(got) != 1 {
		t.Errorf("repeated lookups must deduplicate, got %d findings", len(got))
	}
	// A second failing line appends deterministically (sorted by line).
	resolver.Resolve(conformance.Reference{Namespace: "feather", Type: "sto", ID: "ghost"})
	got = resolver.Findings("feather/ctr:ghost.json", "accepted")
	if len(got) != 2 {
		t.Fatalf("two failing lines = %d findings, want 2", len(got))
	}
	if !strings.HasPrefix(got[0].Message, "reference feather/ctr:ghost") ||
		!strings.HasPrefix(got[1].Message, "reference feather/sto:ghost") {
		t.Errorf("findings must be sorted by referenced line, got %+v", got)
	}
}

// --- m4b: draft frontmatter instance-version --------------------------

// TestPublishFrontmatterInstanceVersion (m4b): an explicit
// instance-version in the draft frontmatter is honored when it exceeds
// the line's highest; a forward-only violation is blocked.
func TestPublishFrontmatterInstanceVersion(t *testing.T) {
	r, project := draftRuntime(t)
	writeDraft := func(id string, withVersion bool, version int) string {
		t.Helper()
		dir := filepath.Join(r.Path(), "drafts", project)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		fm := "---\nnamespace: feather\ntype: sto\nid: " + id + "\n"
		if withVersion {
			fm += "instance-version: " + strconv.Itoa(version) + "\n"
		}
		fm += "revision: 1\nexecution-state: planned\nexistence-state: active\n"
		fm += "change-log:\n  - date: 2026-08-07\n    domain: execution-state\n    from: \"-\"\n    to: planned\n    by: Engineering\n  - date: 2026-08-07\n    domain: existence-state\n    from: \"-\"\n    to: active\n    by: Engineering\n"
		fm += "---\n" + stoContent
		path := filepath.Join(dir, "sto-"+id+".md")
		if err := os.WriteFile(path, []byte(fm), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// Frontmatter version 5: honored.
	writeDraft("fv", true, 5)
	res, err := Authoring.Publish(r, "feather/sto:fv", PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Form != "feather/sto:fv:5" || res.InstanceVersion != 5 {
		t.Errorf("frontmatter version result = %+v, want :5", res)
	}

	// A second draft of the SAME line pinned at 5 (the line's highest,
	// the first publish removed the draft file): forward-only
	// violation, blocked, draft kept.
	writeDraft("fv", true, 5)
	_, err = Authoring.Publish(r, "feather/sto:fv", PublishOptions{})
	if err == nil || !strings.Contains(err.Error(), "must exceed the line's highest") {
		t.Errorf("a frontmatter version at the line's highest = %v, want the forward-only refusal", err)
	}
	if _, err := os.Stat(draftPathAt(r, project, "sto-fv.md")); err != nil {
		t.Errorf("the blocked draft must be kept: %v", err)
	}

	// No frontmatter version: auto-assign max(line)+1 = 6.
	writeDraft("fv", false, 0)
	res, err = Authoring.Publish(r, "feather/sto:fv", PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Form != "feather/sto:fv:6" {
		t.Errorf("auto-assigned form = %q, want :6 (max 5 + 1)", res.Form)
	}

	// The explicit --instance-version override wins over the frontmatter.
	writeDraft("fv", true, 1)
	res, err = Authoring.Publish(r, "feather/sto:fv", PublishOptions{InstanceVersion: 9})
	if err != nil {
		t.Fatal(err)
	}
	if res.Form != "feather/sto:fv:9" {
		t.Errorf("override form = %q, want :9 (the flag wins)", res.Form)
	}
}

// --- m1: remove-draft race handling -----------------------------------

// TestRemoveDraftAfterPublish (m1): the loser of a concurrent publish
// finds the draft already gone — reported, never an error.
func TestRemoveDraftAfterPublish(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sto-x.md")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Normal removal: the file is removed.
	alreadyGone, err := removeDraftAfterPublish(path)
	if err != nil || alreadyGone {
		t.Errorf("normal removal = (%v, %v), want (false, nil)", alreadyGone, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file must be gone: %v", err)
	}
	// Already gone (a concurrent publish removed it): reported as
	// alreadyGone, nil error.
	alreadyGone, err = removeDraftAfterPublish(path)
	if err != nil || !alreadyGone {
		t.Errorf("missing-file removal = (%v, %v), want (true, nil)", alreadyGone, err)
	}
}

// --- P6 guard ---------------------------------------------------------

// TestPublishP6Guard: publishing never touches the referenced objects'
// state — a ticket publish leaves the container's container-state and
// the work items' execution-state untouched. Publishing a container
// (ctr-) follows the same invariant (Option B): it is born planned and
// the depends-on plan is untouched — the plan lock (planning-state ->
// immutable, protocol §4) happens at container ACTIVATION, never at
// publish.
func TestPublishP6Guard(t *testing.T) {
	r, project := draftRuntime(t)
	// Seed the referenced world: a container and its work item, both
	// with non-initial states.
	ctr := unit("feather", "ctr", "wave-7", 1, 1)
	ctr.StateVector = exchange.StateVector{ContainerState: "active", ExistenceState: "active"}
	putUnit(t, r, ctr, "proj-x", "repo-x")
	sto := unit("feather", "sto", "login", 1, 1)
	sto.StateVector = exchange.StateVector{ExecutionState: "in-progress", ExistenceState: "active"}
	putUnit(t, r, sto, "proj-x", "repo-x")

	// A ticket draft deriving from the container (the JSON template
	// carries the projection header in the commands content value).
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:       project,
		Namespace:     "feather",
		Type:          "tkt",
		ID:            "t1",
		Relationships: []exchange.Relationship{{Type: "derives-from", Target: "feather/ctr:wave-7"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Authoring.Publish(r, "feather/tkt:t1", PublishOptions{}); err != nil {
		t.Fatal(err)
	}

	// P6: publish wrote ONLY the ticket — the referenced objects are
	// untouched.
	ctrAfter, ok, err := r.Knowledge.Object("feather/ctr:wave-7:1")
	if err != nil || !ok {
		t.Fatalf("container read-back = %v, %v", ok, err)
	}
	if ctrAfter.StateVector.ContainerState != "active" {
		t.Errorf("P6 violated: container-state changed to %q", ctrAfter.StateVector.ContainerState)
	}
	stoAfter, ok, err := r.Knowledge.Object("feather/sto:login:1")
	if err != nil || !ok {
		t.Fatalf("work item read-back = %v, %v", ok, err)
	}
	if stoAfter.StateVector.ExecutionState != "in-progress" {
		t.Errorf("P6 violated: execution-state changed to %q", stoAfter.StateVector.ExecutionState)
	}
}

// --- M3: ValidateDraft ------------------------------------------------

// TestValidateDraft (M3): the shared CKO-level draft re-validation —
// pass, rule findings, structural findings, not-found.
func TestValidateDraft(t *testing.T) {
	r, project := draftRuntime(t)
	// A publishable draft validates clean.
	newSTODraft(t, r, project, "feather", "ok", nil)
	dv, err := Authoring.ValidateDraft(r, "feather/sto:ok", "")
	if err != nil {
		t.Fatalf("ValidateDraft: %v", err)
	}
	if !dv.Report.Pass() {
		t.Errorf("a publishable draft must validate clean, got %d errors: %+v",
			dv.Report.ErrorCount(), dv.Report.SortedResults())
	}

	// A section-less draft fails with rule findings (non-destructive):
	// a hand-written JSON draft whose content lacks the required keys.
	dir := filepath.Join(r.Path(), "drafts", project)
	badPath := filepath.Join(dir, "sto-bad.json")
	badDoc := `{
  "namespace": "feather",
  "type": "sto",
  "id": "bad",
  "revision": 1,
  "state": {
    "executionState": "planned",
    "existenceState": "active"
  },
  "changeLog": [
    {"date": "2026-08-07", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering"},
    {"date": "2026-08-07", "domain": "executionState", "from": "-", "to": "planned", "by": "Engineering"}
  ],
  "content": {
    "foo": "bar"
  }
}`
	if err := os.WriteFile(badPath, []byte(badDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	dv, err = Authoring.ValidateDraft(r, "feather/sto:bad", "")
	if err != nil {
		t.Fatalf("ValidateDraft: %v", err)
	}
	if dv.Report.Pass() {
		t.Error("a section-less draft must fail CKO-level validation")
	}
	found := false
	for _, res := range dv.Report.Results {
		if res.Rule == "R9" {
			found = true
		}
	}
	if !found {
		t.Errorf("R9 finding missing: %+v", dv.Report.SortedResults())
	}
	// The draft file is untouched by validation.
	if _, err := os.Stat(badPath); err != nil {
		t.Errorf("validation must not delete the draft: %v", err)
	}

	// Structural findings: broken JSON -> ScanError.
	if err := os.WriteFile(filepath.Join(dir, "sto-broken.json"), []byte(`{"namespace": "x",`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Authoring.ValidateDraft(r, "feather/sto:broken", "")
	var se *conformance.ScanError
	if !errors.As(err, &se) {
		t.Fatalf("ValidateDraft of a malformed draft = %v, want *conformance.ScanError", err)
	}

	// Missing draft: DraftNotFoundError.
	_, err = Authoring.ValidateDraft(r, "feather/sto:ghost", "")
	var dne *DraftNotFoundError
	if !errors.As(err, &dne) {
		t.Fatalf("ValidateDraft of a missing draft = %v, want *DraftNotFoundError", err)
	}

	// The project hint addresses a draft in another project.
	newSTODraft(t, r, "foo", "feather", "remote", nil)
	dv, err = Authoring.ValidateDraft(r, "feather/sto:remote", "foo")
	if err != nil {
		t.Fatal(err)
	}
	if !dv.Report.Pass() {
		t.Errorf("the hinted draft must validate clean: %+v", dv.Report.SortedResults())
	}
}

// TestValidateDraftDraftToDraftTargetTolerance (fix-draft-reference
// -tolerance): a draft referencing another draft (both unpublished,
// same project) validates with NO R5 finding — the target-side draft
// tolerance. sto- drafts own no content-state, so the source-side
// content-state tolerance would not apply: this is the reported
// regression (previously a hard R5 error). The tolerance is
// target-specific (a reference to a truly missing line still flags
// R5), and the publish gate shares it (draft A publishes while draft B
// is still unpublished).
func TestValidateDraftDraftToDraftTargetTolerance(t *testing.T) {
	r, project := draftRuntime(t)
	// Draft B exists only as a draft; draft A references it.
	newSTODraft(t, r, project, "feather", "b", nil)
	newSTODraft(t, r, project, "feather", "a",
		[]exchange.Relationship{{Type: "depends-on", Target: "feather/sto:b"}})

	dv, err := Authoring.ValidateDraft(r, "feather/sto:a", "")
	if err != nil {
		t.Fatalf("ValidateDraft: %v", err)
	}
	if !dv.Report.Pass() {
		t.Errorf("a draft referencing another draft must validate, got %d errors: %+v",
			dv.Report.ErrorCount(), dv.Report.SortedResults())
	}
	for _, res := range dv.Report.Results {
		if res.Rule == "R5" {
			t.Errorf("R5 finding on a draft-to-draft reference: %+v", res)
		}
	}

	// The tolerance is target-specific: a reference to a truly
	// missing line still flags R5.
	newSTODraft(t, r, project, "feather", "c",
		[]exchange.Relationship{{Type: "depends-on", Target: "feather/sto:missing"}})
	dv, err = Authoring.ValidateDraft(r, "feather/sto:c", "")
	if err != nil {
		t.Fatalf("ValidateDraft: %v", err)
	}
	found := false
	for _, res := range dv.Report.Results {
		if res.Rule == "R5" {
			found = true
		}
	}
	if !found {
		t.Errorf("a reference to a truly missing line must still flag R5: %+v", dv.Report.SortedResults())
	}

	// The publish gate shares the pipeline: A publishes while B is
	// still a draft.
	if _, err := Authoring.Publish(r, "feather/sto:a", PublishOptions{}); err != nil {
		t.Errorf("publish of a draft referencing a draft must succeed: %v", err)
	}
}

// --- Container lifecycle (protocol §4, Option B) -----------------------

// TestNewDraftContainerRequiresPlan: a ctr- draft without a depends-on
// plan reference is refused at scaffold time (the ticket-guard mirror);
// a plan reference outside depends-on does not satisfy the guard; with
// the depends-on plan reference the scaffold succeeds and the draft
// carries the relationship.
func TestNewDraftContainerRequiresPlan(t *testing.T) {
	r, project := draftRuntime(t)
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "ctr", ID: "wave-7",
	}); err == nil || !strings.Contains(err.Error(), "container drafts require --depends-on with a plan- reference") {
		t.Errorf("ctr without a plan reference = %v, want the scaffold refusal", err)
	}
	// A plan reference in another relationship field does not satisfy
	// the guard (depends-on only).
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "ctr", ID: "wave-7",
		Relationships: []exchange.Relationship{{Type: "derives-from", Target: "plan:roadmap-v1"}},
	}); err == nil {
		t.Error("a plan reference outside depends-on must not satisfy the guard")
	}
	// A malformed depends-on target is not a plan reference.
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "ctr", ID: "wave-7",
		Relationships: []exchange.Relationship{{Type: "depends-on", Target: "not a target"}},
	}); err == nil {
		t.Error("a malformed depends-on target must not satisfy the guard")
	}
	// With the depends-on plan reference the scaffold succeeds.
	d, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:       project,
		Namespace:     "feather",
		Type:          "ctr",
		ID:            "wave-7",
		Relationships: []exchange.Relationship{{Type: "depends-on", Target: "plan:roadmap-v1"}},
	})
	if err != nil {
		t.Fatalf("ctr with a plan reference must scaffold, got %v", err)
	}
	data, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"plan:roadmap-v1"`) {
		t.Errorf("the draft must carry the depends-on plan reference:\n%s", data)
	}
}

// TestPublishCtrBornPlanned: publishing a container persists ONLY the
// container line — born planned (the template's initial
// container-state, change-log "-" -> planned), no lock — and the
// depends-on plan stays approved (P6 restored for publish: the lock
// happens at activation, Option B).
func TestPublishCtrBornPlanned(t *testing.T) {
	r, project := draftRuntime(t)
	seedApprovedPlan(t, r, project, "feather", "roadmap-v1")
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:       project,
		Namespace:     "feather",
		Type:          "ctr",
		ID:            "wave-7",
		Relationships: []exchange.Relationship{{Type: "depends-on", Target: "feather/plan:roadmap-v1"}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := Authoring.Publish(r, "feather/ctr:wave-7", PublishOptions{})
	if err != nil {
		t.Fatalf("container publish: %v", err)
	}
	if res.Form != "feather/ctr:wave-7:1" {
		t.Errorf("container form = %q", res.Form)
	}
	// The container is born planned with the "-" -> planned birth entry.
	ctr, ok, err := r.Knowledge.Object("feather/ctr:wave-7:1")
	if err != nil || !ok {
		t.Fatalf("container read-back = %v, %v", ok, err)
	}
	if ctr.StateVector.ContainerState != "planned" {
		t.Errorf("container-state = %q, want planned (born planned)", ctr.StateVector.ContainerState)
	}
	var bornPlanned bool
	for _, e := range ctr.ChangeLog {
		if e.Domain == conformance.DomainContainerState && e.From == "-" && e.To == "planned" {
			bornPlanned = true
		}
	}
	if !bornPlanned {
		t.Errorf("the change-log must carry the \"-\" -> planned birth entry: %+v", ctr.ChangeLog)
	}
	// No lock: the plan stays approved (publish no longer locks; the
	// lock belongs to activation).
	plan, ok, err := r.Knowledge.Object("feather/plan:roadmap-v1:1")
	if err != nil || !ok {
		t.Fatalf("plan read-back = %v, %v", ok, err)
	}
	if plan.StateVector.PlanningState != "approved" {
		t.Errorf("planning-state = %q, want approved (publish no longer locks)", plan.StateVector.PlanningState)
	}
}

// TestPublishCtrAgainstImmutablePlan: publishing containers against an
// already-immutable plan changes nothing — the container is born
// planned and the plan keeps exactly ONE immutable transition (publish
// performs no lock; the lock belongs to activation).
func TestPublishCtrAgainstImmutablePlan(t *testing.T) {
	r, project := draftRuntime(t)
	seedApprovedPlan(t, r, project, "feather", "roadmap-v1")
	// Lock the plan with the first container's ACTIVATION (the lock
	// moved from publish to activation: Option B).
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:       project,
		Namespace:     "feather",
		Type:          "ctr",
		ID:            "wave-7",
		Relationships: []exchange.Relationship{{Type: "depends-on", Target: "feather/plan:roadmap-v1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Authoring.Publish(r, "feather/ctr:wave-7", PublishOptions{}); err != nil {
		t.Fatalf("container publish: %v", err)
	}
	if _, err := Authoring.Transition(r, TransitionRequest{
		RepoPath: ".", Target: "ctr:wave-7", To: "active", By: "test-agent",
	}); err != nil {
		t.Fatalf("container activation: %v", err)
	}
	// A second container deriving from the same (now immutable) plan:
	// publish persists it planned and performs no lock.
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:       project,
		Namespace:     "feather",
		Type:          "ctr",
		ID:            "wave-8",
		Relationships: []exchange.Relationship{{Type: "depends-on", Target: "feather/plan:roadmap-v1"}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := Authoring.Publish(r, "feather/ctr:wave-8", PublishOptions{})
	if err != nil {
		t.Fatalf("second container publish: %v", err)
	}
	if res.Form != "feather/ctr:wave-8:1" {
		t.Errorf("second container form = %q", res.Form)
	}
	second, ok, err := r.Knowledge.Object("feather/ctr:wave-8:1")
	if err != nil || !ok {
		t.Fatalf("second container read-back = %v, %v", ok, err)
	}
	if second.StateVector.ContainerState != "planned" {
		t.Errorf("second container-state = %q, want planned", second.StateVector.ContainerState)
	}
	// The plan stays immutable with exactly ONE immutable transition.
	plan, ok, err := r.Knowledge.Object("feather/plan:roadmap-v1:1")
	if err != nil || !ok {
		t.Fatalf("plan read-back = %v, %v", ok, err)
	}
	if plan.StateVector.PlanningState != "immutable" {
		t.Errorf("planning-state = %q, want immutable", plan.StateVector.PlanningState)
	}
	var locks int
	for _, e := range plan.ChangeLog {
		if e.Domain == conformance.DomainPlanningState && e.To == "immutable" {
			locks++
		}
	}
	if locks != 1 {
		t.Errorf("immutable change-log entries = %d, want 1 (the lock is idempotent)", locks)
	}
}

// TestLockPlanPutsRefusals: the deterministic activation preconditions
// at the builder level — no depends-on plan reference, more than one
// distinct plan reference, and a plan missing from the store are all
// *TransitionRefusal (the activation gates, Option B).
func TestLockPlanPutsRefusals(t *testing.T) {
	r, project := draftRuntime(t)
	st := r.ws.Store()
	by := conformance.User("Engineering")

	// None: the unit declares no plan reference at all.
	u := plannedCtr("w1", "ghost")
	u.Relationships = []exchange.Relationship{}
	if _, _, err := lockPlanPuts(st, project, draftSourceRepo, u, by); !isRefusalContaining(err, "declares no depends-on plan reference") {
		t.Errorf("no plan reference = %v, want the declares-no-plan refusal", err)
	}
	// More than one distinct plan reference.
	u.Relationships = []exchange.Relationship{
		{Type: "depends-on", Target: "plan:roadmap-v1"},
		{Type: "depends-on", Target: "plan:roadmap-v2"},
	}
	if _, _, err := lockPlanPuts(st, project, draftSourceRepo, u, by); !isRefusalContaining(err, "the container must lock exactly one plan (depends-on)") {
		t.Errorf("multiple plan references = %v, want the exactly-one refusal", err)
	}
	// A plan reference missing from the store.
	u.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "plan:ghost"}}
	if _, _, err := lockPlanPuts(st, project, draftSourceRepo, u, by); !isRefusalContaining(err, "run 'eka sync' first") {
		t.Errorf("missing plan = %v, want the sync-hint refusal", err)
	}
}

// isRefusalContaining reports whether err is a *TransitionRefusal whose
// rendered message contains want.
func isRefusalContaining(err error, want string) bool {
	var tr *TransitionRefusal
	if !errors.As(err, &tr) {
		return false
	}
	return strings.Contains(tr.Error(), want)
}
