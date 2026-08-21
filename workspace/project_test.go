package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/metadata"
)

// testMetadata is the identity used across the metadata registry tests.
var testMetadata = metadata.Metadata{Version: 1, Project: "atrium", Name: "api", Namespace: "atrium-api"}

// writeEKA writes the given identity metadata file into dir.
func writeEKA(t *testing.T, dir string, m metadata.Metadata) {
	t.Helper()
	content := "version: 1\nproject: " + m.Project + "\nname: " + m.Name + "\nnamespace: " + m.Namespace + "\n"
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRegisterRepoMetadata: the metadata identity (project, name,
// namespace) is registered — never the path basename — and the
// namespace column is written immediately.
func TestRegisterRepoMetadata(t *testing.T) {
	w := ensureTest(t)
	repoPath := filepath.Join(t.TempDir(), "some-dir") // basename deliberately differs from the metadata name
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEKA(t, repoPath, testMetadata)

	project, repo, created, err := w.RegisterRepoMetadata(repoPath, testMetadata)
	if err != nil {
		t.Fatalf("RegisterRepoMetadata: %v", err)
	}
	if !created {
		t.Error("first registration must report created")
	}
	if project.ID != "atrium" {
		t.Errorf("project = %+v, want id atrium (from metadata, not the basename)", project)
	}
	if repo.Name != "api" || repo.ProjectID != "atrium" {
		t.Errorf("repo = %+v, want name api under project atrium (from metadata)", repo)
	}
	if repo.Path != filepath.Clean(repoPath) {
		t.Errorf("repo path = %q, want %q", repo.Path, filepath.Clean(repoPath))
	}

	// The namespace is written immediately: every registry read
	// surfaces it.
	got, found, err := w.FindRepo(repoPath)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if got.Namespace != "atrium-api" {
		t.Errorf("namespace after registration = %q, want atrium-api (written immediately)", got.Namespace)
	}
	repos, err := w.Repos("atrium")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Namespace != "atrium-api" {
		t.Errorf("Repos = %+v, want the namespace atrium-api", repos)
	}
}

// TestRegisterRepoMetadataReservedName: the reserved provenance
// sentinel "runtime" is refused on the metadata path too — the check
// runs against m.Name before any registration work.
func TestRegisterRepoMetadataReservedName(t *testing.T) {
	w := ensureTest(t)
	repoPath := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	m := metadata.Metadata{Version: 1, Project: "atrium", Name: "runtime", Namespace: "atrium-api"}
	writeEKA(t, repoPath, m)
	if _, _, _, err := w.RegisterRepoMetadata(repoPath, m); err == nil {
		t.Fatal("a metadata name of runtime must be refused")
	} else if !strings.Contains(err.Error(), `repository name "runtime" is reserved for workspace-native knowledge`) {
		t.Errorf("refusal error = %v, want the reserved-name message", err)
	}
}

// TestRegisterRepoMetadataPathOwnership: the path-ownership rule
// applies on the metadata path — a path already registered under a
// different project is refused.
func TestRegisterRepoMetadataPathOwnership(t *testing.T) {
	w := ensureTest(t)
	repoPath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEKA(t, repoPath, testMetadata)

	// The path is owned by project "other" (legacy registration).
	if _, _, _, err := w.RegisterRepo(repoPath, "other"); err != nil {
		t.Fatal(err)
	}
	// Registering the same path under the metadata project is refused.
	if _, _, _, err := w.RegisterRepoMetadata(repoPath, testMetadata); err == nil {
		t.Fatal("registering an owned path under another project must be refused")
	} else if !strings.Contains(err.Error(), `already registered under project "other"`) {
		t.Errorf("refusal error = %v, want the owning project named", err)
	}
}

// TestFindRepoMetadataIdentity (the worktree case): the registry is
// looked up by the identity (project, name) from eka.yaml, not by the
// path — a worktree at a different path with the same eka.yaml
// resolves to the same repository, and the stored path stays the
// registration path.
func TestFindRepoMetadataIdentity(t *testing.T) {
	w := ensureTest(t)
	parent := t.TempDir()
	repoX := filepath.Join(parent, "repo")
	worktreeY := filepath.Join(parent, "wt")
	for _, dir := range []string{repoX, worktreeY} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The worktree carries the SAME eka.yaml as the repo root.
	writeEKA(t, repoX, testMetadata)
	writeEKA(t, worktreeY, testMetadata)

	if _, _, _, err := w.RegisterRepoMetadata(repoX, testMetadata); err != nil {
		t.Fatal(err)
	}

	// The worktree resolves to the repo registered at X: identity
	// lookup by (project, name); the stored path stays X.
	got, found, err := w.FindRepo(worktreeY)
	if err != nil {
		t.Fatalf("FindRepo(worktree): %v", err)
	}
	if !found {
		t.Fatal("the worktree must resolve to the registered repository")
	}
	if got.ProjectID != "atrium" || got.Name != "api" {
		t.Errorf("resolved repo = %+v, want atrium/api (identity)", got)
	}
	if got.Path != filepath.Clean(repoX) {
		t.Errorf("stored path = %q, want the registration path %q", got.Path, filepath.Clean(repoX))
	}
	if got.Namespace != "atrium-api" {
		t.Errorf("namespace = %q, want atrium-api", got.Namespace)
	}

	// A subdirectory of the repo root resolves via the walk-up too.
	sub := filepath.Join(repoX, "docs", "architecture")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, found, err := w.FindRepo(sub); err != nil || !found || got.Name != "api" {
		t.Errorf("FindRepo(subdir) = %+v, %v, %v; want the repository via walk-up", got, found, err)
	}

	// The repo root itself resolves by identity (the stored path is
	// not the resolver key).
	if got, found, err := w.FindRepo(repoX); err != nil || !found || got.Name != "api" {
		t.Errorf("FindRepo(root) = %+v, %v, %v; want the repository", got, found, err)
	}
}

// TestFindRepoMetadataNotRegistered: eka.yaml present but the identity
// (project, name) not registered → not found (no exact-path fallback:
// the path is auxiliary for metadata repositories).
func TestFindRepoMetadataNotRegistered(t *testing.T) {
	w := ensureTest(t)
	dir := t.TempDir()
	writeEKA(t, dir, testMetadata)
	if _, found, err := w.FindRepo(dir); err != nil || found {
		t.Errorf("FindRepo = %v, %v; want not found (identity unregistered)", found, err)
	}
}

// TestFindRepoLegacyPath: without eka.yaml anywhere above the path the
// legacy exact-path lookup applies unchanged.
func TestFindRepoLegacyPath(t *testing.T) {
	w := ensureTest(t)
	repoPath := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	// Explicitly pin: no metadata above the temp dir (the walk-up to
	// the filesystem root finds nothing).
	if _, _, hasMeta, err := metadata.Find(repoPath); err != nil || hasMeta {
		t.Fatalf("metadata.Find = hasMeta %v, %v; want no metadata (legacy path)", hasMeta, err)
	}

	if _, found, err := w.FindRepo(repoPath); err != nil || found {
		t.Errorf("unregistered repo must not be found: %v, %v", found, err)
	}
	if _, _, _, err := w.RegisterRepo(repoPath, ""); err != nil {
		t.Fatal(err)
	}
	repo, found, err := w.FindRepo(repoPath)
	if err != nil || !found {
		t.Fatalf("registered repo must be found: %v, %v", found, err)
	}
	if repo.Name != "proj" || repo.Namespace != "" {
		t.Errorf("repo = %+v, want name proj with empty namespace (legacy)", repo)
	}
	// An unknown path is not found.
	if _, found, err := w.FindRepo(filepath.Join(repoPath, "..", "ghost")); err != nil || found {
		t.Errorf("unknown path must not be found: %v, %v", found, err)
	}
}

// TestUnregisterRepo: the repos row (project_id, name) is deleted and
// removed reports true; the registry no longer resolves the row. When
// the removed repository was the project's LAST repository the empty
// project row is deleted too.
func TestUnregisterRepo(t *testing.T) {
	w := ensureTest(t)
	dirA := filepath.Join(t.TempDir(), "backend")
	dirB := filepath.Join(t.TempDir(), "frontend")
	for _, dir := range []string{dirA, dirB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := w.RegisterRepo(dirA, "multi"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.RegisterRepo(dirB, "multi"); err != nil {
		t.Fatal(err)
	}

	// Remove the first repo: removed = true, the row is gone, the
	// project stays (it still has one repository).
	removed, err := w.UnregisterRepo("multi", "backend")
	if err != nil || !removed {
		t.Fatalf("UnregisterRepo(backend) = %v, %v; want true, nil", removed, err)
	}
	repos, err := w.Repos("multi")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "frontend" {
		t.Fatalf("Repos after removal = %+v, want only frontend", repos)
	}
	projects, err := w.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ID != "multi" {
		t.Fatalf("Projects after the first removal = %+v, want multi to stay", projects)
	}

	// Remove the LAST repo: removed = true and the emptied project
	// row is deleted too.
	removed, err = w.UnregisterRepo("multi", "frontend")
	if err != nil || !removed {
		t.Fatalf("UnregisterRepo(frontend) = %v, %v; want true, nil", removed, err)
	}
	projects, err = w.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("Projects after removing the last repo = %+v, want the emptied project deleted", projects)
	}
}

// TestUnregisterRepoMissing: an unregistered (project_id, name) pair
// reports removed = false — not an error — and touches nothing.
func TestUnregisterRepoMissing(t *testing.T) {
	w := ensureTest(t)
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.RegisterRepo(dir, ""); err != nil {
		t.Fatal(err)
	}
	removed, err := w.UnregisterRepo("proj", "ghost")
	if err != nil || removed {
		t.Errorf("UnregisterRepo(proj/ghost) = %v, %v; want false, nil", removed, err)
	}
	// The existing repository is untouched by the missing-pair call.
	repos, err := w.Repos("proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "proj" {
		t.Errorf("Repos = %+v, want proj untouched", repos)
	}
	// An unknown project reports false too.
	if removed, err := w.UnregisterRepo("ghost-project", "ghost"); err != nil || removed {
		t.Errorf("UnregisterRepo(ghost-project/ghost) = %v, %v; want false, nil", removed, err)
	}
}

// TestUnregisterRepoReservedName: the reserved provenance sentinel
// "runtime" is refused with the same deterministic error as
// registration — it can never be unregistered because it can never be
// registered.
func TestUnregisterRepoReservedName(t *testing.T) {
	w := ensureTest(t)
	_, err := w.UnregisterRepo("atrium", ReservedRepoName)
	if err == nil {
		t.Fatal("unregistering the reserved name runtime must be refused")
	}
	if !strings.Contains(err.Error(), `repository name "runtime" is reserved for workspace-native knowledge`) {
		t.Errorf("refusal error = %v, want the reserved-name message", err)
	}
}
