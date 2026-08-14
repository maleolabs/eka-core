package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ensureTest sets EKA_HOME to a temp dir and ensures the workspace.
func ensureTest(t *testing.T) *Workspace {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	w, err := Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func TestHomeDirEKAHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EKA_HOME", dir)
	got, err := HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(dir) {
		t.Errorf("HomeDir = %q, want %q", got, dir)
	}
}

func TestHomeDirRejectsRelativeEKAHome(t *testing.T) {
	t.Setenv("EKA_HOME", "relative")
	if _, err := HomeDir(); err == nil {
		t.Error("relative EKA_HOME must error")
	} else if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error must explain the absolute requirement, got %v", err)
	}
}

func TestHomeDirDefaultsToUserHome(t *testing.T) {
	os.Unsetenv("EKA_HOME")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no user home dir in this environment")
	}
	got, err := HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(home, ".eka") {
		t.Errorf("HomeDir = %q, want %q", got, filepath.Join(home, ".eka"))
	}
}

func TestEnsureCreatesLayout(t *testing.T) {
	w := ensureTest(t)
	if _, err := os.Stat(filepath.Join(w.Dir, "workspace.json")); err != nil {
		t.Errorf("workspace.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Dir, "eka.db")); err != nil {
		t.Errorf("eka.db missing: %v", err)
	}
	sv, id, created := w.Meta()
	if sv != 1 {
		t.Errorf("schema version = %d, want 1", sv)
	}
	if !strings.HasPrefix(id, "eka-") || len(id) != 16 {
		t.Errorf("id = %q, want eka-<12 hex chars>", id)
	}
	if created == "" || len(created) != 10 {
		t.Errorf("created = %q, want YYYY-MM-DD", created)
	}
	// Deterministic id: re-ensuring the same dir yields the same id.
	w2, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	_, id2, _ := w2.Meta()
	if id != id2 {
		t.Errorf("id changed between Ensure calls: %q vs %q", id, id2)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EKA_HOME", dir)
	if _, err := Ensure(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("second Ensure must not rewrite workspace.json")
	}
}

func TestRegisterRepoBasenameProject(t *testing.T) {
	w := ensureTest(t)
	repoPath := filepath.Join(t.TempDir(), "myproj")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	project, repo, created, err := w.RegisterRepo(repoPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if project.ID != "myproj" || repo.Name != "myproj" {
		t.Errorf("project = %+v, repo = %+v; want id/name myproj", project, repo)
	}
	if !created {
		t.Error("first registration must report created")
	}
	if repo.Path != filepath.Clean(repoPath) {
		t.Errorf("repo path = %q, want %q", repo.Path, filepath.Clean(repoPath))
	}

	// Re-register: not created, path update.
	project2, repo2, created2, err := w.RegisterRepo(repoPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Error("second registration must report not created")
	}
	if project2.ID != project.ID || repo2.Path != repo.Path {
		t.Errorf("second registration changed identity: %+v %+v", project2, repo2)
	}
}

func TestRegisterRepoExplicitName(t *testing.T) {
	w := ensureTest(t)
	repoPath := filepath.Join(t.TempDir(), "actual-dir")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	project, repo, _, err := w.RegisterRepo(repoPath, "custom-name")
	if err != nil {
		t.Fatal(err)
	}
	if project.ID != "custom-name" {
		t.Errorf("explicit name must become the project: project %+v", project)
	}
	if repo.Name != "actual-dir" {
		t.Errorf("repo name must be the basename, got %q", repo.Name)
	}
	if repo.ProjectID != "custom-name" {
		t.Errorf("repo project = %q, want custom-name", repo.ProjectID)
	}
}

func TestFindRepoByPath(t *testing.T) {
	w := ensureTest(t)
	repoPath := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
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
	if repo.Name != "proj" {
		t.Errorf("repo name = %q, want proj", repo.Name)
	}
}

func TestProjectsAndReposSorted(t *testing.T) {
	w := ensureTest(t)
	for _, name := range []string{"zeta", "alpha"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := w.RegisterRepo(dir, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Two repos under one project (same --name, different basenames).
	dirA := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.RegisterRepo(dirA, "multi"); err != nil {
		t.Fatal(err)
	}
	dirB := filepath.Join(t.TempDir(), "frontend")
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.RegisterRepo(dirB, "multi"); err != nil {
		t.Fatal(err)
	}

	projects, err := w.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 3 || projects[0].ID != "alpha" || projects[1].ID != "multi" || projects[2].ID != "zeta" {
		t.Errorf("projects = %+v, want sorted [alpha multi zeta]", projects)
	}

	repos, err := w.Repos("multi")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].Name != "backend" || repos[1].Name != "frontend" {
		t.Errorf("repos = %+v, want sorted by name [backend frontend]", repos)
	}
}

// TestStoreAccessor: Store exposes the canonical store handle for the
// Runtime Kernel (packages workspace, sync, store, runtime).
func TestStoreAccessor(t *testing.T) {
	w := ensureTest(t)
	if w.Store() == nil {
		t.Fatal("Store must return the opened canonical store")
	}
	if v, err := w.Store().SchemaVersion(); err != nil || v < 1 {
		t.Errorf("SchemaVersion = %d, %v; want the current schema", v, err)
	}
}

func TestOpenAlias(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	w, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.Path() == "" {
		t.Error("Open must resolve a workspace path")
	}
}

// TestRepoNamespaceColumn: the registry carries the repository's
// default namespace (schema v3) — ” at registration, then the value
// the sync engine records through the canonical store — and every
// registry query surfaces it.
func TestRepoNamespaceColumn(t *testing.T) {
	w := ensureTest(t)
	dir := t.TempDir()
	if _, _, _, err := w.RegisterRepo(dir, "proj"); err != nil {
		t.Fatal(err)
	}
	// Registration records '' (the namespace is a sync concern).
	repo, found, err := w.FindRepo(dir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if repo.Namespace != "" {
		t.Errorf("namespace at registration = %q, want \"\"", repo.Namespace)
	}
	// The sync engine records the resolved namespace through the store.
	if err := w.Store().SetRepoNamespace(repo.ProjectID, repo.Name, "atrium-api"); err != nil {
		t.Fatal(err)
	}
	// FindRepo and Repos both surface the value.
	repo, found, err = w.FindRepo(dir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if repo.Namespace != "atrium-api" {
		t.Errorf("FindRepo namespace = %q, want atrium-api", repo.Namespace)
	}
	repos, err := w.Repos("proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Namespace != "atrium-api" {
		t.Errorf("Repos = %+v, want the namespace atrium-api", repos)
	}
	// Re-registration (idempotent upsert) must NOT reset the namespace.
	if _, _, created, err := w.RegisterRepo(dir, "proj"); err != nil || created {
		t.Fatalf("re-registration = %v, %v; want created == false", created, err)
	}
	repo, found, err = w.FindRepo(dir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if repo.Namespace != "atrium-api" {
		t.Errorf("namespace after re-registration = %q, want atrium-api (preserved)", repo.Namespace)
	}
}

// TestReservedRepoName (m6): the repository name "runtime" is reserved
// for workspace-native knowledge (the provenance sentinel of published
// objects, runtime/draft.go draftSourceRepo) — RegisterRepo refuses a
// repository whose basename is exactly "runtime" so the sentinel can
// never collide with a real repository's provenance pair.
func TestReservedRepoName(t *testing.T) {
	w := ensureTest(t)
	dir := t.TempDir()
	runtimeRepo := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(runtimeRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := w.RegisterRepo(runtimeRepo, "")
	if err == nil {
		t.Fatal("a repository named runtime must be refused")
	}
	if !strings.Contains(err.Error(), `repository name "runtime" is reserved for workspace-native knowledge`) {
		t.Errorf("refusal error = %v, want the reserved-name message", err)
	}
	// The reserved name is also refused under an explicit project.
	if _, _, _, err := w.RegisterRepo(runtimeRepo, "some-project"); err == nil {
		t.Error("the reserved name must be refused regardless of the project")
	}
	// Other names are unaffected.
	ok := filepath.Join(dir, "my-repo")
	if err := os.MkdirAll(ok, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.RegisterRepo(ok, ""); err != nil {
		t.Errorf("a normal repository name must register: %v", err)
	}
}
