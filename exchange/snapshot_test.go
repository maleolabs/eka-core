package exchange

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
)

// writeSourceSnapshot writes a source-only snapshot directory from a
// fully assembled package (EmitSource output) and returns its path.
func writeSourceSnapshot(t *testing.T, pkg *Package) string {
	t.Helper()
	dir := t.TempDir()
	files, fp, err := EmitSource(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if fp == "" {
		t.Fatal("EmitSource must return a source fingerprint")
	}
	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The derived aggregates must not be emitted.
	for _, gone := range []string{"manifest.json", "declarations.json", "integrity.json"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("EmitSource must not emit the aggregate %s", gone)
		}
	}
	return dir
}

// snapshotFixturePackage builds a minimal fully assembled package (two
// units, one attachment) for the snapshot tests.
func snapshotFixturePackage(t *testing.T) *Package {
	t.Helper()
	units := []*Unit{
		{
			Identity:              Identity{Namespace: "eka-snap", Type: "adr", ID: "one", InstanceVersion: 1},
			CanonicalIdentityForm: "eka-snap/adr:one:1",
			Revision:              1,
			StateVector:           StateVector{ContentState: "accepted"},
			ChangeLog:             []ChangeLogEntry{{Date: "2026-08-14", Domain: "content-state", From: "proposed", To: "accepted", By: conformance.AuthorIdentity{Name: "dev"}}},
			Relationships:         []Relationship{},
			Classification:        Classification{},
			Content:               ContentRef{Representation: StructuredJSON, File: "content"},
			ContentPayload:        []byte(`{"content":"one"}`),
		},
		{
			Identity:              Identity{Namespace: "eka-snap", Type: "adr", ID: "two", InstanceVersion: 1},
			CanonicalIdentityForm: "eka-snap/adr:two:1",
			Revision:              1,
			StateVector:           StateVector{ContentState: "accepted"},
			ChangeLog:             []ChangeLogEntry{{Date: "2026-08-14", Domain: "content-state", From: "proposed", To: "accepted", By: conformance.AuthorIdentity{Name: "dev"}}},
			Relationships:         []Relationship{},
			Classification:        Classification{},
			Content:               ContentRef{Representation: StructuredJSON, File: "content"},
			ContentPayload:        []byte(`{"content":"two"}`),
		},
	}
	return &Package{
		Header: Header{
			SerializationVersion:  SerializationVersion,
			ExchangeFormatVersion: ExchangeFormatVersion,
			SpecificationVersion:  SpecificationVersion,
			Exporter:              Exporter,
			PackageIdentityLabel:  "rsf-repo-eka-snap-2",
			ExportScope:           ScopeRepository,
			Namespace:             "eka-snap",
		},
		Units: units,
		Declarations: Declarations{
			Closure:            ClosureDeclaration{Scope: ScopeRepository, Seeds: []string{}},
			ExternalReferences: []ExternalReference{},
			Extensions:         []ExtensionDecl{},
		},
		Attachments: []*Attachment{{ID: "docs/diagram.txt", Data: []byte("diagram")}},
	}
}

// TestLoadSnapshotSourceMode: a source-only snapshot verifies
// structurally, reports the source fingerprint, and decodes every unit
// with its per-unit digest filled (recomputed over unit.json ||
// content).
func TestLoadSnapshotSourceMode(t *testing.T) {
	dir := writeSourceSnapshot(t, snapshotFixturePackage(t))
	res, err := LoadSnapshot(dir)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if res.Mode != SnapshotSource {
		t.Errorf("mode = %v, want SnapshotSource", res.Mode)
	}
	if len(res.Package.Units) != 2 || len(res.Package.Attachments) != 1 {
		t.Errorf("units/attachments = %d/%d, want 2/1", len(res.Package.Units), len(res.Package.Attachments))
	}
	for _, u := range res.Package.Units {
		if u.Digest == "" {
			t.Errorf("unit %s must carry its per-unit digest", u.CanonicalIdentityForm)
		}
	}
	// The fingerprint is deterministic: a second load reproduces it.
	res2, err := LoadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != res2.Digest {
		t.Error("fingerprint must be deterministic across loads")
	}
	// The fingerprint differs when a unit changes.
	unitJSON := filepath.Join(dir, "units", "eka-snap", "adr-one-v1", "unit.json")
	data, err := os.ReadFile(unitJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitJSON, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	res3, err := LoadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res3.Digest == res.Digest {
		t.Error("fingerprint must change with the source entries")
	}
}

// TestLoadSnapshotLegacyMode: a snapshot carrying the aggregates is
// verified byte-exact and reports the package digest.
func TestLoadSnapshotLegacyMode(t *testing.T) {
	dir := writeSourceSnapshot(t, snapshotFixturePackage(t))
	// Upgrade to legacy: emit the aggregates from the same package.
	files, err := Emit(snapshotFixturePackage(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name == "manifest.json" || f.Name == "declarations.json" || f.Name == "integrity.json" {
			if err := os.WriteFile(filepath.Join(dir, f.Name), f.Data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	res, err := LoadSnapshot(dir)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if res.Mode != SnapshotLegacy {
		t.Errorf("mode = %v, want SnapshotLegacy", res.Mode)
	}
	if res.Digest == "" {
		t.Error("legacy mode must report the package digest")
	}
}

// TestLoadSnapshotPartialAggregatesRefused: exactly one aggregate
// present is an ambiguous state — refused deterministically.
func TestLoadSnapshotPartialAggregatesRefused(t *testing.T) {
	dir := writeSourceSnapshot(t, snapshotFixturePackage(t))
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(dir); err == nil {
		t.Fatal("partial aggregate state must be refused")
	}
}

// TestLoadSnapshotStructuralRefusals: unit entry in the wrong directory,
// missing content, and unknown entries under units/ are refused.
func TestLoadSnapshotStructuralRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, dir string)
	}{
		{"wrong unit directory", func(t *testing.T, dir string) {
			from := filepath.Join(dir, "units", "eka-snap", "adr-one-v1", "unit.json")
			to := filepath.Join(dir, "units", "eka-snap", "adr-one-v2", "unit.json")
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(from, to); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing content", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "units", "eka-snap", "adr-one-v1", "content")); err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown entry under units", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "units", "stray.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeSourceSnapshot(t, snapshotFixturePackage(t))
			tc.mutate(t, dir)
			if _, err := LoadSnapshot(dir); err == nil {
				t.Fatal("structurally broken snapshot must be refused")
			}
		})
	}
}

// TestLoadSnapshotMissingHeader: without header.json there is nothing to
// verify against.
func TestLoadSnapshotMissingHeader(t *testing.T) {
	dir := writeSourceSnapshot(t, snapshotFixturePackage(t))
	if err := os.Remove(filepath.Join(dir, "header.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(dir); err == nil {
		t.Fatal("snapshot without header.json must be refused")
	}
}

// TestSnapshotFingerprintDeterministic: identical entry sets produce
// identical fingerprints; aggregate entries are excluded.
func TestSnapshotFingerprintDeterministic(t *testing.T) {
	base := map[string][]byte{
		"header.json":       []byte(`{"a":1}`),
		"units/x/unit.json": []byte(`{"b":2}`),
		"attachments/a.txt": []byte("data"),
		"manifest.json":     []byte("ignored"),
		"integrity.json":    []byte("ignored"),
		"declarations.json": []byte("ignored"),
	}
	fp1, err := SnapshotFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := SnapshotFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Error("fingerprint must be deterministic")
	}
	// Adding an aggregate entry must not change the fingerprint.
	base["manifest.json"] = []byte("changed")
	fp3, err := SnapshotFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if fp3 != fp1 {
		t.Error("aggregate entries must be excluded from the fingerprint")
	}
	// Changing a source entry must change the fingerprint.
	base["units/x/unit.json"] = []byte(`{"b":3}`)
	fp4, err := SnapshotFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if fp4 == fp1 {
		t.Error("source changes must change the fingerprint")
	}
}
