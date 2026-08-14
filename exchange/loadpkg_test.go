package exchange

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadPackageRoundtrip: a package produced by Export loads back
// into an equivalent model via LoadPackage — same header, manifest,
// units (with payloads), attachments (with data), declarations and
// integrity.
func TestLoadPackageRoundtrip(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pkg.ekapkg")
	mustExport(t, fixtureValid, nil, out)

	pkg, err := LoadPackage(out)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if pkg.Header.PackageIdentityLabel != "rsf-repo-eka-valid-fixture-2" {
		t.Errorf("label = %q", pkg.Header.PackageIdentityLabel)
	}
	if pkg.Header.ExportScope != ScopeRepository {
		t.Errorf("scope = %q, want repo", pkg.Header.ExportScope)
	}
	if pkg.Header.SerializationVersion != SerializationVersion ||
		pkg.Header.ExchangeFormatVersion != ExchangeFormatVersion ||
		pkg.Header.SpecificationVersion != SpecificationVersion {
		t.Errorf("header versions = %+v", pkg.Header)
	}
	if len(pkg.Units) != 6 {
		t.Fatalf("units = %d, want 6", len(pkg.Units))
	}
	// Units are sorted by canonical identity form and carry payloads.
	for i, u := range pkg.Units {
		if u.CanonicalIdentityForm != u.Identity.CanonicalForm() {
			t.Errorf("unit %d: canonical form %q != identity %s", i, u.CanonicalIdentityForm, u.Identity.CanonicalForm())
		}
		if u.ContentPayload == nil {
			t.Errorf("unit %s: content payload missing", u.CanonicalIdentityForm)
		}
		if u.Digest == "" {
			t.Errorf("unit %s: digest missing", u.CanonicalIdentityForm)
		}
		if i > 0 && pkg.Units[i-1].CanonicalIdentityForm >= u.CanonicalIdentityForm {
			t.Errorf("units not strictly sorted at %d", i)
		}
	}
	if len(pkg.Attachments) != 1 || pkg.Attachments[0].ID != "docs/architecture/diagram.txt" {
		t.Errorf("attachments = %+v", pkg.Attachments)
	}
	if pkg.Attachments[0].Data == nil {
		t.Error("attachment data missing")
	}
	if pkg.Manifest.PackageDigest != pkg.Integrity.PackageDigest {
		t.Errorf("manifest digest %q != integrity digest %q", pkg.Manifest.PackageDigest, pkg.Integrity.PackageDigest)
	}
	if len(pkg.Declarations.ExternalReferences) != 0 {
		t.Errorf("externals = %+v, want none", pkg.Declarations.ExternalReferences)
	}
}

// TestLoadPackageDirectoryLayout: the directory layout loads
// identically to the ZIP container.
func TestLoadPackageDirectoryLayout(t *testing.T) {
	out := filepath.Join(t.TempDir(), "layout")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	mustExport(t, fixtureValid, nil, out)
	pkg, err := LoadPackage(out)
	if err != nil {
		t.Fatalf("LoadPackage(dir): %v", err)
	}
	if len(pkg.Units) != 6 {
		t.Errorf("units = %d, want 6", len(pkg.Units))
	}
}

// TestLoadPackageRejectsTamperedDigest: flipping one byte of a unit
// content file breaks the package digest — LoadPackage must refuse.
func TestLoadPackageRejectsTamperedDigest(t *testing.T) {
	out := filepath.Join(t.TempDir(), "layout")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	mustExport(t, fixtureValid, nil, out)

	// Corrupt a unit content file.
	unit := filepath.Join(out, "units", "eka-valid-fixture", "adr-001-exchange-v1", "content")
	data, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte("X"), data[1:]...)
	if err := os.WriteFile(unit, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadPackage(out)
	if err == nil {
		t.Fatal("tampered package must be refused")
	}
	if !LoadPackageError(err) {
		t.Errorf("error must be a *PackageError, got %T", err)
	}
}

// TestLoadPackageRejectsUnknownEntry: an extra entry maps to no logical
// element (RSF §9.5 reject-by-default).
func TestLoadPackageRejectsUnknownEntry(t *testing.T) {
	out := filepath.Join(t.TempDir(), "layout")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	mustExport(t, fixtureValid, nil, out)
	if err := os.WriteFile(filepath.Join(out, "sneaky.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPackage(out); err == nil {
		t.Error("package with an unknown entry must be refused")
	}
}

// TestLoadPackageMissingPath: an unreadable package path is a
// deterministic error.
func TestLoadPackageMissingPath(t *testing.T) {
	if _, err := LoadPackage(filepath.Join(t.TempDir(), "nope.ekapkg")); err == nil {
		t.Error("missing package must error")
	}
}
