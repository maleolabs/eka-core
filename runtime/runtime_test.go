package runtime

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/store"
)

// This file tests the Runtime Kernel end to end: lifecycle, the
// workspace/registry service, the knowledge service, the resolver, the
// relations traversal, the timeline, snapshot reads, integrity and the
// Authoring API. Fixtures: precise store-level seeding (kernel-internal
// tests may address the canonical store directly) plus the shared sync
// fixture (sync/testdata/valid) for the authoring/sync paths.

// syncFixturePath is the shared conformant sync fixture (4 units + 1
// attachment), the same tree the sync package tests seed from.
const syncFixturePath = "../sync/testdata/valid"

// testRuntime sets EKA_HOME to a temp dir and ensures the Runtime.
func testRuntime(t *testing.T) *Runtime {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	r, err := Ensure()
	if err != nil {
		t.Fatalf("runtime.Ensure: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// copyFixture copies a fixture tree into a fresh temp dir.
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// putUnit seeds one unit into the Runtime's canonical store (direct
// store access is legitimate for kernel-internal tests) and returns
// the stored object hash.
func putUnit(t *testing.T, r *Runtime, u *exchange.Unit, project, repo string) string {
	t.Helper()
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()
	unitJSON, err := exchange.MarshalUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	ref := store.Ref{
		Form:            u.CanonicalIdentityForm,
		ProjectID:       project,
		SourceRepo:      repo,
		Namespace:       u.Identity.Namespace,
		Type:            u.Identity.Type,
		ID:              u.Identity.ID,
		InstanceVersion: u.Identity.InstanceVersion,
		Revision:        u.Revision,
		Dimension:       u.Classification.Dimension,
		Domain:          u.Classification.Domain,
		Phase:           u.Phase,
		UpdatedAt:       "2026-08-07T00:00:00Z",
	}
	h, _, err := r.ws.Store().PutUnit(unitJSON, u.ContentPayload, ref)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// unit builds a minimal unit with the given identity and fields.
func unit(ns, typ, id string, version, revision int) *exchange.Unit {
	return &exchange.Unit{
		Identity:       exchange.Identity{Namespace: ns, Type: typ, ID: id, InstanceVersion: version},
		Revision:       revision,
		StateVector:    exchange.StateVector{ContentState: "draft", ExistenceState: "active"},
		ChangeLog:      []exchange.ChangeLogEntry{},
		Relationships:  []exchange.Relationship{},
		Classification: exchange.Classification{},
		Content:        exchange.ContentRef{Representation: "eka/structured-text/1", File: "content"},
		ContentPayload: []byte("body " + ns + "/" + typ + ":" + id),
	}
}

// --- lifecycle ----------------------------------------------------------

// TestEnsureCreatesAndOpens: Ensure initializes the workspace (Path,
// Exists, schema, Close) and repeated Ensure calls are equivalent.
func TestEnsureCreatesAndOpens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	r, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if r.Path() != home {
		t.Errorf("Path = %q, want %q", r.Path(), home)
	}
	if !r.Exists() {
		t.Error("an ensured Runtime must exist")
	}
	if _, err := os.Stat(filepath.Join(home, "workspace.json")); err != nil {
		t.Errorf("Ensure must write workspace.json: %v", err)
	}
	sv, err := r.Integrity.SchemaVersion()
	if err != nil || sv != 4 {
		t.Errorf("SchemaVersion = %d, %v; want 4", sv, err)
	}
	// Services must be wired.
	if r.Workspace == nil || r.Knowledge == nil || r.Resolver == nil ||
		r.Relations == nil || r.Timeline == nil || r.Snapshot == nil || r.Integrity == nil {
		t.Error("every service must be wired on an ensured Runtime")
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Re-open after close.
	r2, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if r2.Path() != home {
		t.Errorf("re-opened Path = %q, want %q", r2.Path(), home)
	}
}

// TestOpenDetachedOnMissingWorkspace: Open never initializes the
// workspace — a missing workspace.json yields a detached Runtime
// (Exists false, Path resolves, Close is a no-op) and its services
// error instead of panicking.
func TestOpenDetachedOnMissingWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	r, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if r.Exists() {
		t.Error("a missing workspace must report Exists() == false")
	}
	if r.Path() != home {
		t.Errorf("Path = %q, want the home dir %q", r.Path(), home)
	}
	// Open must not create anything.
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("Open must not create workspace files, found: %v", entries)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close of a detached Runtime must be a no-op, got %v", err)
	}
	// Services on a detached Runtime error deterministically.
	if _, err := r.Workspace.Projects(); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("service call on a detached Runtime must error with the initialization hint, got %v", err)
	}
	// Every store/registry-touching service errors on a detached
	// Runtime; Snapshot.Read (pure disk read) is the documented
	// exception and must still work.
	detachedServiceErrors := []struct {
		name string
		call func() error
	}{
		{"Knowledge.UnitsByProject", func() error { _, err := r.Knowledge.UnitsByProject("p"); return err }},
		{"Knowledge.Object", func() error { _, _, err := r.Knowledge.Object("ns/sto:x:1"); return err }},
		{"Knowledge.Search", func() error { _, err := r.Knowledge.Search(SearchQuery{ProjectID: "p"}); return err }},
		{"Knowledge.Counts", func() error { _, _, _, err := r.Knowledge.Counts(); return err }},
		{"Resolver.Resolve", func() error { _, _, err := r.Resolver.Resolve("ns/sto:x:1"); return err }},
		{"Resolver.ResolveLine", func() error { _, err := r.Resolver.ResolveLine("ns", "sto", "x"); return err }},
		{"Relations.From", func() error { _, err := r.Relations.From("ns/sto:x:1"); return err }},
		{"Relations.To", func() error { _, err := r.Relations.To("ns/sto:x:1"); return err }},
		{"Relations.Upstream", func() error { _, err := r.Relations.Upstream("ns/sto:x:1"); return err }},
		{"Relations.Downstream", func() error { _, err := r.Relations.Downstream("ns/sto:x:1"); return err }},
		{"Timeline.Line", func() error { _, err := r.Timeline.Line("ns", "sto", "x"); return err }},
		{"Integrity.Verify", func() error { _, err := r.Integrity.Verify(); return err }},
		{"Integrity.SchemaVersion", func() error { _, err := r.Integrity.SchemaVersion(); return err }},
		{"Workspace.RegisterRepo", func() error { _, _, _, err := r.Workspace.RegisterRepo(".", ""); return err }},
		{"Workspace.FindRepo", func() error { _, _, err := r.Workspace.FindRepo("."); return err }},
		{"Workspace.Repos", func() error { _, err := r.Workspace.Repos("p"); return err }},
		{"Workspace.LastSync", func() error { _, _, err := r.Workspace.LastSync("p", "r"); return err }},
		{"Workspace.Status", func() error { _, err := r.Workspace.Status(); return err }},
	}
	for _, tc := range detachedServiceErrors {
		if err := tc.call(); err == nil || !strings.Contains(err.Error(), "not initialized") {
			t.Errorf("%s on a detached Runtime must error with the initialization hint, got %v", tc.name, err)
		}
	}
	// Snapshot.Read is a pure disk read: a missing snapshot path errors
	// with a package-level failure, never the initialization hint.
	if _, err := r.Snapshot.Read(filepath.Join(home, "nonexistent")); err == nil || strings.Contains(err.Error(), "not initialized") {
		t.Errorf("Snapshot.Read on a detached Runtime must be a disk read failure, got %v", err)
	}
}

// TestOpenExistingWorkspace: Open opens an initialized workspace.
func TestOpenExistingWorkspace(t *testing.T) {
	r1 := testRuntime(t)
	if _, _, _, err := r1.Workspace.RegisterRepo(t.TempDir(), "proj"); err != nil {
		t.Fatal(err)
	}
	r2, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if !r2.Exists() {
		t.Error("Open on an existing workspace must report Exists() == true")
	}
	projects, err := r2.Workspace.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ID != "proj" {
		t.Errorf("Open must see the registered project, got %+v", projects)
	}
}

// TestHomeDirWrapsWorkspace: the package-level HomeDir honors EKA_HOME.
func TestHomeDirWrapsWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	got, err := HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(home) {
		t.Errorf("HomeDir = %q, want %q", got, home)
	}
}

// --- workspace service --------------------------------------------------

// TestRegisterRepoLifecycle: register (created), re-register (no-op),
// find, list — deterministic registry behavior through the Runtime.
func TestRegisterRepoLifecycle(t *testing.T) {
	r := testRuntime(t)
	repoDir := t.TempDir()

	project, repo, created, err := r.Workspace.RegisterRepo(repoDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("first registration must report created == true")
	}
	if project.ID != filepath.Base(repoDir) || repo.Name != filepath.Base(repoDir) {
		t.Errorf("register = %s/%s, want the basename project", project.ID, repo.Name)
	}
	// Idempotent re-registration.
	if _, _, created, err := r.Workspace.RegisterRepo(repoDir, ""); err != nil || created {
		t.Errorf("re-registration = %v, %v; want created == false", created, err)
	}
	// Find by normalized absolute path.
	got, found, err := r.Workspace.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if got.Path != repo.Path {
		t.Errorf("FindRepo path = %q, want %q", got.Path, repo.Path)
	}
	if _, found, err := r.Workspace.FindRepo(t.TempDir()); err != nil || found {
		t.Errorf("FindRepo(unknown) = %v, %v; want false", found, err)
	}
	// Lists are deterministic.
	projects, err := r.Workspace.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ID != project.ID {
		t.Errorf("Projects = %+v", projects)
	}
	repos, err := r.Workspace.Repos(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != repo.Name {
		t.Errorf("Repos = %+v", repos)
	}
}

// TestWorkspaceStatusAggregation: Status aggregates metadata, schema,
// deterministic project/repo ordering and last-sync per repository in
// one call.
func TestWorkspaceStatusAggregation(t *testing.T) {
	r := testRuntime(t)
	repoA := copyFixture(t, syncFixturePath)
	repoB := t.TempDir() // registered, never synced

	// Two repositories, two projects: A under "proj-b" (deliberately
	// out of alphabetical registration order) and B under "proj-a".
	if _, _, _, err := r.Workspace.RegisterRepo(repoB, "proj-a"); err != nil {
		t.Fatal(err)
	}
	// The fixture's own eka.yaml records the eka-sync-fixture identity;
	// overwrite it so the metadata identity matches the proj-b
	// registration (ADR-017 identity lookup) and the sync resolves the
	// same repository.
	writeRuntimeEKAFile(t, repoA, "proj-b", filepath.Base(repoA), "eka-sync-fixture")
	if _, _, _, err := r.Workspace.RegisterRepo(repoA, "proj-b"); err != nil {
		t.Fatal(err)
	}
	// Seed the project-proj-b repository through the Authoring API.
	if _, err := Authoring.Sync(r, repoA, SyncOptions{Pull: true, Push: true}); err != nil {
		t.Fatal(err)
	}

	st, err := r.Workspace.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Path != r.Path() {
		t.Errorf("status path = %q, want %q", st.Path, r.Path())
	}
	if !strings.HasPrefix(st.ID, "eka-") || st.Created == "" {
		t.Errorf("status metadata = id %q created %q, want a deterministic id and date", st.ID, st.Created)
	}
	if st.SchemaVersion != 4 {
		t.Errorf("schema version = %d, want 4", st.SchemaVersion)
	}
	// Projects sorted by id: proj-a before proj-b.
	if len(st.Projects) != 2 || st.Projects[0].Project.ID != "proj-a" || st.Projects[1].Project.ID != "proj-b" {
		t.Fatalf("projects must be sorted by id, got %+v", st.Projects)
	}
	// The synced repo carries its last-sync entry; the unsynced one none.
	pa := st.Projects[0]
	if len(pa.Repos) != 1 || pa.Repos[0].Repo.Name != filepath.Base(repoB) || pa.Repos[0].LastSync != nil {
		t.Errorf("unsynced repo must carry LastSync nil, got %+v", pa.Repos)
	}
	pb := st.Projects[1]
	if len(pb.Repos) != 1 || pb.Repos[0].LastSync == nil {
		t.Fatalf("synced repo must carry its last sync, got %+v", pb.Repos)
	}
	if pb.Repos[0].LastSync.Direction != "push" || pb.Repos[0].LastSync.Units != 4 {
		t.Errorf("last sync = %+v, want the push of 4 units (the newest entry)", pb.Repos[0].LastSync)
	}
	// Store totals: 4 objects / 4 payloads / 1 attachment.
	if st.Objects != 4 || st.Payloads != 4 || st.Attachments != 1 {
		t.Errorf("totals = %d/%d/%d, want 4/4/1", st.Objects, st.Payloads, st.Attachments)
	}

	// Determinism: two Status calls produce identical aggregations.
	st2, err := r.Workspace.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.Projects) != len(st.Projects) ||
		st2.Projects[1].Repos[0].LastSync.At != pb.Repos[0].LastSync.At {
		t.Error("Status must be deterministic across calls")
	}
}

// TestLastSyncBeforeAnySync: a repository with no recorded sync has no
// last-sync entry.
func TestLastSyncBeforeAnySync(t *testing.T) {
	r := testRuntime(t)
	repoDir := t.TempDir()
	if _, _, _, err := r.Workspace.RegisterRepo(repoDir, "p"); err != nil {
		t.Fatal(err)
	}
	repo, found, err := r.Workspace.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatal("FindRepo failed")
	}
	entry, ok, err := r.Workspace.LastSync(repo.ProjectID, repo.Name)
	if err != nil || ok || entry != nil {
		t.Errorf("LastSync before any sync = %+v, %v; want nil, false", entry, ok)
	}
}

// --- knowledge service --------------------------------------------------

// registerWorld registers a project/repo pair in the registry — the
// relationship scan (Relations.To) iterates the registry's projects,
// so seeded worlds must be registered. The repo path basename is the
// repo name, so a dir named after the repo is registered under the
// project.
func registerWorld(t *testing.T, r *Runtime, project, repo string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := r.Workspace.RegisterRepo(dir, project); err != nil {
		t.Fatal(err)
	}
}

// seedKnowledgeWorld seeds the cross-project knowledge world of the
// service tests:
//
//	proj-a/repo-a: acme/sto:a:1 (dimension work-items, domain Execution)
//	              acme/adr:001:1 (dimension decisions, domain Architecture,
//	                             phase wave-1)
//	proj-a/repo-b: zeta/sto:z:1 (dimension work-items, domain Execution,
//	                             phase wave-1)
//	proj-b/repo-a: acme/sto:q:1 (dimension work-items)
func seedKnowledgeWorld(t *testing.T, r *Runtime) {
	t.Helper()
	registerWorld(t, r, "proj-a", "repo-a")
	registerWorld(t, r, "proj-a", "repo-b")
	registerWorld(t, r, "proj-b", "repo-a")
	u := unit("acme", "sto", "a", 1, 1)
	u.Classification = exchange.Classification{Dimension: "work-items", Domain: "Execution"}
	putUnit(t, r, u, "proj-a", "repo-a")

	adr := unit("acme", "adr", "001", 1, 1)
	adr.Classification = exchange.Classification{Dimension: "decisions", Domain: "Architecture"}
	adr.Phase = "wave-1"
	putUnit(t, r, adr, "proj-a", "repo-a")

	z := unit("zeta", "sto", "z", 1, 1)
	z.Classification = exchange.Classification{Dimension: "work-items", Domain: "Execution"}
	z.Phase = "wave-1"
	putUnit(t, r, z, "proj-a", "repo-b")

	q := unit("acme", "sto", "q", 1, 1)
	q.Classification = exchange.Classification{Dimension: "work-items"}
	putUnit(t, r, q, "proj-b", "repo-a")
}

// TestKnowledgeUnitsByProject: the projection source — the union of
// every repository of the project, sorted by canonical form; other
// projects stay invisible.
func TestKnowledgeUnitsByProject(t *testing.T) {
	r := testRuntime(t)
	seedKnowledgeWorld(t, r)

	units, err := r.Knowledge.UnitsByProject("proj-a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"acme/adr:001:1", "acme/sto:a:1", "zeta/sto:z:1"}
	if len(units) != len(want) {
		t.Fatalf("UnitsByProject = %d units, want %d", len(units), len(want))
	}
	for i, form := range want {
		if units[i].CanonicalIdentityForm != form {
			t.Errorf("unit %d = %s, want %s (sorted by form)", i, units[i].CanonicalIdentityForm, form)
		}
	}

	// Per-repository view: the provenance pair filters the union.
	repoA, err := r.Knowledge.Units("proj-a", "repo-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(repoA) != 2 || repoA[0].CanonicalIdentityForm != "acme/adr:001:1" {
		t.Errorf("Units(proj-a, repo-a) = %+v", repoA)
	}
	// Empty project: empty non-nil slice.
	none, err := r.Knowledge.UnitsByProject("proj-empty")
	if err != nil {
		t.Fatal(err)
	}
	if none == nil || len(none) != 0 {
		t.Errorf("UnitsByProject(empty) = %v, want empty non-nil slice", none)
	}
}

// TestKnowledgeObject: single-object resolution by canonical form.
func TestKnowledgeObject(t *testing.T) {
	r := testRuntime(t)
	seedKnowledgeWorld(t, r)

	u, ok, err := r.Knowledge.Object("acme/sto:a:1")
	if err != nil || !ok {
		t.Fatalf("Object = %v, %v", ok, err)
	}
	if u.Identity.ID != "a" || u.Classification.Dimension != "work-items" {
		t.Errorf("Object = %+v", u)
	}
	if _, ok, err := r.Knowledge.Object("acme/sto:absent:1"); err != nil || ok {
		t.Errorf("Object(absent) = %v, %v; want false", ok, err)
	}
}

// TestKnowledgeSearch: exact-match filters on identity and the
// classification/context index columns, including cross-field
// combinations; deterministic order; project required.
func TestKnowledgeSearch(t *testing.T) {
	r := testRuntime(t)
	seedKnowledgeWorld(t, r)

	if _, err := r.Knowledge.Search(SearchQuery{}); err == nil {
		t.Error("Search without a project must error")
	}

	forms := func(units []*exchange.Unit) []string {
		out := make([]string, 0, len(units))
		for _, u := range units {
			out = append(out, u.CanonicalIdentityForm)
		}
		return out
	}

	cases := []struct {
		name  string
		query SearchQuery
		want  []string
	}{
		{"project only", SearchQuery{ProjectID: "proj-a"}, []string{"acme/adr:001:1", "acme/sto:a:1", "zeta/sto:z:1"}},
		{"namespace", SearchQuery{ProjectID: "proj-a", Namespace: "acme"}, []string{"acme/adr:001:1", "acme/sto:a:1"}},
		{"type", SearchQuery{ProjectID: "proj-a", Type: "adr"}, []string{"acme/adr:001:1"}},
		{"id", SearchQuery{ProjectID: "proj-a", ID: "z"}, []string{"zeta/sto:z:1"}},
		{"dimension", SearchQuery{ProjectID: "proj-a", Dimension: "decisions"}, []string{"acme/adr:001:1"}},
		{"domain", SearchQuery{ProjectID: "proj-a", Domain: "Execution"}, []string{"acme/sto:a:1", "zeta/sto:z:1"}},
		{"phase", SearchQuery{ProjectID: "proj-a", Phase: "wave-1"}, []string{"acme/adr:001:1", "zeta/sto:z:1"}},
		{"cross dimension+phase", SearchQuery{ProjectID: "proj-a", Dimension: "work-items", Phase: "wave-1"}, []string{"zeta/sto:z:1"}},
		{"cross ns+type+id", SearchQuery{ProjectID: "proj-a", Namespace: "zeta", Type: "sto", ID: "z"}, []string{"zeta/sto:z:1"}},
		{"no match", SearchQuery{ProjectID: "proj-a", Type: "tkt"}, []string{}},
		{"project scoping", SearchQuery{ProjectID: "proj-b", Type: "sto"}, []string{"acme/sto:q:1"}},
	}
	for _, c := range cases {
		units, err := r.Knowledge.Search(c.query)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := forms(units)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

// TestKnowledgeCounts: the canonical store totals.
func TestKnowledgeCounts(t *testing.T) {
	r := testRuntime(t)
	o, p, a, err := r.Knowledge.Counts()
	if err != nil || o != 0 || p != 0 || a != 0 {
		t.Errorf("empty counts = %d/%d/%d, %v", o, p, a, err)
	}
	seedKnowledgeWorld(t, r)
	o, p, a, err = r.Knowledge.Counts()
	if err != nil || o != 4 || p != 4 || a != 0 {
		t.Errorf("seeded counts = %d/%d/%d, %v; want 4/4/0", o, p, a, err)
	}
}

// --- resolver service ---------------------------------------------------

// seedLineWorld seeds the line ns/sto:x with instances 1, 2 and 10
// (the version-10 instance proves the numeric instance ordering of the
// line resolution, which canonical form order does not provide) and
// returns the per-instance hashes.
func seedLineWorld(t *testing.T, r *Runtime) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	for _, v := range []int{1, 2, 10} {
		u := unit("ns", "sto", "x", v, v)
		u.ContentPayload = []byte("body x v" + strings.Repeat("0", 0))
		h := putUnit(t, r, u, "proj-a", "repo-a")
		hashes["ns/sto:x:"+strconv.Itoa(v)] = h
	}
	return hashes
}

// TestResolverCanonicalForm: Resolve with the full canonical form
// returns the exact instance.
func TestResolverCanonicalForm(t *testing.T) {
	r := testRuntime(t)
	seedLineWorld(t, r)

	u, ok, err := r.Resolver.Resolve("ns/sto:x:2")
	if err != nil || !ok {
		t.Fatalf("Resolve = %v, %v", ok, err)
	}
	if u.Identity.InstanceVersion != 2 {
		t.Errorf("Resolve(ns/sto:x:2) = v%d, want v2", u.Identity.InstanceVersion)
	}
	if _, ok, err := r.Resolver.Resolve("ns/sto:x:99"); err != nil || ok {
		t.Errorf("Resolve(unknown instance) = %v, %v; want false", ok, err)
	}
}

// TestResolverLineForm: the qualified line form resolves to the highest
// instance-version of the line — the latest knowledge version
// (ADR-025).
func TestResolverLineForm(t *testing.T) {
	r := testRuntime(t)
	seedLineWorld(t, r)

	u, ok, err := r.Resolver.Resolve("ns/sto:x")
	if err != nil || !ok {
		t.Fatalf("Resolve(line) = %v, %v", ok, err)
	}
	if u.Identity.InstanceVersion != 10 {
		t.Errorf("Resolve(ns/sto:x) = v%d, want the highest instance v10 (the latest knowledge version)", u.Identity.InstanceVersion)
	}
}

// TestResolverRejectsUnqualifiedForms: unqualified references are not
// accepted — the Runtime resolves globally and the reference grammar
// needs a referrer context for bare forms.
func TestResolverRejectsUnqualifiedForms(t *testing.T) {
	r := testRuntime(t)
	seedLineWorld(t, r)
	for _, form := range []string{"sto:x:1", "sto:x", "x"} {
		if _, _, err := r.Resolver.Resolve(form); err == nil {
			t.Errorf("Resolve(%q) must error (canonical/qualified only)", form)
		}
	}
}

// TestResolverResolveLineOrdering: every instance of the line across
// the workspace, sorted by instance-version — numeric, not canonical
// form order.
func TestResolverResolveLineOrdering(t *testing.T) {
	r := testRuntime(t)
	hashes := seedLineWorld(t, r)

	units, err := r.Resolver.ResolveLine("ns", "sto", "x")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 10}
	if len(units) != len(want) {
		t.Fatalf("ResolveLine = %d units, want %d", len(units), len(want))
	}
	for i, v := range want {
		if units[i].Identity.InstanceVersion != v {
			t.Errorf("ResolveLine[%d] = v%d, want v%d (numeric instance order)", i, units[i].Identity.InstanceVersion, v)
		}
		if units[i].Digest != hashes["ns/sto:x:"+strconv.Itoa(v)] {
			t.Errorf("ResolveLine[%d] digest mismatch", i)
		}
	}
	// Empty line: empty non-nil slice.
	none, err := r.Resolver.ResolveLine("ns", "sto", "absent")
	if err != nil {
		t.Fatal(err)
	}
	if none == nil || len(none) != 0 {
		t.Errorf("ResolveLine(absent) = %v, want empty non-nil slice", none)
	}
}

// --- relations service --------------------------------------------------

// seedRelationWorld seeds the relationship world:
//
//	proj-a/repo-a: acme/adr:001:1
//	  amends      -> acme/adr:000-old:1   (unresolvable: draft tolerance)
//	  depends-on  -> acme/sto:a:1
//	  derives-from-> acme/sto:a:1         (duplicate target: resolved once)
//	proj-a/repo-a: acme/sto:a:1            (no relationships)
//	proj-b/repo-b: zeta/sto:z:1
//	  depends-on  -> acme/sto:a:1          (cross-project reference)
func seedRelationWorld(t *testing.T, r *Runtime) {
	t.Helper()
	registerWorld(t, r, "proj-a", "repo-a")
	registerWorld(t, r, "proj-b", "repo-b")
	adr := unit("acme", "adr", "001", 1, 1)
	adr.Relationships = []exchange.Relationship{
		{Type: "amends", Target: "acme/adr:000-old:1"},
		{Type: "depends-on", Target: "acme/sto:a:1"},
		{Type: "derives-from", Target: "acme/sto:a:1"},
	}
	putUnit(t, r, adr, "proj-a", "repo-a")

	putUnit(t, r, unit("acme", "sto", "a", 1, 1), "proj-a", "repo-a")

	z := unit("zeta", "sto", "z", 1, 1)
	z.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "acme/sto:a:1"}}
	putUnit(t, r, z, "proj-b", "repo-b")
}

// TestRelationsFrom: the unit's stored relationships in (type, target)
// order; an unknown form errors, a relation-less unit is empty.
func TestRelationsFrom(t *testing.T) {
	r := testRuntime(t)
	seedRelationWorld(t, r)

	rels, err := r.Relations.From("acme/adr:001:1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Relation{
		{Type: "amends", Target: "acme/adr:000-old:1"},
		{Type: "depends-on", Target: "acme/sto:a:1"},
		{Type: "derives-from", Target: "acme/sto:a:1"},
	}
	if len(rels) != len(want) {
		t.Fatalf("From = %+v, want %+v", rels, want)
	}
	for i := range want {
		if rels[i] != want[i] {
			t.Errorf("From[%d] = %+v, want %+v", i, rels[i], want[i])
		}
	}

	none, err := r.Relations.From("acme/sto:a:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("From of a relation-less unit = %+v, want empty", none)
	}

	if _, err := r.Relations.From("acme/sto:absent:1"); err == nil {
		t.Error("From of an unknown form must error")
	}
}

// TestRelationsTo: the reverse edges — every relationship pointing AT
// the target across the whole workspace (projects by id, units by
// form), with the referring unit as Target.
func TestRelationsTo(t *testing.T) {
	r := testRuntime(t)
	seedRelationWorld(t, r)

	rels, err := r.Relations.To("acme/sto:a:1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Relation{
		{Type: "depends-on", Target: "acme/adr:001:1"},   // proj-a first
		{Type: "derives-from", Target: "acme/adr:001:1"}, // stored order within the unit
		{Type: "depends-on", Target: "zeta/sto:z:1"},     // proj-b second
	}
	if len(rels) != len(want) {
		t.Fatalf("To = %+v, want %+v", rels, want)
	}
	for i := range want {
		if rels[i] != want[i] {
			t.Errorf("To[%d] = %+v, want %+v", i, rels[i], want[i])
		}
	}
	// A target nobody references: empty.
	none, err := r.Relations.To("acme/sto:ghost:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("To of an unreferenced target = %+v, want empty", none)
	}
}

// TestRelationsToEmptyWorkspace: an initialized workspace with no
// projects yields an empty, non-nil result — never an error.
func TestRelationsToEmptyWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	r, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	rels, err := r.Relations.To("acme/sto:a:1")
	if err != nil {
		t.Fatal(err)
	}
	if rels == nil || len(rels) != 0 {
		t.Errorf("To on an empty workspace = %v, want an empty non-nil slice", rels)
	}
}

// TestRelationsUpstream: the resolved outgoing targets — unresolvable
// targets skipped (draft tolerance), duplicates resolved once, sorted
// by canonical form.
func TestRelationsUpstream(t *testing.T) {
	r := testRuntime(t)
	seedRelationWorld(t, r)

	units, err := r.Relations.Upstream("acme/adr:001:1")
	if err != nil {
		t.Fatal(err)
	}
	// amends->old is skipped (unresolvable); depends-on and
	// derives-from both point at acme/sto:a:1 — resolved once.
	if len(units) != 1 || units[0].CanonicalIdentityForm != "acme/sto:a:1" {
		t.Errorf("Upstream = %+v, want [acme/sto:a:1]", formsOf(units))
	}
}

// TestRelationsDownstream: the referring units, cross-project, sorted
// by canonical form.
func TestRelationsDownstream(t *testing.T) {
	r := testRuntime(t)
	seedRelationWorld(t, r)

	units, err := r.Relations.Downstream("acme/sto:a:1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"acme/adr:001:1", "zeta/sto:z:1"}
	got := formsOf(units)
	if len(got) != len(want) {
		t.Fatalf("Downstream = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Downstream = %v, want %v", got, want)
			break
		}
	}
	// A unit nobody references: empty.
	units, err = r.Relations.Downstream("acme/sto:absent:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 0 {
		t.Errorf("Downstream of an unreferenced form = %+v, want empty", formsOf(units))
	}
}

func formsOf(units []*exchange.Unit) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, u.CanonicalIdentityForm)
	}
	return out
}

// --- timeline service ---------------------------------------------------

// TestTimelineLine: every instance of the line across the workspace
// sorted by instance-version, with the decoded change log and the
// reference's object hash.
func TestTimelineLine(t *testing.T) {
	r := testRuntime(t)
	entries := []exchange.ChangeLogEntry{
		{Date: "2026-08-01", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Eng")},
		{Date: "2026-08-02", Domain: "content-state", From: "draft", To: "review", By: conformance.User("Eng")},
	}
	u1 := unit("ns", "sto", "x", 1, 1)
	u1.ChangeLog = entries[:1]
	h1 := putUnit(t, r, u1, "proj-a", "repo-a")

	u2 := unit("ns", "sto", "x", 2, 2)
	u2.ChangeLog = entries
	h2 := putUnit(t, r, u2, "proj-a", "repo-a")

	// A second project instance of the same line must be included.
	u3 := unit("ns", "sto", "x", 3, 3)
	u3.ChangeLog = entries[:1]
	h3 := putUnit(t, r, u3, "proj-b", "repo-b")

	line, err := r.Timeline.Line("ns", "sto", "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(line) != 3 {
		t.Fatalf("Line = %d entries, want 3", len(line))
	}
	wantHashes := []string{h1, h2, h3}
	wantLogLen := []int{1, 2, 1}
	for i, e := range line {
		if e.InstanceVersion != i+1 {
			t.Errorf("entry %d instance version = %d, want %d (ascending)", i, e.InstanceVersion, i+1)
		}
		if e.ObjectHash != wantHashes[i] {
			t.Errorf("entry %d object hash = %q, want %q (the reference's hash)", i, e.ObjectHash, wantHashes[i])
		}
		if len(e.ChangeLog) != wantLogLen[i] {
			t.Errorf("entry %d change log = %+v, want %d transitions", i, e.ChangeLog, wantLogLen[i])
		}
		if e.ChangeLog[0] != entries[0] {
			t.Errorf("entry %d first transition = %+v, want %+v", i, e.ChangeLog[0], entries[0])
		}
		if e.Form != "ns/sto:x:"+strconv.Itoa(i+1) || e.Revision != e.InstanceVersion {
			t.Errorf("entry %d = %s r%d, want the canonical form and revision", i, e.Form, e.Revision)
		}
	}
	// Empty line: empty non-nil slice.
	none, err := r.Timeline.Line("ns", "sto", "absent")
	if err != nil {
		t.Fatal(err)
	}
	if none == nil || len(none) != 0 {
		t.Errorf("Line(absent) = %v, want empty non-nil slice", none)
	}
}

// --- snapshot service ---------------------------------------------------

// TestSnapshotReadVerified: Read verifies a synced repository's
// snapshot byte-exact and returns the package model.
func TestSnapshotReadVerified(t *testing.T) {
	r := testRuntime(t)
	repo := copyFixture(t, syncFixturePath)
	if _, err := Authoring.Sync(r, repo, SyncOptions{Pull: true, Push: true}); err != nil {
		t.Fatal(err)
	}
	snapshotDir := filepath.Join(repo, "exchange", "snapshots")
	pkg, err := r.Snapshot.Read(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Units) != 4 {
		t.Errorf("Read units = %d, want 4", len(pkg.Units))
	}
	if len(pkg.Attachments) != 1 {
		t.Errorf("Read attachments = %d, want 1", len(pkg.Attachments))
	}
	if pkg.Header.PackageIdentityLabel == "" {
		t.Error("Read must carry the package identity label")
	}
}

// TestSnapshotReadCorrupt: a structurally corrupted snapshot is refused
// — the PackageError class travels through the service.
func TestSnapshotReadCorrupt(t *testing.T) {
	r := testRuntime(t)
	repo := copyFixture(t, syncFixturePath)
	if _, err := Authoring.Sync(r, repo, SyncOptions{Pull: true, Push: true}); err != nil {
		t.Fatal(err)
	}
	unitJSON := filepath.Join(repo, "exchange", "snapshots", "units", "eka-sync-fixture", "adr-001-runtime-v1", "unit.json")
	data, err := os.ReadFile(unitJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitJSON, append([]byte("X"), data[1:]...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = r.Snapshot.Read(filepath.Join(repo, "exchange", "snapshots"))
	if err == nil {
		t.Fatal("Read must refuse a corrupt snapshot")
	}
	var pe *exchange.PackageError
	if !errors.As(err, &pe) {
		t.Errorf("corrupt snapshot error = %T, want *exchange.PackageError", err)
	}
}

// --- integrity service --------------------------------------------------

// TestIntegrityVerifyClean: a consistent store verifies clean.
func TestIntegrityVerifyClean(t *testing.T) {
	r := testRuntime(t)
	seedKnowledgeWorld(t, r)
	report, err := r.Integrity.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Violations) != 0 {
		t.Errorf("clean store must have no violations, got %+v", report.Violations)
	}
	if report.RefsChecked != 4 || report.PayloadsChecked != 4 {
		t.Errorf("checked counts = %d refs / %d payloads, want 4/4",
			report.RefsChecked, report.PayloadsChecked)
	}
}

// TestIntegrityVerifyTamperedPayload: tampering behind the store's
// back is detected as a payload-hash violation.
func TestIntegrityVerifyTamperedPayload(t *testing.T) {
	r := testRuntime(t)
	seedKnowledgeWorld(t, r)
	if _, err := r.ws.Store().DB().Exec(`UPDATE object_payloads SET content = ? WHERE object_hash IN (SELECT object_hash FROM object_payloads LIMIT 1)`, []byte("tampered")); err != nil {
		t.Fatal(err)
	}
	report, err := r.Integrity.Verify()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range report.Violations {
		if v.Kind == "payload-hash" {
			found = true
		}
	}
	if !found {
		t.Errorf("payload-hash violation missing: %+v", report.Violations)
	}
}

// --- authoring API ------------------------------------------------------

// TestAuthoringValidate: the conformance gate through the Authoring
// API — findings come back in the report, not as errors.
func TestAuthoringValidate(t *testing.T) {
	repo := copyFixture(t, syncFixturePath)
	report, err := Authoring.Validate(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Pass() {
		t.Errorf("the fixture must pass, got %d errors", report.ErrorCount())
	}
	if report.Artifacts != 4 {
		t.Errorf("Artifacts = %d, want 4", report.Artifacts)
	}
}

// TestAuthoringValidateFindings: a non-conformant repository yields a
// failing report without an error.
func TestAuthoringValidateFindings(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "docs", "decisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "---\nnamespace: x\nid: 1\n---\n# bad\n" // type missing: R0 error
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Authoring.Validate(repo)
	if err != nil {
		t.Fatal("a failing repository must return the report, not an error")
	}
	if report.Pass() {
		t.Error("the broken repository must fail the gate")
	}
	if report.ErrorCount() == 0 {
		t.Error("the broken repository must carry blocking errors")
	}
}

// TestAuthoringCompile: Compile produces the CKOs of the repository —
// the package assembly of the compiler, never written to disk.
func TestAuthoringCompile(t *testing.T) {
	repo := copyFixture(t, syncFixturePath)
	result, err := Authoring.Compile(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CKOs) != 4 {
		t.Errorf("CKOs = %d, want 4", len(result.CKOs))
	}
	if !result.Validation.Pass() {
		t.Error("the compile validation gate must pass")
	}
	if result.Package.Integrity.PackageDigest == "" {
		t.Error("the compiled package must carry its digest")
	}
	for _, u := range result.CKOs {
		if u.Digest == "" || len(u.ContentPayload) == 0 {
			t.Errorf("CKO %s must carry its digest and content", u.CanonicalIdentityForm)
		}
	}
}

// TestAuthoringCompileValidationError: a non-conformant repository is
// refused with *ValidationError — and the alias makes errors.As work
// through the Runtime.
func TestAuthoringCompileValidationError(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "docs", "decisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "---\nnamespace: x\nid: 1\n---\n# bad\n"
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Authoring.Compile(repo)
	if err == nil {
		t.Fatal("Compile must refuse a non-conformant repository")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("refusal error = %T, want *runtime.ValidationError (the compile alias)", err)
	}
	if ve.Report == nil || ve.Report.Pass() {
		t.Error("the ValidationError must carry the failing report")
	}
}

// TestAuthoringSyncEndToEnd: the full authoring path — sync the shared
// fixture into a fresh workspace (EKA_HOME temp), then read the seeded
// knowledge back through the Runtime services.
func TestAuthoringSyncEndToEnd(t *testing.T) {
	r := testRuntime(t)
	repo := copyFixture(t, syncFixturePath)

	report, err := Authoring.Sync(r, repo, SyncOptions{Pull: true, Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.NewRepo != true || report.Project != "eka-sync-fixture" || report.Repo != "eka-sync-fixture" {
		t.Errorf("sync report = %+v, want a newly registered repository with the eka.yaml identity eka-sync-fixture", report)
	}
	if report.PullSource != "docs" || report.PulledUnits != 4 || report.PulledAttachments != 1 {
		t.Errorf("pull = %s %d units %d attachments, want docs 4/1",
			report.PullSource, report.PulledUnits, report.PulledAttachments)
	}
	if report.SnapshotLabel == "" || report.SnapshotDigest == "" {
		t.Error("the push must carry the snapshot label and digest")
	}

	// The seeded knowledge is readable through the Runtime services.
	units, err := r.Knowledge.UnitsByProject(report.Project)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 4 {
		t.Errorf("UnitsByProject = %d, want 4", len(units))
	}
	if u, ok, err := r.Knowledge.Object("eka-sync-fixture/adr:001-runtime:1"); err != nil || !ok {
		t.Errorf("Object(adr-001-runtime) = %v, %v; want resolved", ok, err)
	} else if u.Identity.Type != "adr" {
		t.Errorf("Object = %+v, want the adr unit", u)
	}

	// The snapshot written by the push reads back verified.
	pkg, err := r.Snapshot.Read(filepath.Join(repo, "exchange", "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Units) != 4 || len(pkg.Attachments) != 1 {
		t.Error("the pushed snapshot must verify and carry its source entries")
	}

	// Integrity of the seeded store.
	integrity, err := r.Integrity.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if len(integrity.Violations) != 0 {
		t.Errorf("seeded store must verify clean, got %+v", integrity.Violations)
	}

	// Second sync: unchanged (idempotent).
	report2, err := Authoring.Sync(r, repo, SyncOptions{Pull: true, Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report2.Unchanged {
		t.Error("the second sync must report unchanged")
	}
}

// TestAuthoringSyncValidationFailure: a non-conformant repository is
// refused by the docs gate with *ValidationError — through the alias.
// The tree must carry eka.yaml first: without it the ADR-018
// repository-context gate refuses before the docs gate (a plain error,
// not the typed validation class).
func TestAuthoringSyncValidationFailure(t *testing.T) {
	r := testRuntime(t)
	repo := t.TempDir()
	dir := filepath.Join(repo, "docs", "decisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRuntimeEKAFile(t, repo, filepath.Base(repo), filepath.Base(repo), "eka-sync-fixture")
	bad := "---\nnamespace: x\nid: 1\n---\n# bad\n"
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Authoring.Sync(r, repo, SyncOptions{Pull: true, Push: true})
	if err == nil {
		t.Fatal("Sync must refuse a non-conformant repository")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("refusal error = %T, want *runtime.ValidationError", err)
	}
}

// writeRuntimeEKAFile writes an eka.yaml with the given identity triple
// into dir (the ADR-017 identity file, test helper).
func writeRuntimeEKAFile(t *testing.T, dir, project, name, namespace string) {
	t.Helper()
	content := "version: 1\nproject: " + project + "\nname: " + name + "\nnamespace: " + namespace + "\n"
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
