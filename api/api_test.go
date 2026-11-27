package api_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/maleolabs/eka-core/v2/api"
)

// syncFixture is the shared conformant sync fixture (4 units + 1
// attachment), the same tree the runtime and sync package tests seed
// from. The api test consumes it through the public contract only.
const syncFixture = "../sync/testdata/valid"

// TestRuntimeContract exercises the public api.Runtime end to end as a
// consumer would: obtain the runtime, read an empty workspace, sync a
// repository (the write/register/persist path), then read the persisted
// knowledge, resolve an identity and verify integrity — every call
// through the api interfaces, none through runtime/ or store/.
func TestRuntimeContract(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())

	rt, err := api.Ensure()
	if err != nil {
		t.Fatalf("api.Ensure: %v", err)
	}
	defer rt.Close()

	if !rt.Exists() {
		t.Fatal("Exists() = false after Ensure")
	}
	if rt.Path() == "" {
		t.Fatal("Path() is empty after Ensure")
	}

	// Empty workspace: no projects yet.
	projects, err := rt.Workspace().Projects()
	if err != nil {
		t.Fatalf("Workspace().Projects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("Projects() = %d, want 0 on a fresh workspace", len(projects))
	}

	// Write path: sync the fixture (auto-registers the repository and
	// persists its knowledge).
	repo := copyFixture(t, syncFixture)
	if _, err := rt.Authoring().Sync(repo, api.SyncOptions{Pull: true, Push: true}); err != nil {
		t.Fatalf("Authoring().Sync: %v", err)
	}

	// Read path: the project is registered and the status aggregates
	// the persisted totals.
	projects, err = rt.Workspace().Projects()
	if err != nil {
		t.Fatalf("Workspace().Projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("Projects() = %d, want 1 after sync", len(projects))
	}
	st, err := rt.Workspace().Status()
	if err != nil {
		t.Fatalf("Workspace().Status: %v", err)
	}
	if st.Objects != 4 || st.Payloads != 4 || st.Attachments != 1 {
		t.Errorf("Status totals = %d/%d/%d, want 4/4/1", st.Objects, st.Payloads, st.Attachments)
	}

	// Knowledge read: the 4 fixture units.
	units, err := rt.Knowledge().UnitsByProject(projects[0].ID)
	if err != nil {
		t.Fatalf("Knowledge().UnitsByProject: %v", err)
	}
	if len(units) != 4 {
		t.Fatalf("UnitsByProject = %d, want 4", len(units))
	}

	// Resolver read (single object and line).
	u, ok, err := rt.Resolver().Resolve(units[0].CanonicalIdentityForm)
	if err != nil || !ok {
		t.Fatalf("Resolver().Resolve(%q) = %v, %v", units[0].CanonicalIdentityForm, ok, err)
	}
	if u == nil || u.Identity.ID != units[0].Identity.ID {
		t.Fatalf("Resolver().Resolve returned the wrong unit: %+v", u)
	}
	line, err := rt.Resolver().ResolveLine(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
	if err != nil || len(line) == 0 {
		t.Fatalf("Resolver().ResolveLine = %d units, %v", len(line), err)
	}

	// Authoring read: the draft backlog is empty after a push.
	drafts, err := rt.Authoring().Drafts(projects[0].ID)
	if err != nil {
		t.Fatalf("Authoring().Drafts: %v", err)
	}
	if len(drafts) != 0 {
		t.Errorf("Drafts() = %d, want 0", len(drafts))
	}

	// Authoring validate: the fixture passes the gate.
	report, err := rt.Authoring().Validate(repo)
	if err != nil {
		t.Fatalf("Authoring().Validate: %v", err)
	}
	if report.FilesScanned == 0 {
		t.Error("Validate scanned no files")
	}

	// Integrity read: a freshly synced store has no violations.
	ir, err := rt.Integrity().Verify()
	if err != nil {
		t.Fatalf("Integrity().Verify: %v", err)
	}
	if len(ir.Violations) != 0 || ir.RefsChecked != 4 {
		t.Errorf("Integrity report = %+v, want 4 refs and no violations", ir)
	}
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
