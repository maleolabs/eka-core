package exchange

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
)

// This file tests the legacy RSF import path (spec-standard-v2 §4):
// packages declaring Serialization Version "1.1" (and the pre-1.1 line
// "1") carry the legacy kebab/snake JSON keys and must import cleanly
// through the key-rename pass, then re-emit as v2.0.

// v2ToV11KeyMap is the inverse of the importer's rename maps: the
// v2.0 -> v1.1 key spelling, used by the fixture builder to fabricate
// legacy packages from v2.0 exports.
var v2ToV11KeyMap = map[string]string{
	"serializationVersion":  "serialization_version",
	"exchangeFormatVersion": "exchange_format_version",
	"specificationVersion":  "specification_version",
	"packageIdentityLabel":  "package_identity_label",
	"exportScope":           "export_scope",
	"packageDigest":         "package_digest",
	"canonicalIdentityForm": "canonical_identity_form",
	"instanceVersion":       "instance_version",
	"contentRepresentation": "content_representation",
	"contentFile":           "content_file",
	"unitDigest":            "unit_digest",
	"externalReferences":    "external_references",
	"stateVector":           "state_vector",
	"changeLog":             "change_log",
	"contentState":          "content-state",
	"executionState":        "execution-state",
	"planningState":         "planning-state",
	"containerState":        "container-state",
	"existenceState":        "existence-state",
	"dimensionsSecondary":   "dimensions_secondary",
}

// legacyV11Package rewrites a v2.0 package directory into the v1.1 key
// spelling, declares the given serialization version ("1.1" or "1") in
// header + manifest + label, and recomputes every digest over the
// rewritten bytes (the fixtures are self-consistent packages, exactly
// like a historical legacy export). Returns the new directory.
func legacyV11Package(t *testing.T, src, version string) string {
	t.Helper()
	dir := t.TempDir()
	entries := map[string][]byte{}
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
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
		entries[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Rewrite every JSON entry to the v1.1 key spelling.
	for name, data := range entries {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		renamed, err := renameJSONKeys(data, v2ToV11KeyMap)
		if err != nil {
			t.Fatalf("rewrite %s: %v", name, err)
		}
		entries[name] = renamed
	}

	// 2. Recompute the per-unit digests over the rewritten bytes.
	unitDigests := map[string]string{}
	for name, data := range entries {
		if !strings.HasSuffix(name, "/unit.json") {
			continue
		}
		var u struct {
			CanonicalIdentityForm string `json:"canonical_identity_form"`
		}
		if err := json.Unmarshal(data, &u); err != nil {
			t.Fatal(err)
		}
		content := entries[strings.TrimSuffix(name, "unit.json")+"content"]
		sum := sha256.Sum256(append(data, content...))
		unitDigests[u.CanonicalIdentityForm] = hex.EncodeToString(sum[:])
	}

	// 3. Update the manifest and integrity blocks.
	writeDigests := func(name string, update func(m map[string]any)) {
		var m map[string]any
		if err := json.Unmarshal(entries[name], &m); err != nil {
			t.Fatal(err)
		}
		update(m)
		entries[name], err = json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
	}
	writeDigests("manifest.json", func(m map[string]any) {
		for _, u := range m["units"].([]any) {
			um := u.(map[string]any)
			um["unit_digest"] = unitDigests[um["canonical_identity_form"].(string)]
		}
	})
	writeDigests("integrity.json", func(m map[string]any) {
		for _, u := range m["units"].([]any) {
			um := u.(map[string]any)
			um["digest"] = unitDigests[um["canonical_identity_form"].(string)]
		}
	})

	// 4. Package digest over every entry except manifest/integrity.
	packageDigestOf := func() string {
		var buf []byte
		var names []string
		for name := range entries {
			if name == "manifest.json" || name == "integrity.json" {
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			buf = append(buf, entries[name]...)
		}
		sum := sha256.Sum256(buf)
		return hex.EncodeToString(sum[:])
	}
	writeDigests("manifest.json", func(m map[string]any) { m["package_digest"] = packageDigestOf() })
	writeDigests("integrity.json", func(m map[string]any) { m["package_digest"] = packageDigestOf() })

	// 5. Declare the legacy serialization version (header + manifest +
	// label; the label carries the version suffix).
	var hdr map[string]any
	if err := json.Unmarshal(entries["header.json"], &hdr); err != nil {
		t.Fatal(err)
	}
	hdr["serialization_version"] = version
	label := hdr["package_identity_label"].(string)
	parts := strings.Split(label, "-")
	parts[len(parts)-1] = version
	hdr["package_identity_label"] = strings.Join(parts, "-")
	entries["header.json"], err = json.Marshal(hdr)
	if err != nil {
		t.Fatal(err)
	}
	writeDigests("manifest.json", func(m map[string]any) {
		m["serialization_version"] = version
		m["package_identity_label"] = strings.Join(parts, "-")
	})
	// The header is part of the package digest input: recompute.
	writeDigests("manifest.json", func(m map[string]any) { m["package_digest"] = packageDigestOf() })
	writeDigests("integrity.json", func(m map[string]any) { m["package_digest"] = packageDigestOf() })

	for name, data := range entries {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestImportLegacyV11Package: a v1.1 package (kebab/snake keys) imports
// cleanly through the legacy key-rename pass and re-exports as v2.0.
func TestImportLegacyV11Package(t *testing.T) {
	src := assembleTestPackage(t, testPackageSpec{
		scope:     ScopeRepository,
		namespace: "ns-one",
		units:     []*Unit{specUnit("ns-one", "001", 1, "approved", nil)},
	})
	dir := legacyV11Package(t, src, LegacySerializationVersion)

	repo := newTestRepo(t)
	res, err := Import(dir, ImportOptions{Root: repo})
	if err != nil {
		t.Fatalf("legacy v1.1 import failed: %v", err)
	}
	if len(res.ImportedArtifacts) != 1 || res.ImportedArtifacts[0] != "ns-one/spec:001:1" {
		t.Errorf("imported = %v", res.ImportedArtifacts)
	}

	// The imported repository re-exports as v2.0 (serialization "2").
	out := filepath.Join(t.TempDir(), "re-export.ekapkg")
	mustExport(t, repo, nil, out)
	pkg, err := LoadPackage(out)
	if err != nil {
		t.Fatalf("re-exported package: %v", err)
	}
	if pkg.Header.SerializationVersion != SerializationVersion {
		t.Errorf("re-export serialization version = %q, want %q", pkg.Header.SerializationVersion, SerializationVersion)
	}
	if len(pkg.Units) != 1 || pkg.Units[0].ContentPayload == nil {
		t.Errorf("re-exported units = %d", len(pkg.Units))
	}
}

// TestImportLegacyV1Package: the pre-1.1 line ("1") imports through the
// same legacy key map.
func TestImportLegacyV1Package(t *testing.T) {
	src := assembleTestPackage(t, testPackageSpec{
		scope:     ScopeRepository,
		namespace: "ns-one",
		units:     []*Unit{specUnit("ns-one", "002", 1, "approved", nil)},
	})
	dir := legacyV11Package(t, src, LegacySerializationVersionV1)

	repo := newTestRepo(t)
	res, err := Import(dir, ImportOptions{Root: repo})
	if err != nil {
		t.Fatalf("legacy v1 import failed: %v", err)
	}
	if len(res.ImportedArtifacts) != 1 {
		t.Errorf("imported = %v", res.ImportedArtifacts)
	}
}

// TestLegacyKeyRenameRoundTrip: the rename maps are exact inverses — a
// v2.0 entry rewritten to v1.1 keys and back decodes to the same values
// (every serialized field of model.go is covered by the maps).
func TestLegacyKeyRenameRoundTrip(t *testing.T) {
	u := specUnit("ns-one", "003", 1, "approved", []Relationship{{Type: "depends-on", Target: "ns-two/spec:004:1"}})
	u.StateVector = StateVector{ContentState: "approved", ExistenceState: "active", ExecutionState: "done"}
	u.ChangeLog = []ChangeLogEntry{
		{Date: "2026-08-05", Domain: "content-state", From: "-", To: "approved", By: conformance.User("T")},
	}
	u.Classification = Classification{Dimension: "specifications", DimensionsSecondary: []string{"architecture"}, Domain: "backend"}
	data, err := marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	v11, err := renameJSONKeys(data, v2ToV11KeyMap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(v11), "contentState") || strings.Contains(string(v11), "stateVector") {
		t.Fatalf("v1.1 rewrite kept v2 keys: %s", v11)
	}
	back, err := renameJSONKeys(v11, legacyUnitKeys)
	if err != nil {
		t.Fatal(err)
	}
	var got Unit
	if err := strictDecode("unit.json", back, &got); err != nil {
		t.Fatalf("round-trip decode failed: %v", err)
	}
	if got.CanonicalIdentityForm != u.CanonicalIdentityForm || got.StateVector != u.StateVector ||
		got.Classification.DimensionsSecondary[0] != "architecture" || got.ChangeLog[0].Domain != "content-state" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}
