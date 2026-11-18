package exchange

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writeTestEntries returns a small but representative entry set: nested
// directories, a root-level file and an empty payload.
func writeTestEntries() []entry {
	return []entry{
		{name: "integrity.json", data: []byte("{}\n")},
		{name: "manifest.json", data: []byte("{\"format\":\"rsf\"}\n")},
		{name: "units/eka/req-abc-v01/content.md", data: bytes.Repeat([]byte("body\n"), 32)},
		{name: "units/eka/req-abc-v01/meta.json", data: []byte("{\"id\":\"abc\"}\n")},
		{name: "units/eka/req-abc-v01/empty.txt", data: nil},
	}
}

// mustReadFile reads a file and fails the test on error.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	return b
}

// TestWriteZIPFailureLeavesPreviousPackageIntact asserts that a writeZIP
// that cannot publish leaves the previously written container byte-for-byte
// unchanged and removes its temporary sibling. The failure is injected by
// making the destination's parent directory read-only after the first
// successful write, which defeats both the temporary-file creation and any
// rename onto the destination.
func TestWriteZIPFailureLeavesPreviousPackageIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only directory injection does not apply to root")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "out")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("cannot create parent: %v", err)
	}
	path := filepath.Join(parent, "pkg.ekapkg")
	if err := writeZIP(path, writeTestEntries()); err != nil {
		t.Fatalf("first writeZIP failed: %v", err)
	}
	before := mustReadFile(t, path)

	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("cannot make parent read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	err := writeZIP(path, writeTestEntries())
	if err == nil {
		t.Fatal("writeZIP into a read-only parent succeeded, want error")
	}
	after := mustReadFile(t, path)
	if !bytes.Equal(before, after) {
		t.Fatalf("previous package changed after failed write: got %d bytes, want %d", len(after), len(before))
	}

	// No temporary sibling may survive the failed run.
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("cannot restore parent permissions: %v", err)
	}
	assertOnlyDestEntries(t, parent, "pkg.ekapkg")
}

// TestWriteDirFailureLeavesPreviousPackageIntact asserts that a writeDir
// that cannot publish leaves the previously written directory layout
// complete and untouched, with no temporary staging or backup directories
// left beside it.
func TestWriteDirFailureLeavesPreviousPackageIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only directory injection does not apply to root")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "out")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("cannot create parent: %v", err)
	}
	dir := filepath.Join(parent, "pkg")
	if err := writeDir(dir, writeTestEntries()); err != nil {
		t.Fatalf("first writeDir failed: %v", err)
	}
	before := snapshotTree(t, dir)

	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("cannot make parent read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	err := writeDir(dir, writeTestEntries())
	if err == nil {
		t.Fatal("writeDir into a read-only parent succeeded, want error")
	}
	after := snapshotTree(t, dir)
	if len(before) != len(after) {
		t.Fatalf("previous package changed after failed write: got %d files, want %d", len(after), len(before))
	}
	for name, want := range before {
		got, ok := after[name]
		if !ok {
			t.Fatalf("entry %s missing after failed write", name)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("entry %s changed after failed write", name)
		}
	}

	// No staging or backup sibling may survive the failed run.
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("cannot restore parent permissions: %v", err)
	}
	assertOnlyDestEntries(t, parent, "pkg")
}

// TestWriteZIPIsDeterministic asserts that the atomic writer still produces
// byte-identical containers for identical entry sets.
func TestWriteZIPIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.ekapkg")
	b := filepath.Join(dir, "b.ekapkg")
	entries := writeTestEntries()
	if err := writeZIP(a, entries); err != nil {
		t.Fatalf("writeZIP(%s) failed: %v", a, err)
	}
	if err := writeZIP(b, entries); err != nil {
		t.Fatalf("writeZIP(%s) failed: %v", b, err)
	}
	if !bytes.Equal(mustReadFile(t, a), mustReadFile(t, b)) {
		t.Fatal("writeZIP output differs between two runs with identical entries")
	}
}

// TestWriteDirReplacesExistingPackage asserts that publishing over an
// existing directory replaces it wholesale rather than merging with stale
// entries left from the previous layout.
func TestWriteDirReplacesExistingPackage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pkg")
	stale := []entry{
		{name: "manifest.json", data: []byte("old\n")},
		{name: "units/eka/req-old-v01/content.md", data: []byte("stale\n")},
	}
	if err := writeDir(dir, stale); err != nil {
		t.Fatalf("first writeDir failed: %v", err)
	}
	if err := writeDir(dir, writeTestEntries()); err != nil {
		t.Fatalf("second writeDir failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "units", "eka", "req-old-v01")); !os.IsNotExist(err) {
		t.Fatalf("stale entry survived replacement: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(dir, "manifest.json")); !bytes.Equal(got, []byte("{\"format\":\"rsf\"}\n")) {
		t.Fatalf("manifest.json not replaced: %q", got)
	}
	assertOnlyDestEntries(t, filepath.Dir(dir), "pkg")
}

// snapshotTree returns entry path -> content for every regular file below
// root (paths relative to root, slash separated).
func snapshotTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = mustReadFile(t, path)
		return nil
	})
	if err != nil {
		t.Fatalf("cannot snapshot %s: %v", root, err)
	}
	return out
}

// assertOnlyDestEntries fails when parent holds anything besides want: it
// catches temporary staging, backup and partially published leftovers.
func assertOnlyDestEntries(t *testing.T, parent, want string) {
	t.Helper()
	names, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("cannot list %s: %v", parent, err)
	}
	for _, n := range names {
		if n.Name() != want {
			t.Fatalf("unexpected leftover %q in %s", n.Name(), parent)
		}
	}
}
