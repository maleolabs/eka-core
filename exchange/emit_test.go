package exchange

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestEmitRoundtrip: Emit(pkg) produces entries that LoadPackage
// accepts and reconstructs as the same model.
func TestEmitRoundtrip(t *testing.T) {
	// Assemble the package via RepositoryPackage (the model the runtime
	// uses), then emit it to a temp directory and load it back.
	pkg, err := RepositoryPackage(fixtureValid)
	if err != nil {
		t.Fatalf("RepositoryPackage: %v", err)
	}
	files, err := Emit(pkg)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	dir := t.TempDir()
	for _, f := range files {
		path := filepath.Join(dir, f.Name)
		if err := writeFileAll(path, f.Data); err != nil {
			t.Fatalf("cannot write %s: %v", f.Name, err)
		}
	}

	loaded, err := LoadPackage(dir)
	if err != nil {
		t.Fatalf("LoadPackage after Emit: %v", err)
	}
	if loaded.Header.PackageIdentityLabel != pkg.Header.PackageIdentityLabel {
		t.Errorf("label = %q, want %q", loaded.Header.PackageIdentityLabel, pkg.Header.PackageIdentityLabel)
	}
	if len(loaded.Units) != len(pkg.Units) {
		t.Fatalf("units = %d, want %d", len(loaded.Units), len(pkg.Units))
	}
	for i := range pkg.Units {
		a, b := pkg.Units[i], loaded.Units[i]
		if a.CanonicalIdentityForm != b.CanonicalIdentityForm {
			t.Errorf("unit %d form mismatch: %s vs %s", i, a.CanonicalIdentityForm, b.CanonicalIdentityForm)
		}
		if !bytes.Equal(a.ContentPayload, b.ContentPayload) {
			t.Errorf("unit %s content payload differs after roundtrip", a.CanonicalIdentityForm)
		}
	}
	if len(loaded.Attachments) != len(pkg.Attachments) {
		t.Errorf("attachments = %d, want %d", len(loaded.Attachments), len(pkg.Attachments))
	}
}

// TestEmitDeterminism: two Emits of an identical package produce
// byte-identical entries.
func TestEmitDeterminism(t *testing.T) {
	pkg, err := RepositoryPackage(fixtureValid)
	if err != nil {
		t.Fatalf("RepositoryPackage: %v", err)
	}
	one, err := Emit(pkg)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Emit(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != len(two) {
		t.Fatalf("entry counts differ: %d vs %d", len(one), len(two))
	}
	for i := range one {
		if one[i].Name != two[i].Name || !bytes.Equal(one[i].Data, two[i].Data) {
			t.Errorf("entry %d differs: %q vs %q", i, one[i].Name, two[i].Name)
		}
	}
	// Entries must be sorted by name (deterministic projection).
	for i := 1; i < len(one); i++ {
		if one[i-1].Name >= one[i].Name {
			t.Errorf("entries not sorted at %d: %q >= %q", i, one[i-1].Name, one[i].Name)
		}
	}
}

// TestEmitEmptyExternalsNormalized: a package with nil external
// references must still emit (the serializer needs a non-nil slice).
func TestEmitEmptyExternalsNormalized(t *testing.T) {
	pkg, err := RepositoryPackage(fixtureValid)
	if err != nil {
		t.Fatalf("RepositoryPackage: %v", err)
	}
	// Simulate a model whose declarations carry no externals.
	pkg.Declarations.ExternalReferences = nil
	files, err := Emit(pkg)
	if err != nil {
		t.Fatalf("Emit with nil externals: %v", err)
	}
	// declarations.json must encode the externals as [], never null.
	for _, f := range files {
		if f.Name == "declarations.json" {
			if bytes.Contains(f.Data, []byte("null")) {
				t.Errorf("declarations.json must not contain null: %s", f.Data)
			}
		}
	}
}

// TestEmitMatchesExportBytes: Emit(RepositoryPackage(root)) is
// byte-identical to a real repository-scope export of the same root.
func TestEmitMatchesExportBytes(t *testing.T) {
	pkg, err := RepositoryPackage(fixtureValid)
	if err != nil {
		t.Fatalf("RepositoryPackage: %v", err)
	}
	emitted, err := Emit(pkg)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string][]byte{}
	for _, f := range emitted {
		byName[f.Name] = f.Data
	}

	out := filepath.Join(t.TempDir(), "layout")
	if err := mkdirAll(out); err != nil {
		t.Fatal(err)
	}
	mustExport(t, fixtureValid, nil, out)
	entries, _ := readDirEntries(t, out)
	for name, data := range entries {
		got, ok := byName[name]
		if !ok {
			t.Errorf("exported entry %s missing from Emit", name)
			continue
		}
		if !bytes.Equal(got, data) {
			t.Errorf("entry %s differs between Emit and Export", name)
		}
	}
	if len(byName) != len(entries) {
		t.Errorf("entry count differs: Emit %d, Export %d", len(byName), len(entries))
	}
}

// TestRepositoryPackageMatchesExport: the model assembled by
// RepositoryPackage mirrors Export's package (label, counts, digest).
func TestRepositoryPackageMatchesExport(t *testing.T) {
	pkg, err := RepositoryPackage(fixtureValid)
	if err != nil {
		t.Fatalf("RepositoryPackage: %v", err)
	}
	res := mustExport(t, fixtureValid, nil, filepath.Join(t.TempDir(), "p.ekapkg"))
	if pkg.Header.PackageIdentityLabel != res.Label {
		t.Errorf("label = %q, want %q", pkg.Header.PackageIdentityLabel, res.Label)
	}
	if pkg.Integrity.PackageDigest != res.Package.Integrity.PackageDigest {
		t.Errorf("digest = %q, want %q", pkg.Integrity.PackageDigest, res.Package.Integrity.PackageDigest)
	}
	if len(pkg.Units) != res.Units || len(pkg.Attachments) != res.Attachments {
		t.Errorf("counts = %d/%d, want %d/%d", len(pkg.Units), len(pkg.Attachments), res.Units, res.Attachments)
	}
}

// writeFileAll writes one emitted entry, creating parent directories.
func writeFileAll(path string, data []byte) error {
	if err := mkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return osWriteFile(path, data, 0o644)
}

// mkdirAll wraps os.MkdirAll for test helpers.
func mkdirAll(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// osWriteFile wraps os.WriteFile for test helpers.
func osWriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

// readDirEntries walks dir and returns entry name -> bytes (forward
// slashes), mirroring the package reader's directory layout.
func readDirEntries(t *testing.T, dir string) (map[string][]byte, error) {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	return out, err
}
