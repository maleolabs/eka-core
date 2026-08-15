package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
)

// This file tests the Relate API (runtime/relate.go): the
// relationship-only edge-add of the Authoring API. The acceptance under
// test is the instance-churn guarantee — adding an edge to a published
// artifact must NOT advance the line (instance version and revision
// unchanged) — plus the refusal semantics (unknown type, self-reference,
// malformed reference, missing artifact), the idempotent duplicate
// handling, the Rule 5 draft tolerance of missing targets, and the
// pending-draft path.

// specUnit builds a CKO-valid spec- unit (the shape the publish
// pipeline produces: content-state draft, the owned existence-state,
// the change-log covering both owned domains, classification
// specifications/Architecture). spec- owns content-state, so the draft
// tolerance of Rule 5 is exercised exactly like on a freshly published
// artifact.
func specUnit(ns, id string, version int) *exchange.Unit {
	u := &exchange.Unit{
		Identity: exchange.Identity{Namespace: ns, Type: "spec", ID: id, InstanceVersion: version},
		Revision: 1,
		StateVector: exchange.StateVector{
			ContentState:   "draft",
			ExistenceState: "active",
		},
		ChangeLog: []exchange.ChangeLogEntry{
			{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
			{Date: "2026-08-07", Domain: "content-state", From: "-", To: "draft", By: conformance.User("Engineering")},
		},
		Relationships: []exchange.Relationship{},
		Classification: exchange.Classification{
			Dimension: "specifications",
			Domain:    "Architecture",
		},
		Content:        exchange.ContentRef{Representation: "eka/structured-text/1", File: "content"},
		ContentPayload: []byte("# " + id + "\n\n## Purpose\n\np\n\n## Content\n\nc\n"),
	}
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()
	return u
}

// relateWorld seeds a project/repo pair with two published spec- units
// (acme/spec:a:1 and acme/spec:b:1) under proj-a/repo-a and returns
// the runtime. The working directory is moved to a repo-free temp dir:
// the repository-context ownership gate resolves the walk-up from the
// cwd, so tests of the published path must not accidentally resolve the
// worktree's own eka.yaml (the test cwd would otherwise carry a repo
// context with an unrelated namespace).
func relateWorld(t *testing.T) *Runtime {
	t.Helper()
	t.Chdir(t.TempDir())
	r := testRuntime(t)
	registerWorld(t, r, "proj-a", "repo-a")
	putUnit(t, r, specUnit("acme", "a", 1), "proj-a", "repo-a")
	putUnit(t, r, specUnit("acme", "b", 1), "proj-a", "repo-a")
	return r
}

// relEdges builds the edges of one relate request.
func relEdges(pairs ...string) []exchange.Relationship {
	var out []exchange.Relationship
	for _, p := range pairs {
		parts := strings.SplitN(p, "=", 2)
		out = append(out, exchange.Relationship{Type: parts[0], Target: parts[1]})
	}
	return out
}

// --- acceptance: no instance churn -------------------------------------

// TestRelatePublishedNoInstanceChurn is the acceptance test: adding an
// edge to a published artifact re-points the reference to a new
// immutable payload WITHOUT advancing the line — instance version and
// revision unchanged, provenance preserved, the payload archive gains
// exactly one row, and the store verifies clean.
func TestRelatePublishedNoInstanceChurn(t *testing.T) {
	r := relateWorld(t)
	payloadsBefore, err := r.ws.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}

	res, err := Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("depends-on=acme/spec:b"),
	})
	if err != nil {
		t.Fatalf("Relate: %v", err)
	}

	if res.State != "published" {
		t.Errorf("State = %q, want published", res.State)
	}
	if res.Target != "acme/spec:a" {
		t.Errorf("Target = %q, want acme/spec:a", res.Target)
	}
	// The acceptance: the instance version must NOT move.
	if res.InstanceVersion != 1 {
		t.Errorf("InstanceVersion = %d, want 1 (no instance churn)", res.InstanceVersion)
	}
	if len(res.Added) != 1 || res.Added[0] != (exchange.Relationship{Type: "depends-on", Target: "acme/spec:b"}) {
		t.Errorf("Added = %+v, want the depends-on edge", res.Added)
	}

	// The line still resolves to instance 1, revision 1 — no churn.
	u, ok, err := r.Knowledge.Object("acme/spec:a:1")
	if err != nil || !ok {
		t.Fatalf("Object = %v, %v", ok, err)
	}
	if u.Identity.InstanceVersion != 1 || u.Revision != 1 {
		t.Errorf("after relate = v%d r%d, want v1 r1 (instance and revision unchanged)",
			u.Identity.InstanceVersion, u.Revision)
	}
	if len(u.Relationships) != 1 || u.Relationships[0] != (exchange.Relationship{Type: "depends-on", Target: "acme/spec:b"}) {
		t.Errorf("Relationships = %+v, want the depends-on edge", u.Relationships)
	}
	if u.Digest != res.ObjectHash {
		t.Errorf("the reference must point at the new payload %s, got %s", res.ObjectHash, u.Digest)
	}

	// Provenance preserved: project and source repo unchanged.
	ref, ok, err := r.ws.Store().Ref("acme/spec:a:1")
	if err != nil || !ok {
		t.Fatalf("Ref = %v, %v", ok, err)
	}
	if ref.ProjectID != "proj-a" || ref.SourceRepo != "repo-a" {
		t.Errorf("provenance = %s/%s, want proj-a/repo-a", ref.ProjectID, ref.SourceRepo)
	}
	if ref.InstanceVersion != 1 || ref.Revision != 1 {
		t.Errorf("ref index = v%d r%d, want v1 r1 (no churn in the reference index)",
			ref.InstanceVersion, ref.Revision)
	}

	// The payload archive gained exactly one row (the new edge payload);
	// the old payload stays in the history (prev_hash lineage).
	payloadsAfter, err := r.ws.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}
	if payloadsAfter != payloadsBefore+1 {
		t.Errorf("payloads = %d -> %d, want exactly +1 (history accumulates)", payloadsBefore, payloadsAfter)
	}

	// The store verifies clean: the re-point did not break integrity.
	report, err := r.Integrity.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Violations) != 0 {
		t.Errorf("store must verify clean after relate, got %+v", report.Violations)
	}

	// The line's history still shows instance 1 once (no new instance).
	line, err := r.Timeline.Line("acme", "spec", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(line) != 1 || line[0].InstanceVersion != 1 {
		t.Errorf("Timeline.Line = %+v, want exactly instance v1", line)
	}
}

// --- idempotent duplicates ---------------------------------------------

// TestRelateDuplicateEdgeIdempotent: a duplicate edge is a no-op — the
// second relate writes NOTHING (no new payload, State = unchanged), and
// the unit still carries exactly one edge.
func TestRelateDuplicateEdgeIdempotent(t *testing.T) {
	r := relateWorld(t)
	if _, err := Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("depends-on=acme/spec:b"),
	}); err != nil {
		t.Fatalf("first relate: %v", err)
	}
	payloadsAfterFirst, err := r.ws.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}

	res, err := Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("depends-on=acme/spec:b", "depends-on=acme/spec:b"),
	})
	if err != nil {
		t.Fatalf("duplicate relate: %v", err)
	}
	if res.State != "unchanged" {
		t.Errorf("State = %q, want unchanged (idempotent)", res.State)
	}
	if res.ObjectHash != "" || len(res.Added) != 0 {
		t.Errorf("an unchanged relate must write nothing, got hash %q added %+v", res.ObjectHash, res.Added)
	}
	if res.InstanceVersion != 1 {
		t.Errorf("InstanceVersion = %d, want 1 (unchanged)", res.InstanceVersion)
	}

	payloadsAfter, err := r.ws.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}
	if payloadsAfter != payloadsAfterFirst {
		t.Errorf("a duplicate relate must not write a payload, got %d -> %d", payloadsAfterFirst, payloadsAfter)
	}

	u, ok, err := r.Knowledge.Object("acme/spec:a:1")
	if err != nil || !ok {
		t.Fatalf("Object = %v, %v", ok, err)
	}
	if len(u.Relationships) != 1 {
		t.Errorf("Relationships = %+v, want exactly one edge", u.Relationships)
	}
}

// TestRelateMixedDuplicatesAddOnlyMissing: when some edges are already
// present, only the missing ones are added.
func TestRelateMixedDuplicatesAddOnlyMissing(t *testing.T) {
	r := relateWorld(t)
	if _, err := Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("depends-on=acme/spec:b"),
	}); err != nil {
		t.Fatalf("first relate: %v", err)
	}
	res, err := Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("depends-on=acme/spec:b", "derives-from=acme/spec:b"),
	})
	if err != nil {
		t.Fatalf("mixed relate: %v", err)
	}
	if res.State != "published" {
		t.Errorf("State = %q, want published", res.State)
	}
	if len(res.Added) != 1 || res.Added[0] != (exchange.Relationship{Type: "derives-from", Target: "acme/spec:b"}) {
		t.Errorf("Added = %+v, want only the derives-from edge", res.Added)
	}
	u, ok, err := r.Knowledge.Object("acme/spec:a:1")
	if err != nil || !ok {
		t.Fatalf("Object = %v, %v", ok, err)
	}
	if len(u.Relationships) != 2 {
		t.Errorf("Relationships = %+v, want both edges in canonical order", u.Relationships)
	}
	// Canonical (type, target) order: depends-on before derives-from.
	if u.Relationships[0].Type != "depends-on" || u.Relationships[1].Type != "derives-from" {
		t.Errorf("Relationships order = %+v, want (depends-on, derives-from)", u.Relationships)
	}
}

// --- refusal semantics -------------------------------------------------

// TestRelateSelfReferenceRefused: a self-reference is refused (the R5
// mirror); nothing is written.
func TestRelateSelfReferenceRefused(t *testing.T) {
	r := relateWorld(t)
	payloadsBefore, err := r.ws.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("depends-on=acme/spec:a"),
	})
	var refusal *RelateRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Relate error = %v, want *RelateRefusal", err)
	}
	if !strings.Contains(refusal.Error(), "self-reference") {
		t.Errorf("refusal = %q, want the self-reference message", refusal.Error())
	}
	payloadsAfter, err := r.ws.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}
	if payloadsAfter != payloadsBefore {
		t.Errorf("a refused relate must not write a payload, got %d -> %d", payloadsBefore, payloadsAfter)
	}
	u, ok, err := r.Knowledge.Object("acme/spec:a:1")
	if err != nil || !ok {
		t.Fatalf("Object = %v, %v", ok, err)
	}
	if len(u.Relationships) != 0 {
		t.Errorf("Relationships = %+v, want none (refused before write)", u.Relationships)
	}
}

// TestRelateUnknownTypeRefused: an unknown relationship type is refused
// (the R5 mirror); nothing is written.
func TestRelateUnknownTypeRefused(t *testing.T) {
	r := relateWorld(t)
	payloadsBefore, err := r.ws.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("bogus=acme/spec:b"),
	})
	var refusal *RelateRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Relate error = %v, want *RelateRefusal", err)
	}
	if !strings.Contains(refusal.Error(), "unknown relationship type") {
		t.Errorf("refusal = %q, want the unknown-type message", refusal.Error())
	}
	payloadsAfter, err := r.ws.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}
	if payloadsAfter != payloadsBefore {
		t.Errorf("a refused relate must not write a payload, got %d -> %d", payloadsBefore, payloadsAfter)
	}
}

// TestRelateMalformedReferenceRefused: a target that does not parse is
// refused; nothing is written.
func TestRelateMalformedReferenceRefused(t *testing.T) {
	r := relateWorld(t)
	_, err := Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("depends-on=acme/spec:"),
	})
	var refusal *RelateRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Relate error = %v, want *RelateRefusal", err)
	}
	if !strings.Contains(refusal.Error(), "malformed reference") {
		t.Errorf("refusal = %q, want the malformed-reference message", refusal.Error())
	}
}

// TestRelateMissingArtifactRefused: a line with no published instance
// and no pending draft is refused.
func TestRelateMissingArtifactRefused(t *testing.T) {
	r := relateWorld(t)
	_, err := Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:ghost",
		Relationships: relEdges("depends-on=acme/spec:b"),
	})
	var refusal *RelateRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Relate error = %v, want *RelateRefusal", err)
	}
	if !strings.Contains(refusal.Error(), "no published instance and no pending draft") {
		t.Errorf("refusal = %q, want the missing-artifact message", refusal.Error())
	}
}

// TestRelateVersionedTargetRefused: a canonical published form (with an
// instance-version suffix) is refused — relate addresses the line.
func TestRelateVersionedTargetRefused(t *testing.T) {
	r := relateWorld(t)
	_, err := Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a:1",
		Relationships: relEdges("depends-on=acme/spec:b"),
	})
	if err == nil || !strings.Contains(err.Error(), "canonical published form") {
		t.Errorf("Relate error = %v, want the published-form refusal", err)
	}
}

// TestRelateNoEdgesUsageError: a relate with no relationship targets at
// all is a usage error — never a silent "unchanged" (the idempotent
// duplicate case, where every REQUESTED edge is already present, has a
// distinct message and result).
func TestRelateNoEdgesUsageError(t *testing.T) {
	r := relateWorld(t)
	_, err := Authoring.Relate(r, RelateRequest{Target: "acme/spec:a"})
	if err == nil || !strings.Contains(err.Error(), "no relationship targets") {
		t.Errorf("Relate error = %v, want the no-relationship-targets usage error", err)
	}
}

// TestRelateVersionedSelfReferenceRefused: on the published path a
// versioned self-reference PINNED TO THE ARTIFACT'S OWN INSTANCE is
// refused (the R5 mirror); a versioned reference to another instance of
// the own line stays a legitimate intra-line reference (e.g. a
// supersedes edge to an older instance), mirroring the CKO rule.
func TestRelateVersionedSelfReferenceRefused(t *testing.T) {
	r := relateWorld(t)
	_, err := Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("depends-on=acme/spec:a:1"),
	})
	var refusal *RelateRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Relate error = %v, want *RelateRefusal", err)
	}
	if !strings.Contains(refusal.Error(), "self-reference") {
		t.Errorf("refusal = %q, want the self-reference message", refusal.Error())
	}
}

// TestRelateVersionedSelfReferenceOnDraftRefused: on the draft path ANY
// reference to the draft's own line — versioned or not — is refused: a
// draft carries no instance-version, so a versioned own-line reference
// cannot name a meaningful different instance and must not land in the
// draft only to be refused at publish.
func TestRelateVersionedSelfReferenceOnDraftRefused(t *testing.T) {
	r, project := relateDraftEnv(t)
	newSTODraft(t, r, project, "feather", "my-item", nil)
	_, err := Authoring.Relate(r, RelateRequest{
		Target:        "feather/sto:my-item",
		Relationships: relEdges("depends-on=feather/sto:my-item:1"),
	})
	var refusal *RelateRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Relate error = %v, want *RelateRefusal", err)
	}
	if !strings.Contains(refusal.Error(), "self-reference") {
		t.Errorf("refusal = %q, want the self-reference message", refusal.Error())
	}
	// The draft file is untouched.
	data, err := os.ReadFile(filepath.Join(r.Path(), "drafts", project, "sto-my-item.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "relationships") {
		t.Errorf("a refused relate must not touch the draft:\n%s", data)
	}
}

// --- missing target resolution (Rule 5 draft tolerance) -----------------

// TestRelateUnresolvedTargetDraftTolerance: an unresolved target on a
// content-state-draft artifact is tolerated (the Rule 5 draft
// tolerance) — the relate succeeds and the edge lands.
func TestRelateUnresolvedTargetDraftTolerance(t *testing.T) {
	r := relateWorld(t)
	res, err := Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("depends-on=acme/spec:ghost"),
	})
	if err != nil {
		t.Fatalf("Relate with a draft-state unresolved target must be tolerated: %v", err)
	}
	if res.State != "published" {
		t.Errorf("State = %q, want published", res.State)
	}
	u, ok, err := r.Knowledge.Object("acme/spec:a:1")
	if err != nil || !ok {
		t.Fatalf("Object = %v, %v", ok, err)
	}
	if len(u.Relationships) != 1 {
		t.Errorf("Relationships = %+v, want the tolerated edge", u.Relationships)
	}
}

// TestRelateUnresolvedTargetNonDraftRefused: an unresolved target on a
// non-draft artifact is a blocking validation error — the relate is
// refused with *RelateValidationError and nothing is written.
func TestRelateUnresolvedTargetNonDraftRefused(t *testing.T) {
	t.Chdir(t.TempDir())
	r := testRuntime(t)
	registerWorld(t, r, "proj-a", "repo-a")
	// A content-state-approved spec- unit (the change-log documents the
	// draft -> approved move).
	a := specUnit("acme", "a", 1)
	a.StateVector.ContentState = "approved"
	a.ChangeLog = append(a.ChangeLog,
		exchange.ChangeLogEntry{Date: "2026-08-08", Domain: "content-state", From: "draft", To: "approved", By: conformance.User("Engineering")})
	putUnit(t, r, a, "proj-a", "repo-a")
	putUnit(t, r, specUnit("acme", "b", 1), "proj-a", "repo-a")

	payloadsBefore, err := r.ws.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Authoring.Relate(r, RelateRequest{
		Target:        "acme/spec:a",
		Relationships: relEdges("depends-on=acme/spec:ghost"),
	})
	var ve *RelateValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Relate error = %v, want *RelateValidationError", err)
	}
	if ve.Report == nil || ve.Report.Pass() {
		t.Errorf("the validation error must carry a failing report, got %+v", ve.Report)
	}
	payloadsAfter, err := r.ws.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}
	if payloadsAfter != payloadsBefore {
		t.Errorf("a refused relate must not write a payload, got %d -> %d", payloadsBefore, payloadsAfter)
	}
}

// --- the draft path -----------------------------------------------------

// relateDraftEnv returns a Runtime plus a registered repository whose
// directory becomes the working directory (the cwd-repo resolution of
// the unqualified-target tests).
func relateDraftEnv(t *testing.T) (*Runtime, string) {
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

// TestRelateDraftPath: a line with no published instance but a pending
// draft gets its edge added to the draft file in place (an edge-add
// before publish); the edge resolves (the target has a published
// instance) and the draft re-validates clean afterwards.
func TestRelateDraftPath(t *testing.T) {
	r, project := relateDraftEnv(t)
	d := newSTODraft(t, r, project, "feather", "my-item", nil)
	// The edge target resolves: a published instance of the target
	// line exists in the store (the draft-path re-validation runs the
	// same publish gate, whose unresolved-reference tolerance depends
	// on content-state — a sto- draft owns no content-state, so the
	// target must resolve for the re-validation to pass clean).
	putUnit(t, r, unit("feather", "sto", "other", 1, 1), project, filepath.Base("."))

	res, err := Authoring.Relate(r, RelateRequest{
		Target:        "feather/sto:my-item",
		Relationships: relEdges("depends-on=feather/sto:other"),
	})
	if err != nil {
		t.Fatalf("Relate on a draft: %v", err)
	}
	if res.State != "draft" {
		t.Errorf("State = %q, want draft", res.State)
	}
	if res.Target != "feather/sto:my-item" {
		t.Errorf("Target = %q, want feather/sto:my-item", res.Target)
	}
	if len(res.Added) != 1 || res.Added[0] != (exchange.Relationship{Type: "depends-on", Target: "feather/sto:other"}) {
		t.Errorf("Added = %+v, want the depends-on edge", res.Added)
	}

	// The draft file carries the edge (camelCase field spelling).
	data, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"relationships": {`) ||
		!strings.Contains(text, `"dependsOn": [`) ||
		!strings.Contains(text, `"feather/sto:other"`) {
		t.Errorf("draft file missing the related edge:\n%s", text)
	}
	// No published instance was created.
	if _, ok, err := r.Knowledge.Object("feather/sto:my-item:1"); err != nil || ok {
		t.Errorf("relate on a draft must not publish, got %v, %v", ok, err)
	}

	// The post-mutation re-validation passes (the draft is still valid).
	if res.DraftValidation == nil || res.DraftValidation.Report == nil || !res.DraftValidation.Report.Pass() {
		t.Errorf("the related draft must re-validate clean, got %+v", res.DraftValidation)
	}
}

// TestRelateDraftDuplicateIdempotent: a duplicate edge on the draft
// path writes nothing and leaves the file byte-identical.
func TestRelateDraftDuplicateIdempotent(t *testing.T) {
	r, project := relateDraftEnv(t)
	d := newSTODraft(t, r, project, "feather", "my-item", nil)
	if _, err := Authoring.Relate(r, RelateRequest{
		Target:        "feather/sto:my-item",
		Relationships: relEdges("depends-on=feather/sto:other"),
	}); err != nil {
		t.Fatalf("first relate: %v", err)
	}
	afterFirst, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Authoring.Relate(r, RelateRequest{
		Target:        "feather/sto:my-item",
		Relationships: relEdges("depends-on=feather/sto:other"),
	})
	if err != nil {
		t.Fatalf("duplicate relate: %v", err)
	}
	if res.State != "unchanged" || len(res.Added) != 0 {
		t.Errorf("State = %q, Added = %+v; want unchanged with no additions", res.State, res.Added)
	}
	afterSecond, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Errorf("a duplicate relate must not touch the draft file:\n%s", afterSecond)
	}
}

// TestRelateDraftUnresolvedTargetTolerated: the draft path tolerates
// unresolved targets by design — the edge lands before publish, and
// publish re-checks.
func TestRelateDraftUnresolvedTargetTolerated(t *testing.T) {
	r, project := relateDraftEnv(t)
	d := newSTODraft(t, r, project, "feather", "my-item", nil)
	if _, err := Authoring.Relate(r, RelateRequest{
		Target:        "feather/sto:my-item",
		Relationships: relEdges("depends-on=feather/sto:ghost"),
	}); err != nil {
		t.Fatalf("a draft-path unresolved target must be tolerated: %v", err)
	}
	data, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"feather/sto:ghost"`) {
		t.Errorf("draft file missing the tolerated edge:\n%s", data)
	}
}

// TestRelateDraftMdRefused: a legacy Markdown draft is refused — relate
// cannot mutate it deterministically.
func TestRelateDraftMdRefused(t *testing.T) {
	r, project := relateDraftEnv(t)
	dir := filepath.Join(r.Path(), "drafts", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "---\nnamespace: feather\ntype: sto\nid: my-item\n---\n# my-item\n"
	if err := os.WriteFile(filepath.Join(dir, "sto-my-item.md"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Authoring.Relate(r, RelateRequest{
		Target:        "feather/sto:my-item",
		Relationships: relEdges("depends-on=feather/sto:other"),
	})
	var refusal *RelateRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Relate error = %v, want *RelateRefusal", err)
	}
	if !strings.Contains(refusal.Error(), "legacy Markdown draft") {
		t.Errorf("refusal = %q, want the Markdown-draft message", refusal.Error())
	}
	// The legacy draft file is untouched.
	data, err := os.ReadFile(filepath.Join(dir, "sto-my-item.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != legacy {
		t.Errorf("a refused relate must not touch the draft:\n%s", data)
	}
}

// TestRelateDraftNamespaceMismatch: a qualified target whose namespace
// differs from the draft frontmatter's namespace is refused (mirrors
// publish's namespace check).
func TestRelateDraftNamespaceMismatch(t *testing.T) {
	r, project := relateDraftEnv(t)
	newSTODraft(t, r, project, "feather", "my-item", nil)
	_, err := Authoring.Relate(r, RelateRequest{
		Target:        "other/sto:my-item",
		Relationships: relEdges("depends-on=feather/sto:other"),
	})
	var refusal *RelateRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Relate error = %v, want *RelateRefusal", err)
	}
	if !strings.Contains(refusal.Error(), "does not match draft namespace") {
		t.Errorf("refusal = %q, want the namespace-mismatch message", refusal.Error())
	}
}

// --- namespace resolution ----------------------------------------------

// TestRelateUnqualifiedTargetUsesRepoNamespace: an unqualified target
// resolves its namespace from the repository context (eka.yaml +
// registration) — the same resolution `eka new` and `eka transition`
// use.
func TestRelateUnqualifiedTargetUsesRepoNamespace(t *testing.T) {
	r := testRuntime(t)
	repoDir := t.TempDir()
	writeRuntimeEKAFile(t, repoDir, "feather-project", "feather-repo", "feather")
	m := metadata.Metadata{Version: 1, Project: "feather-project", Name: "feather-repo", Namespace: "feather"}
	if _, _, _, err := r.ws.RegisterRepoMetadata(repoDir, m); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoDir)
	putUnit(t, r, specUnit("feather", "a", 1), "feather-project", "feather-repo")
	putUnit(t, r, specUnit("feather", "b", 1), "feather-project", "feather-repo")

	res, err := Authoring.Relate(r, RelateRequest{
		Target:        "spec:a",
		Relationships: relEdges("depends-on=spec:b"),
	})
	if err != nil {
		t.Fatalf("Relate with an unqualified target: %v", err)
	}
	if res.Target != "feather/spec:a" {
		t.Errorf("Target = %q, want feather/spec:a (namespace from the repository)", res.Target)
	}
	u, ok, err := r.Knowledge.Object("feather/spec:a:1")
	if err != nil || !ok {
		t.Fatalf("Object = %v, %v", ok, err)
	}
	if len(u.Relationships) != 1 {
		t.Errorf("Relationships = %+v, want the depends-on edge", u.Relationships)
	}
}

// TestRelateUnqualifiedTargetOutsideRepoRefused: without a repository
// context an unqualified target cannot resolve a namespace — a
// deterministic refusal with the qualify-the-target hint.
func TestRelateUnqualifiedTargetOutsideRepoRefused(t *testing.T) {
	r := testRuntime(t)
	// A project registered elsewhere, cwd outside any repository.
	registerWorld(t, r, "proj-a", "repo-a")
	putUnit(t, r, specUnit("acme", "a", 1), "proj-a", "repo-a")
	t.Chdir(t.TempDir())

	_, err := Authoring.Relate(r, RelateRequest{
		Target:        "spec:a",
		Relationships: relEdges("depends-on=spec:b"),
	})
	var refusal *RelateRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Relate error = %v, want *RelateRefusal", err)
	}
	if !strings.Contains(refusal.Error(), "cannot resolve a namespace") {
		t.Errorf("refusal = %q, want the namespace-resolution message", refusal.Error())
	}
}

// TestRelateQualifiedCrossNamespaceRefused: inside a repository
// context a QUALIFIED target whose namespace differs from the
// repository's is refused — cross-platform access is read-only, the
// same ownership gate `eka new`, `eka publish` and `eka transition`
// enforce (the draft path already protects via its frontmatter check;
// this gate closes the published path and the whole entry point).
func TestRelateQualifiedCrossNamespaceRefused(t *testing.T) {
	r := testRuntime(t)
	repoDir := t.TempDir()
	writeRuntimeEKAFile(t, repoDir, "feather-project", "feather-repo", "feather")
	m := metadata.Metadata{Version: 1, Project: "feather-project", Name: "feather-repo", Namespace: "feather"}
	if _, _, _, err := r.ws.RegisterRepoMetadata(repoDir, m); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoDir)

	_, err := Authoring.Relate(r, RelateRequest{
		Target:        "other/spec:a",
		Relationships: relEdges("depends-on=spec:b"),
	})
	var refusal *RelateRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Relate error = %v, want *RelateRefusal", err)
	}
	if !strings.Contains(refusal.Error(), "cross-platform access is read-only") {
		t.Errorf("refusal = %q, want the cross-platform ownership message", refusal.Error())
	}
}
