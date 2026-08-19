package sync

import (
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
)

// This file tests the adopt side of the sync engine (ADR-032 Option
// C2): the re-attribution of workspace-native units (source_repo =
// "runtime") to the repository provenance. The fixtures reuse the sync
// test helpers (newWorkspace, copyFixture, writeEKAFile) and seed
// workspace-native units directly through the store (the same
// provenance sentinel `eka publish` records).

// seedRuntimeUnit persists one workspace-native unit (source_repo =
// "runtime") into the project and returns its canonical form.
func seedRuntimeUnit(t *testing.T, w *workspace.Workspace, projectID, ns, typeToken, id string, version int, content string) string {
	t.Helper()
	u := &exchange.Unit{
		Identity: exchange.Identity{Namespace: ns, Type: typeToken, ID: id, InstanceVersion: version},
		Revision: 1,
		StateVector: exchange.StateVector{
			ExecutionState: "planned",
			ExistenceState: "active",
		},
		ChangeLog: []exchange.ChangeLogEntry{
			{Date: "2026-08-07", Domain: "execution-state", From: "-", To: "planned", By: conformance.User("Engineering")},
			{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		},
		Relationships:  []exchange.Relationship{},
		Classification: exchange.Classification{},
		Content:        exchange.ContentRef{Representation: exchange.ContentRepresentation, File: "content"},
		ContentPayload: []byte(content),
	}
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()
	unitJSON, err := exchange.MarshalUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Store().PutUnit(unitJSON, u.ContentPayload, store.Ref{
		Form:            u.CanonicalIdentityForm,
		ProjectID:       projectID,
		SourceRepo:      workspace.ReservedRepoName,
		Namespace:       ns,
		Type:            typeToken,
		ID:              id,
		InstanceVersion: version,
		Revision:        u.Revision,
		UpdatedAt:       "2026-08-07T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	return u.CanonicalIdentityForm
}

// adoptEnv builds the adopt test environment: a synced fixture
// repository (registered, 4 repo-attributed units) plus two
// workspace-native units in the same project. It returns the workspace
// and the resolved repository record.
func adoptEnv(t *testing.T) (*workspace.Workspace, workspace.Repo) {
	t.Helper()
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatalf("settling sync: %v", err)
	}
	repo, found, err := w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	seedRuntimeUnit(t, w, repo.ProjectID, repo.Namespace, "sto", "runtime-only", 1, "# runtime-only\n\n## Description\n\nadopt me\n\n## Acceptance Criteria\n\nac\n")
	seedRuntimeUnit(t, w, repo.ProjectID, repo.Namespace, "sto", "runtime-second", 1, "# runtime-second\n\n## Description\n\nadopt me too\n\n## Acceptance Criteria\n\nac\n")
	return w, repo
}

// TestAdoptDryRunNoChanges: a dry run computes the identical result
// (adopted count) without touching the store — the workspace-native
// references stay, no repository references appear.
func TestAdoptDryRunNoChanges(t *testing.T) {
	w, repo := adoptEnv(t)

	result, err := Adopt(w, repo, nil, true)
	if err != nil {
		t.Fatalf("Adopt(dryRun): %v", err)
	}
	if !result.DryRun {
		t.Error("dry run must report DryRun = true")
	}
	if result.Units != 2 {
		t.Errorf("dry run adopted = %d, want 2", result.Units)
	}
	if len(result.Skipped) != 0 {
		t.Errorf("dry run skipped = %v, want none", result.Skipped)
	}

	runtimeRefs, err := w.Store().Refs(repo.ProjectID, workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 2 {
		t.Errorf("workspace-native refs after dry run = %d, want 2 (no mutation)", len(runtimeRefs))
	}
	repoRefs, err := w.Store().Refs(repo.ProjectID, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(repoRefs) != 4 {
		t.Errorf("repository refs after dry run = %d, want 4 (no mutation)", len(repoRefs))
	}
}

// TestAdoptAllRuntimeUnits: adopting without targets re-attributes
// every workspace-native unit of the project to the repository
// provenance; the store then resolves them under the repository.
func TestAdoptAllRuntimeUnits(t *testing.T) {
	w, repo := adoptEnv(t)

	result, err := Adopt(w, repo, nil, false)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if result.DryRun {
		t.Error("a real run must report DryRun = false")
	}
	if result.Units != 2 {
		t.Errorf("adopted = %d, want 2", result.Units)
	}

	runtimeRefs, err := w.Store().Refs(repo.ProjectID, workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 0 {
		t.Errorf("workspace-native refs after adopt = %d, want 0", len(runtimeRefs))
	}
	repoRefs, err := w.Store().Refs(repo.ProjectID, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(repoRefs) != 6 {
		t.Errorf("repository refs after adopt = %d, want 6 (4 seeded + 2 adopted)", len(repoRefs))
	}
	for _, r := range repoRefs {
		if r.SourceRepo != repo.Name {
			t.Errorf("reference %s attributed to %q, want %q", r.Form, r.SourceRepo, repo.Name)
		}
	}

	// The adopted units resolve under the repository provenance.
	units, err := w.Store().Units(repo.ProjectID, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	forms := map[string]bool{}
	for _, u := range units {
		forms[u.CanonicalIdentityForm] = true
	}
	for _, want := range []string{
		repo.Namespace + "/sto:runtime-only:1",
		repo.Namespace + "/sto:runtime-second:1",
	} {
		if !forms[want] {
			t.Errorf("store.Units(%s) must contain %s, got %v", repo.Name, want, forms)
		}
	}
}

// TestAdoptSpecificTarget: a target adopts only the matching
// workspace-native unit; the other stays under the runtime provenance.
func TestAdoptSpecificTarget(t *testing.T) {
	w, repo := adoptEnv(t)

	result, err := Adopt(w, repo, []string{repo.Namespace + "/sto:runtime-only"}, false)
	if err != nil {
		t.Fatalf("Adopt(target): %v", err)
	}
	if result.Units != 1 {
		t.Errorf("adopted = %d, want 1", result.Units)
	}

	runtimeRefs, err := w.Store().Refs(repo.ProjectID, workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 1 || runtimeRefs[0].Form != repo.Namespace+"/sto:runtime-second:1" {
		t.Errorf("workspace-native refs after targeted adopt = %+v, want only the second unit", runtimeRefs)
	}
	if _, ok, err := w.Store().Ref(repo.Namespace + "/sto:runtime-only:1"); err != nil || !ok {
		t.Fatalf("adopted reference missing: ok = %v, err = %v", ok, err)
	} else if ref, _, _ := w.Store().Ref(repo.Namespace + "/sto:runtime-only:1"); ref.SourceRepo != repo.Name {
		t.Errorf("adopted reference source repo = %q, want %q", ref.SourceRepo, repo.Name)
	}
}

// TestAdoptTargetInstanceVersion: a target with the
// `:<instance-version>` suffix adopts exactly that instance.
func TestAdoptTargetInstanceVersion(t *testing.T) {
	w, repo := adoptEnv(t)
	// A second instance of the same line, also workspace-native.
	seedRuntimeUnit(t, w, repo.ProjectID, repo.Namespace, "sto", "runtime-only", 2, "# runtime-only v2\n\n## Description\n\nsecond instance\n\n## Acceptance Criteria\n\nac\n")

	result, err := Adopt(w, repo, []string{repo.Namespace + "/sto:runtime-only:2"}, false)
	if err != nil {
		t.Fatalf("Adopt(target with version): %v", err)
	}
	if result.Units != 1 {
		t.Errorf("adopted = %d, want 1 (only instance 2)", result.Units)
	}
	runtimeRefs, err := w.Store().Refs(repo.ProjectID, workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 2 {
		t.Errorf("workspace-native refs = %d, want 2 (instances 1 of both lines stay)", len(runtimeRefs))
	}
}

// TestAdoptTargetNotRuntimeRefused: a target matching no
// workspace-native unit — a missing identity or an identity the
// repository already owns — is refused deterministically.
func TestAdoptTargetNotRuntimeRefused(t *testing.T) {
	w, repo := adoptEnv(t)

	for _, target := range []string{
		repo.Namespace + "/sto:missing",        // no unit at all
		repo.Namespace + "/sto:login-email",    // a repository-owned unit (seeded by the fixture sync)
		repo.Namespace + "/sto:runtime-only:9", // a workspace-native line, wrong instance
	} {
		_, err := Adopt(w, repo, []string{target}, false)
		if err == nil {
			t.Errorf("Adopt(%q) must be refused", target)
			continue
		}
		if !strings.Contains(err.Error(), "sync adopt refused:") {
			t.Errorf("Adopt(%q) error = %v, want the deterministic refusal prefix", target, err)
		}
		if !strings.Contains(err.Error(), "no workspace-native unit") {
			t.Errorf("Adopt(%q) error = %v, want the no-workspace-native-unit sentence", target, err)
		}
	}
	// The refused runs wrote nothing.
	runtimeRefs, err := w.Store().Refs(repo.ProjectID, workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 2 {
		t.Errorf("workspace-native refs after refusals = %d, want 2 (no mutation)", len(runtimeRefs))
	}
}

// TestAdoptTargetNamespaceMismatchRefused: a target whose namespace
// differs from the repository namespace is refused deterministically.
func TestAdoptTargetNamespaceMismatchRefused(t *testing.T) {
	w, repo := adoptEnv(t)

	_, err := Adopt(w, repo, []string{"other-ns/sto:runtime-only"}, false)
	if err == nil {
		t.Fatal("a target with a foreign namespace must be refused")
	}
	if !strings.Contains(err.Error(), "sync adopt refused:") ||
		!strings.Contains(err.Error(), "differs from the repository namespace") {
		t.Errorf("refusal = %v, want the deterministic namespace-mismatch sentence", err)
	}
}

// TestAdoptTargetInvalidRefused: an unparseable target is refused.
func TestAdoptTargetInvalidRefused(t *testing.T) {
	w, repo := adoptEnv(t)

	for _, target := range []string{"", "no-type-token", "ns/unknown-type:x", "ns/sto:"} {
		_, err := Adopt(w, repo, []string{target}, false)
		if err == nil {
			t.Errorf("Adopt(%q) must be refused", target)
			continue
		}
		if !strings.Contains(err.Error(), "sync adopt refused: invalid target") {
			t.Errorf("Adopt(%q) error = %v, want the invalid-target refusal", target, err)
		}
	}
}

// TestAdoptThenPushIncludesUnits: after an adopt, a push assembles the
// adopted units into the snapshot — the transport that carries them to
// a clone on another device.
func TestAdoptThenPushIncludesUnits(t *testing.T) {
	w, repo := adoptEnv(t)
	if _, err := Adopt(w, repo, nil, false); err != nil {
		t.Fatal(err)
	}

	report, err := Run(w, repo.Path, Options{Push: true})
	if err != nil {
		t.Fatalf("push after adopt: %v", err)
	}
	if report.PushedUnits != 6 {
		t.Errorf("pushed units = %d, want 6 (4 seeded + 2 adopted)", report.PushedUnits)
	}
	res, err := exchange.LoadSnapshot(repo.Path + "/exchange/snapshots")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	forms := map[string]bool{}
	for _, u := range res.Package.Units {
		forms[u.CanonicalIdentityForm] = true
	}
	for _, want := range []string{
		repo.Namespace + "/sto:runtime-only:1",
		repo.Namespace + "/sto:runtime-second:1",
	} {
		if !forms[want] {
			t.Errorf("snapshot must carry the adopted unit %s, got %v", want, forms)
		}
	}
}

// TestRunAdoptBeforePush: Options.AdoptBeforePush runs the adopt
// inside Run before the push and reports the adopted units — the
// engine behind `eka sync push --adopt`.
func TestRunAdoptBeforePush(t *testing.T) {
	w, repo := adoptEnv(t)

	report, err := Run(w, repo.Path, Options{Push: true, AdoptBeforePush: true})
	if err != nil {
		t.Fatalf("Run(AdoptBeforePush): %v", err)
	}
	if report.AdoptedUnits != 2 {
		t.Errorf("adopted units = %d, want 2", report.AdoptedUnits)
	}
	if len(report.AdoptedSkipped) != 0 {
		t.Errorf("adopted skipped = %v, want none", report.AdoptedSkipped)
	}
	if report.PushedUnits != 6 {
		t.Errorf("pushed units = %d, want 6 (the adopted units entered the snapshot)", report.PushedUnits)
	}
	// The workspace-native provenance is empty afterwards.
	runtimeRefs, err := w.Store().Refs(repo.ProjectID, workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 0 {
		t.Errorf("workspace-native refs after push --adopt = %d, want 0", len(runtimeRefs))
	}
}

// TestAdoptAtResolvesAndAdopts: AdoptAt resolves the repository with
// the sync conventions (ADR-018 gate, ADR-017 auto-registration) and
// adopts — a fresh repository needs no prior sync.
func TestAdoptAtResolvesAndAdopts(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	// Seed a workspace-native unit into the fixture's project BEFORE
	// any sync (the project id comes from eka.yaml).
	seedRuntimeUnit(t, w, "eka-sync-fixture", "eka-sync-fixture", "sto", "runtime-only", 1, "# runtime-only\n\n## Description\n\nadopt me\n\n## Acceptance Criteria\n\nac\n")

	result, err := AdoptAt(w, repoDir, nil, false)
	if err != nil {
		t.Fatalf("AdoptAt: %v", err)
	}
	if result.Units != 1 {
		t.Errorf("adopted = %d, want 1", result.Units)
	}
	repo, found, err := w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo after AdoptAt = %v, %v (auto-registration expected)", found, err)
	}
	if repo.Name != "eka-sync-fixture" || repo.ProjectID != "eka-sync-fixture" {
		t.Errorf("auto-registered repo = %+v, want the eka.yaml identity", repo)
	}
	if _, ok, err := w.Store().Ref("eka-sync-fixture/sto:runtime-only:1"); err != nil || !ok {
		t.Fatalf("adopted reference missing: ok = %v, err = %v", ok, err)
	} else if ref, _, _ := w.Store().Ref("eka-sync-fixture/sto:runtime-only:1"); ref.SourceRepo != "eka-sync-fixture" {
		t.Errorf("adopted reference source repo = %q, want eka-sync-fixture", ref.SourceRepo)
	}
}

// TestAdoptRefusesWithoutMetadata (ADR-018): a directory whose walk-up
// finds no eka.yaml is not an EKA repository — the adopt is refused
// deterministically.
func TestAdoptRefusesWithoutMetadata(t *testing.T) {
	w := newWorkspace(t)
	dir := t.TempDir()

	_, err := AdoptAt(w, dir, nil, false)
	if err == nil {
		t.Fatal("adopt without eka.yaml must be refused")
	}
	if !strings.Contains(err.Error(), "sync adopt refused:") ||
		!strings.Contains(err.Error(), "is not an EKA repository (no eka.yaml)") ||
		!strings.Contains(err.Error(), "run 'eka init' first") {
		t.Errorf("refusal = %v, want the pinned ADR-018 sentence", err)
	}
}

// TestAdoptNoRuntimeUnits: a project with no workspace-native units
// adopts zero units without error.
func TestAdoptNoRuntimeUnits(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatal(err)
	}
	repo, found, err := w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}

	result, err := Adopt(w, repo, nil, false)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if result.Units != 0 || len(result.Skipped) != 0 {
		t.Errorf("adopt result = %+v, want zero units and no skips", result)
	}
}

// TestAdoptAtDryRunFreshRepoNoRegistration: a dry run on a fresh
// repository (never synced) resolves the identity WITHOUT registering
// it — the dry-run contract "without touching the store" covers the
// registry too (review finding HIGH-1).
func TestAdoptAtDryRunFreshRepoNoRegistration(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	seedRuntimeUnit(t, w, "eka-sync-fixture", "eka-sync-fixture", "sto", "runtime-only", 1, "# runtime-only\n\n## Description\n\nadopt me\n\n## Acceptance Criteria\n\nac\n")

	result, err := AdoptAt(w, repoDir, nil, true)
	if err != nil {
		t.Fatalf("AdoptAt(dryRun): %v", err)
	}
	if result.Units != 1 {
		t.Errorf("dry run adopted = %d, want 1", result.Units)
	}
	if _, found, err := w.FindRepo(repoDir); err != nil || found {
		t.Errorf("FindRepo after dry run = %v, %v — a dry run must not register the repository", found, err)
	}
	// The workspace-native unit is untouched.
	runtimeRefs, err := w.Store().Refs("eka-sync-fixture", workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 1 {
		t.Errorf("workspace-native refs after dry run = %d, want 1 (no mutation)", len(runtimeRefs))
	}
}

// TestAdoptAtDryRunSubdirectoryNoPathRefresh: a dry run from a
// subdirectory resolves the repository root but does NOT re-point the
// stored path (review finding HIGH-1).
func TestAdoptAtDryRunSubdirectoryNoPathRefresh(t *testing.T) {
	w, repo := adoptEnv(t)

	sub := repo.Path + "/docs"
	if _, err := AdoptAt(w, sub, nil, true); err != nil {
		t.Fatalf("AdoptAt(subdir, dryRun): %v", err)
	}
	got, found, err := w.FindRepo(repo.Path)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if got.Path != repo.Path {
		t.Errorf("stored repository path after dry run = %q, want %q (no path refresh)", got.Path, repo.Path)
	}
}

// TestClassifyAdoptMergeAndSkip: the pure classification splits the
// workspace-native references against the repository's existing
// references — no conflict adopts, an identical payload merges
// (adopted), a different payload is refused (skipped). The branch is
// defensive in the current schema (form PRIMARY KEY makes the
// coexistence of a runtime row and a repository row with the same form
// impossible), so it is tested through the pure function (review
// finding MEDIUM-3).
func TestClassifyAdoptMergeAndSkip(t *testing.T) {
	refs := []adoptRuntimeRef{
		{Form: "ns/sto:a:1", ObjectHash: "hash-a"},
		{Form: "ns/sto:b:1", ObjectHash: "hash-b"},
		{Form: "ns/sto:c:1", ObjectHash: "hash-c"},
	}
	repoHashes := map[string]string{
		"ns/sto:b:1": "hash-b", // identical payload: merge
		"ns/sto:c:1": "other",  // different payload: skip
	}

	adopted, skipped := classifyAdopt(refs, repoHashes)
	if len(adopted) != 2 {
		t.Errorf("adopted = %d, want 2 (a + merged b)", len(adopted))
	}
	for _, want := range []string{"ns/sto:a:1", "ns/sto:b:1"} {
		found := false
		for _, rt := range adopted {
			if rt.Form == want {
				found = true
			}
		}
		if !found {
			t.Errorf("adopted must contain %s, got %v", want, adopted)
		}
	}
	if len(skipped) != 1 || skipped[0] != "ns/sto:c:1" {
		t.Errorf("skipped = %v, want [ns/sto:c:1]", skipped)
	}
}

// TestAdoptNamespaceFilterIgnored: an adopt-all run leaves the
// workspace-native units whose namespace differs from the repository
// namespace in place and reports them in Ignored — a repository is one
// platform (review finding LOW-6).
func TestAdoptNamespaceFilterIgnored(t *testing.T) {
	w, repo := adoptEnv(t)
	seedRuntimeUnit(t, w, repo.ProjectID, "other-ns", "sto", "foreign", 1, "# foreign\n\n## Description\n\nforeign namespace\n\n## Acceptance Criteria\n\nac\n")

	result, err := Adopt(w, repo, nil, false)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if result.Units != 2 {
		t.Errorf("adopted = %d, want 2 (the repository-namespace units)", result.Units)
	}
	if len(result.Ignored) != 1 || result.Ignored[0] != "other-ns/sto:foreign:1" {
		t.Errorf("ignored = %v, want [other-ns/sto:foreign:1]", result.Ignored)
	}
	// The foreign unit stays workspace-native.
	runtimeRefs, err := w.Store().Refs(repo.ProjectID, workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 1 || runtimeRefs[0].Form != "other-ns/sto:foreign:1" {
		t.Errorf("workspace-native refs after adopt = %+v, want only the foreign unit", runtimeRefs)
	}
}

// TestAdoptRollbackOnMidTransactionFailure: a failure in the middle of
// the re-attribution rolls the WHOLE adopt back — no partial
// re-attribution survives (review finding MEDIUM-4). The failure is
// forced with a SQLite trigger aborting the INSERT of the second unit.
func TestAdoptRollbackOnMidTransactionFailure(t *testing.T) {
	w, repo := adoptEnv(t)
	second := repo.Namespace + "/sto:runtime-second:1"
	// SQLite triggers cannot bind parameters, so the fixture values are
	// interpolated — both come from the test fixture (no quotes), safe.
	if _, err := w.Store().DB().Exec(
		`CREATE TRIGGER fail_adopt_mid BEFORE INSERT ON object_refs
		 WHEN NEW.source_repo = '` + repo.Name + `' AND NEW.form = '` + second + `'
		 BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatalf("cannot install the failing trigger: %v", err)
	}

	_, err := Adopt(w, repo, nil, false)
	if err == nil {
		t.Fatal("Adopt must fail when the second INSERT aborts")
	}
	if !strings.Contains(err.Error(), "sync adopt failed:") {
		t.Errorf("error = %v, want the deterministic failure prefix", err)
	}

	// Atomicity: every workspace-native reference is intact, no
	// repository reference appeared.
	runtimeRefs, err := w.Store().Refs(repo.ProjectID, workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 2 {
		t.Errorf("workspace-native refs after failed adopt = %d, want 2 (full rollback)", len(runtimeRefs))
	}
	repoRefs, err := w.Store().Refs(repo.ProjectID, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(repoRefs) != 4 {
		t.Errorf("repository refs after failed adopt = %d, want 4 (no partial re-attribution)", len(repoRefs))
	}
}
