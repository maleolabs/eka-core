package exchange

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/metadata"
)

// import_test.go covers the import pipeline (Exchange §11, RSF §13.2):
// integrity verification, ordered phases 1-10, conservative merge,
// round-trip, rollback, draft tolerance, deterministic behavior.
//
// Two package fabrication helpers are used:
//
//   - assembleTestPackage builds a package from scratch (testPackageSpec)
//     with correct digests, mirroring serialize.go's assemble(); used for
//     package-side validation scenarios (undeclared externals, draft
//     tolerance, cross-namespace v1 limitation, unknown fields);
//   - mutatePackage rewrites a real exported package (mutate the entry
//     map, re-digest) for corruption scenarios (versions, integrity,
//     declarations).
//
// A fresh target repository is either bootstrapped (bootstrap.Run — the
// round-trip test uses the real skeleton) or a plain docs/ directory (the
// minimal EKA repository shape required by the pre-gate).

// newTestRepo creates a minimal EKA target repository: a docs/ directory.
func newTestRepo(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// copyFixtureRepo copies the whole valid fixture repository (artifacts,
// attachments, convention docs) into a fresh target repository.
func copyFixtureRepo(t *testing.T) string {
	t.Helper()
	root := newTestRepo(t)
	err := filepath.WalkDir(fixtureValid, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(fixtureValid, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// initTestRepo prepares a valid import target with an eka.yaml identity
// (identity-only, the equivalent of `eka init`) plus the docs/ knowledge
// tree the import gate requires (the legacy authoring adapter). The
// identity is written directly via the metadata package — the CLI's
// bootstrap engine lives in a sibling module and must not be a test
// dependency of the core library.
func initTestRepo(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "init")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	id := metadata.Metadata{Version: 1, Project: "init", Name: "init", Namespace: "init"}
	if err := os.WriteFile(filepath.Join(root, "eka.yaml"), id.Marshal(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// exportPackage exports the repository at root (repository scope) to a
// temp .ekapkg and returns its path.
func exportPackage(t *testing.T, root string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "pkg.ekapkg")
	mustExport(t, root, nil, out)
	return out
}

// mustImport runs Import and fails the test on error.
func mustImport(t *testing.T, pkgPath string, opts ImportOptions) *ImportResult {
	t.Helper()
	res, err := Import(pkgPath, opts)
	if err != nil {
		t.Fatalf("Import(%s) failed: %v", pkgPath, err)
	}
	return res
}

// snapshotRepo walks a repository and returns relative path -> content.
func snapshotRepo(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
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
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertRepoUnchanged verifies the repository equals the snapshot.
func assertRepoUnchanged(t *testing.T, root string, before map[string][]byte) {
	t.Helper()
	after := snapshotRepo(t, root)
	if len(after) != len(before) {
		t.Fatalf("repository changed: %d files before, %d after", len(before), len(after))
	}
	for path, data := range before {
		if got, ok := after[path]; !ok || !bytes.Equal(got, data) {
			t.Errorf("repository changed at %s", path)
		}
	}
}

// ---------------------------------------------------------------------------
// Package fabrication helpers
// ---------------------------------------------------------------------------

// testPackageSpec describes a hand-built package.
type testPackageSpec struct {
	scope     ScopeKind
	namespace string
	units     []*Unit
	atts      []*Attachment
	externals []ExternalReference
	// corruptDigest writes a wrong package digest into integrity.json.
	corruptDigest bool
	// dropManifestUnit drops the last manifest unit entry (the unit
	// entries stay on disk: manifest <-> units 1:1 violation).
	dropManifestUnit bool
	// malformedManifest writes invalid JSON into manifest.json.
	malformedManifest bool
	// headerOverrides mutate the header before serialization (digest is
	// recomputed afterwards: phase 1 is exercised, not integrity).
	headerOverrides func(*Header)
	// extraHeaderField appends an unknown field to header.json.
	extraHeaderField bool
}

// specUnit builds a valid spec (spec-) unit with the given state.
func specUnit(ns, id string, iv int, state string, rels []Relationship) *Unit {
	u := &Unit{
		Identity: Identity{Namespace: ns, Type: "spec", ID: id, InstanceVersion: iv},
		Revision: 1, Author: conformance.User("Test Author"), Created: "2026-08-05", Updated: "2026-08-05",
		StateVector:    StateVector{ContentState: state, ExistenceState: "active"},
		Classification: Classification{Dimension: "specifications"},
		Content:        ContentRef{Representation: ContentRepresentation, File: "content"},
		ContentPayload: []byte("\n# Spec\n\n## Purpose\n\nP\n\n## Content\n\nC\n"),
		ChangeLog: []ChangeLogEntry{
			{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Test Author")},
			{Date: "2026-08-05", Domain: "content-state", From: "-", To: state, By: conformance.User("Test Author")},
		},
		Relationships: rels,
	}
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()
	return u
}

// adrUnit builds a valid adr- unit with the given state and relationships.
func adrUnit(ns, id string, iv int, state string, rels []Relationship) *Unit {
	u := &Unit{
		Identity: Identity{Namespace: ns, Type: "adr", ID: id, InstanceVersion: iv},
		Revision: 1, Author: conformance.User("Test Author"), Created: "2026-08-05", Updated: "2026-08-05",
		StateVector:    StateVector{ContentState: state, ExistenceState: "active"},
		Classification: Classification{Dimension: "decisions"},
		Content:        ContentRef{Representation: ContentRepresentation, File: "content"},
		ContentPayload: []byte("\n# ADR\n\n## Context\n\nC\n\n## Decision\n\nD\n\n## Consequences\n\nCo\n\n## Alternatives Considered\n\nA\n"),
		ChangeLog: []ChangeLogEntry{
			{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Test Author")},
			{Date: "2026-08-05", Domain: "content-state", From: "-", To: state, By: conformance.User("Test Author")},
		},
		Relationships: rels,
	}
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()
	return u
}

// assembleTestPackage builds a directory-layout package from the spec and
// returns its path. Digests are computed exactly like serialize.go.
func assembleTestPackage(t *testing.T, spec testPackageSpec) string {
	t.Helper()
	sort.Slice(spec.units, func(i, j int) bool {
		return spec.units[i].CanonicalIdentityForm < spec.units[j].CanonicalIdentityForm
	})

	all := map[string][]byte{}
	unitDigests := map[string]string{}
	var manifestUnits []ManifestUnit
	for _, u := range spec.units {
		u.UnitDir = UnitDirName(u.Identity)
		unitJSON, err := marshalLF(u)
		if err != nil {
			t.Fatal(err)
		}
		all[u.UnitDir+"/unit.json"] = unitJSON
		all[u.UnitDir+"/content"] = u.ContentPayload
		sum := sha256.Sum256(append(unitJSON, u.ContentPayload...))
		u.Digest = hex.EncodeToString(sum[:])
		unitDigests[u.CanonicalIdentityForm] = u.Digest
		manifestUnits = append(manifestUnits, ManifestUnit{
			CanonicalIdentityForm: u.CanonicalIdentityForm,
			Type:                  u.Identity.Type,
			ID:                    u.Identity.ID,
			Namespace:             u.Identity.Namespace,
			InstanceVersion:       u.Identity.InstanceVersion,
			Revision:              u.Revision,
			ContentRepresentation: u.Content.Representation,
			ContentFile:           u.UnitDir + "/content",
			UnitDigest:            u.Digest,
		})
	}
	sort.Slice(manifestUnits, func(i, j int) bool {
		return manifestUnits[i].CanonicalIdentityForm < manifestUnits[j].CanonicalIdentityForm
	})

	var attDigests []AttachmentDigest
	for _, a := range spec.atts {
		sum := sha256.Sum256(a.Data)
		a.Digest = hex.EncodeToString(sum[:])
		all["attachments/"+a.ID] = a.Data
		attDigests = append(attDigests, AttachmentDigest{ID: a.ID, Digest: a.Digest})
	}
	sort.Slice(attDigests, func(i, j int) bool { return attDigests[i].ID < attDigests[j].ID })

	scope := spec.scope
	if scope == "" {
		scope = ScopeRepository
	}
	header := Header{
		SerializationVersion:  SerializationVersion,
		ExchangeFormatVersion: ExchangeFormatVersion,
		SpecificationVersion:  SpecificationVersion,
		Exporter:              Exporter,
		PackageIdentityLabel:  PackageIdentityLabel(scope, spec.namespace),
		ExportScope:           scope,
		Namespace:             spec.namespace,
	}
	if spec.headerOverrides != nil {
		spec.headerOverrides(&header)
	}
	headerBytes, err := marshalLF(header)
	if err != nil {
		t.Fatal(err)
	}
	if spec.extraHeaderField {
		s := strings.TrimSuffix(string(headerBytes), "\n")
		s = s[:len(s)-1] + `,"bogus_field":1}` + "\n"
		headerBytes = []byte(s)
	}
	all["header.json"] = headerBytes

	decls := Declarations{
		Closure:            ClosureDeclaration{Scope: scope, Seeds: []string{}},
		ExternalReferences: spec.externals,
		Extensions:         []ExtensionDecl{},
	}
	declBytes, err := marshalLF(decls)
	if err != nil {
		t.Fatal(err)
	}
	all["declarations.json"] = declBytes

	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	hasher := sha256.New()
	for _, name := range names {
		hasher.Write(all[name])
	}
	packageDigest := hex.EncodeToString(hasher.Sum(nil))

	manifest := Manifest{
		Scope:                 scope,
		PackageIdentityLabel:  header.PackageIdentityLabel,
		SerializationVersion:  header.SerializationVersion,
		ExchangeFormatVersion: header.ExchangeFormatVersion,
		SpecificationVersion:  header.SpecificationVersion,
		PackageDigest:         packageDigest,
		Units:                 manifestUnits,
		Counts: Counts{
			Units:              len(manifestUnits),
			Attachments:        len(spec.atts),
			ExternalReferences: len(spec.externals),
			Extensions:         0,
		},
		Closure: ClosureDeclaration{Scope: scope, Seeds: []string{}},
	}
	if spec.dropManifestUnit && len(manifest.Units) > 0 {
		manifest.Units = manifest.Units[:len(manifest.Units)-1]
		manifest.Counts.Units = len(manifest.Units)
	}
	manifestBytes, err := marshalLF(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if spec.malformedManifest {
		manifestBytes = []byte("{not json")
	}
	all["manifest.json"] = manifestBytes

	integrity := Integrity{
		PackageDigest: packageDigest,
		Attachments:   attDigests,
	}
	for _, form := range namesOf(unitDigests) {
		integrity.Units = append(integrity.Units, UnitDigest{CanonicalIdentityForm: form, Digest: unitDigests[form]})
	}
	if spec.corruptDigest {
		integrity.PackageDigest = strings.Repeat("0", 64)
	}
	integrityBytes, err := marshalLF(integrity)
	if err != nil {
		t.Fatal(err)
	}
	all["integrity.json"] = integrityBytes

	names = names[:0]
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	var entries []entry
	for _, name := range names {
		entries = append(entries, entry{name: name, data: all[name]})
	}
	dir := filepath.Join(t.TempDir(), "pkgdir")
	if err := writeDir(dir, entries); err != nil {
		t.Fatal(err)
	}
	return dir
}

// namesOf returns the sorted keys of a string map.
func namesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mutatePackage rewrites a real exported package: applies fn to the entry
// map and re-digests everything (package digest, per-unit digests, echoes)
// so the mutated package is integrity-valid. Returns the new package path.
func mutatePackage(t *testing.T, src string, fn func(entries map[string][]byte)) string {
	t.Helper()
	entries, _ := readZIP(t, src)
	fn(entries)

	// Recompute per-unit digests (unit.json || content) and refresh the
	// manifest/integrity unit lists.
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	unitDigests := map[string]string{}
	for _, u := range manifest.Units {
		dir := strings.TrimSuffix(u.ContentFile, "/content")
		unitJSON := entries[dir+"/unit.json"]
		content := entries[dir+"/content"]
		sum := sha256.Sum256(append(unitJSON, content...))
		unitDigests[u.CanonicalIdentityForm] = hex.EncodeToString(sum[:])
		u.UnitDigest = unitDigests[u.CanonicalIdentityForm]
		manifest.Units = replaceManifestUnit(manifest.Units, u)
	}
	var integrity Integrity
	mustUnmarshal(t, entries, "integrity.json", &integrity)
	integrity.Units = integrity.Units[:0]
	for _, form := range namesOf(unitDigests) {
		integrity.Units = append(integrity.Units, UnitDigest{CanonicalIdentityForm: form, Digest: unitDigests[form]})
	}

	// Package digest over everything except manifest.json + integrity.json.
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	hasher := sha256.New()
	for _, name := range names {
		if name == "manifest.json" || name == "integrity.json" {
			continue
		}
		hasher.Write(entries[name])
	}
	packageDigest := hex.EncodeToString(hasher.Sum(nil))
	manifest.PackageDigest = packageDigest
	integrity.PackageDigest = packageDigest

	var err error
	entries["manifest.json"], err = marshalLF(manifest)
	if err != nil {
		t.Fatal(err)
	}
	entries["integrity.json"], err = marshalLF(integrity)
	if err != nil {
		t.Fatal(err)
	}

	names = names[:0]
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []entry
	for _, name := range names {
		out = append(out, entry{name: name, data: entries[name]})
	}
	path := filepath.Join(t.TempDir(), "mutated.ekapkg")
	if err := writeZIP(path, out); err != nil {
		t.Fatal(err)
	}
	return path
}

func replaceManifestUnit(units []ManifestUnit, u ManifestUnit) []ManifestUnit {
	for i := range units {
		if units[i].CanonicalIdentityForm == u.CanonicalIdentityForm {
			units[i] = u
		}
	}
	return units
}

// ---------------------------------------------------------------------------
// Round-trip (the crown jewel)
// ---------------------------------------------------------------------------

// TestImportRoundTrip: export the valid fixture repository, import into a
// freshly bootstrapped repository, validate PASS, re-export — the second
// package must be byte-identical to the first (Exchange §15.1, RSF §10.4).
func TestImportRoundTrip(t *testing.T) {
	pkgA := exportPackage(t, fixtureValid)
	repoB := initTestRepo(t)

	res := mustImport(t, pkgA, ImportOptions{Root: repoB})
	if len(res.ImportedArtifacts) != 6 {
		t.Fatalf("imported = %d, want 6: %v", len(res.ImportedArtifacts), res.ImportedArtifacts)
	}
	if len(res.Conflicts) != 0 || len(res.SkippedArtifacts) != 0 {
		t.Fatalf("verdicts: conflicts %v, skipped %v", res.Conflicts, res.SkippedArtifacts)
	}
	if !res.Validation.Pass() || !res.PreValidation.Pass() {
		t.Fatal("pre/post validation must pass")
	}

	// Post-import repository must validate.
	report, err := conformance.Validate(repoB)
	if err != nil || !report.Pass() {
		t.Fatalf("imported repository must validate: %v %+v", err, report.SortedResults())
	}

	pkgB := exportPackage(t, repoB)
	dataA, err := os.ReadFile(pkgA)
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := os.ReadFile(pkgB)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dataA, dataB) {
		entriesA, _ := readZIP(t, pkgA)
		entriesB, _ := readZIP(t, pkgB)
		for name := range entriesA {
			if !bytes.Equal(entriesA[name], entriesB[name]) {
				t.Errorf("entry %s differs between the original and the re-exported package", name)
				t.Errorf("  original: %s", truncate(entriesA[name], 300))
				t.Errorf("  re-export: %s", truncate(entriesB[name], 300))
			}
		}
		t.Fatal("re-export is not byte-identical to the original package (round-trip broken)")
	}
}

// truncate shortens a byte slice for diagnostics.
func truncate(data []byte, n int) string {
	s := string(data)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// TestImportSingleArtifact: an instance-scope package imports exactly one
// artifact, written to the correct repository location. (The plan v2
// instance is not used here: its supersedes reference to v1 is a declared
// external that cannot resolve in an empty target, which correctly blocks
// a non-draft import — that scenario is covered by the referential tests.)
func TestImportSingleArtifact(t *testing.T) {
	pkg := filepath.Join(t.TempDir(), "instance.ekapkg")
	mustExport(t, fixtureValid, []string{"sto:login-email:1"}, pkg)
	repo := newTestRepo(t)

	res := mustImport(t, pkg, ImportOptions{Root: repo})
	if len(res.ImportedArtifacts) != 1 || res.ImportedArtifacts[0] != "eka-valid-fixture/sto:login-email:1" {
		t.Fatalf("imported = %v", res.ImportedArtifacts)
	}
	want := filepath.Join(repo, "docs", "operating", "work-items", "stories", "sto-login-email.json")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("story file missing: %v", err)
	}
	if !strings.Contains(string(data), "\"executionState\": \"in-progress\"") {
		t.Errorf("file must carry the owned execution state:\n%s", truncate(data, 400))
	}
}

// TestImportCollection: a collection-scope package imports the union of
// the targets.
func TestImportCollection(t *testing.T) {
	pkg := filepath.Join(t.TempDir(), "collection.ekapkg")
	mustExport(t, fixtureValid, []string{"plan:rilis-1", "sto:login-email", "adr:001-exchange:1"}, pkg)
	repo := newTestRepo(t)

	res := mustImport(t, pkg, ImportOptions{Root: repo})
	if len(res.ImportedArtifacts) != 4 {
		t.Fatalf("imported = %d, want 4: %v", len(res.ImportedArtifacts), res.ImportedArtifacts)
	}
}

// TestImportMergeIntoExisting: importing a repository-scope package into a
// repository that already holds a subset of identical artifacts adds the
// new identities and skips the identical ones.
func TestImportMergeIntoExisting(t *testing.T) {
	pkgA := exportPackage(t, fixtureValid)

	// Target repository: a subset of the fixture artifacts (sto + adr),
	// byte-identical to the source.
	repo := newTestRepo(t)
	for _, rel := range []string{
		"docs/operating/work-items/stories/sto-login-email.md",
		"docs/decisions/adr-001-exchange.md",
	} {
		data, err := os.ReadFile(filepath.Join(fixtureValid, rel))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res := mustImport(t, pkgA, ImportOptions{Root: repo})
	if len(res.ImportedArtifacts) != 4 {
		t.Fatalf("imported = %d, want 4 (the 4 new identities): %v", len(res.ImportedArtifacts), res.ImportedArtifacts)
	}
	if len(res.SkippedArtifacts) != 2 {
		t.Fatalf("skipped = %v, want the 2 identical duplicates", res.SkippedArtifacts)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", res.Conflicts)
	}
	// The pre-existing files must be untouched.
	orig, _ := os.ReadFile(filepath.Join(fixtureValid, "docs/decisions/adr-001-exchange.md"))
	got, _ := os.ReadFile(filepath.Join(repo, "docs/decisions/adr-001-exchange.md"))
	if !bytes.Equal(orig, got) {
		t.Error("pre-existing artifact must not be modified")
	}
	report, err := conformance.Validate(repo)
	if err != nil || !report.Pass() {
		t.Fatalf("merged repository must validate: %v %+v", err, report.SortedResults())
	}
}

// TestImportTwiceIsNoOp: re-importing the same package is a no-op — no new
// units, no conflicts (Exchange §15.5, §11.2 duplicate detection).
func TestImportTwiceIsNoOp(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	repo := newTestRepo(t)

	first := mustImport(t, pkg, ImportOptions{Root: repo})
	if len(first.ImportedArtifacts) != 6 {
		t.Fatalf("first import = %d, want 6", len(first.ImportedArtifacts))
	}
	before := snapshotRepo(t, repo)

	second := mustImport(t, pkg, ImportOptions{Root: repo})
	if len(second.ImportedArtifacts) != 0 {
		t.Errorf("second import must import nothing, got %v", second.ImportedArtifacts)
	}
	if len(second.SkippedArtifacts) != 6 {
		t.Errorf("second import must skip 6 duplicates, got %v", second.SkippedArtifacts)
	}
	if len(second.Conflicts) != 0 {
		t.Errorf("second import must not conflict, got %v", second.Conflicts)
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportDeterministic: importing the same package into two identical
// repositories produces identical results and identical file trees.
func TestImportDeterministic(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	repo1 := newTestRepo(t)
	repo2 := newTestRepo(t)

	res1 := mustImport(t, pkg, ImportOptions{Root: repo1})
	res2 := mustImport(t, pkg, ImportOptions{Root: repo2})

	if !reflectDeepEqualStrings(res1.ImportedArtifacts, res2.ImportedArtifacts) {
		t.Errorf("imported sets differ: %v vs %v", res1.ImportedArtifacts, res2.ImportedArtifacts)
	}
	tree1 := snapshotRepo(t, repo1)
	tree2 := snapshotRepo(t, repo2)
	if len(tree1) != len(tree2) {
		t.Fatalf("file trees differ: %d vs %d files", len(tree1), len(tree2))
	}
	for path, data := range tree1 {
		if got, ok := tree2[path]; !ok || !bytes.Equal(got, data) {
			t.Errorf("file %s differs between the two imports", path)
		}
	}
}

func reflectDeepEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Package rejection scenarios (exit 2; repository unchanged)
// ---------------------------------------------------------------------------

// TestImportCorruptPackageDigest: a corrupted integrity block is rejected
// before any validation phase (Exchange §17.1).
func TestImportCorruptPackageDigest(t *testing.T) {
	bad := assembleTestPackage(t, testPackageSpec{
		units:         []*Unit{specUnit("ns-one", "001", 1, "approved", nil)},
		corruptDigest: true,
	})
	repo := newTestRepo(t)
	before := snapshotRepo(t, repo)

	_, err := Import(bad, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("corrupt package must be rejected")
	}
	var pe *PackageError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %T, want *PackageError: %v", err, err)
	}
	if !strings.Contains(err.Error(), "integrity verification failed") {
		t.Errorf("message must name the integrity failure, got %q", err.Error())
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportMalformedManifest: invalid JSON in a package block is rejected
// (manifest.json is excluded from the package digest, so this exercises
// the strict-decode path, not the digest path).
func TestImportMalformedManifest(t *testing.T) {
	dir := assembleTestPackage(t, testPackageSpec{
		units:             []*Unit{specUnit("ns-one", "001", 1, "approved", nil)},
		malformedManifest: true,
	})
	repo := newTestRepo(t)
	before := snapshotRepo(t, repo)

	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("malformed manifest must be rejected")
	}
	var pe *PackageError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %T, want *PackageError", err)
	}
	if !strings.Contains(err.Error(), "not valid RSF JSON") {
		t.Errorf("message must name the JSON failure, got %q", err.Error())
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportMissingManifestUnit: a manifest that omits one unit violates
// the manifest <-> units 1:1 correspondence (Exchange §10.6) and is
// rejected.
func TestImportMissingManifestUnit(t *testing.T) {
	dir := assembleTestPackage(t, testPackageSpec{
		units:            []*Unit{specUnit("ns-one", "001", 1, "approved", nil), specUnit("ns-one", "002", 1, "approved", nil)},
		dropManifestUnit: true,
	})
	repo := newTestRepo(t)
	before := snapshotRepo(t, repo)

	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("manifest with a missing unit must be rejected")
	}
	var pe *PackageError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %T, want *PackageError", err)
	}
	if !strings.Contains(err.Error(), "not self-consistent") {
		t.Errorf("message must name the self-consistency failure, got %q", err.Error())
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportMissingRequiredEntry: a package without header.json is not a
// package.
func TestImportMissingRequiredEntry(t *testing.T) {
	repo := newTestRepo(t)
	_, err := Import(repo, ImportOptions{Root: newTestRepo(t)})
	if err == nil {
		t.Fatal("a non-package path must be rejected")
	}
}

// TestImportUnsupportedVersions: phase 1 contract validation rejects
// unsupported versions with diagnostics listing found vs supported
// (Exchange §9.2, §16.3).
func TestImportUnsupportedVersions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rewrite func(h *Header)
		wantMsg string
	}{
		{"serialization version 3", func(h *Header) { h.SerializationVersion = "3" }, "serialization version"},
		{"exchange format 9", func(h *Header) { h.ExchangeFormatVersion = "9" }, "exchange format version"},
		{"specification 2.0", func(h *Header) { h.SpecificationVersion = "2.0" }, "specification version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := assembleTestPackage(t, testPackageSpec{
				units:           []*Unit{specUnit("ns-one", "001", 1, "approved", nil)},
				headerOverrides: tc.rewrite,
			})
			repo := newTestRepo(t)
			_, err := Import(dir, ImportOptions{Root: repo})
			if err == nil {
				t.Fatal("unsupported version must be rejected")
			}
			var pe *PackageError
			if !errors.As(err, &pe) {
				t.Fatalf("error = %T, want *PackageError: %v", err, err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("message must name %q, got %q", tc.wantMsg, err.Error())
			}
			if !strings.Contains(err.Error(), "supported") {
				t.Errorf("message must contrast found vs supported, got %q", err.Error())
			}
		})
	}
}

// TestImportUnknownFieldRejected: a field unknown to a v1 implementer is
// rejected (RSF §9.5 reject-by-default).
func TestImportUnknownFieldRejected(t *testing.T) {
	dir := assembleTestPackage(t, testPackageSpec{
		units:            []*Unit{specUnit("ns-one", "001", 1, "approved", nil)},
		extraHeaderField: true,
	})
	repo := newTestRepo(t)
	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("an unknown header field must be rejected")
	}
	if !strings.Contains(err.Error(), "not valid RSF JSON") {
		t.Errorf("message must name the unknown field, got %q", err.Error())
	}
}

// TestImportUnknownEntryRejected: a package entry that maps to no logical
// element is rejected (RSF §9.5).
func TestImportUnknownEntryRejected(t *testing.T) {
	dir := assembleTestPackage(t, testPackageSpec{units: []*Unit{specUnit("ns-one", "001", 1, "approved", nil)}})
	// Add a stray file to the directory layout.
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := newTestRepo(t)
	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("an unknown package entry must be rejected")
	}
	if !strings.Contains(err.Error(), "unknown entry") {
		t.Errorf("message must name the unknown entry, got %q", err.Error())
	}
}

// TestImportDuplicateIdentityInPackage: a manifest listing the same unit
// form twice violates the Exchange §10.6 1:1 correspondence and is
// rejected (a self-consistent package cannot carry duplicate identities;
// phase 2 uniqueness is defense in depth on top of self-consistency).
func TestImportDuplicateIdentityInPackage(t *testing.T) {
	dir := assembleTestPackage(t, testPackageSpec{
		units: []*Unit{specUnit("ns-one", "001", 1, "approved", nil), specUnit("ns-one", "001", 1, "approved", nil)},
	})
	repo := newTestRepo(t)
	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("duplicate package identity must be rejected")
	}
	if !strings.Contains(err.Error(), "more than once") {
		t.Errorf("message must name the duplicate, got %q", err.Error())
	}
}

// TestImportBadIdentityCharset: phase 2 rejects a non-canonical identity
// component (RSF §5.2.3 charset guard).
func TestImportBadIdentityCharset(t *testing.T) {
	bad := specUnit("bad ns", "001", 1, "approved", nil) // whitespace in namespace
	dir := assembleTestPackage(t, testPackageSpec{units: []*Unit{bad}})
	repo := newTestRepo(t)
	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("a non-canonical identity must be rejected")
	}
	if !strings.Contains(err.Error(), "identity charset") {
		t.Errorf("message must name the charset guard, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Referential scenarios (phase 5)
// ---------------------------------------------------------------------------

// TestImportUndeclaredExternal: a non-draft unit referencing a target that
// is not in the package, not in the repository and not declared makes the
// package invalid (Exchange §12.3) — rejected, repository unchanged.
func TestImportUndeclaredExternal(t *testing.T) {
	// Export the adr line only: its depends-on target (sto) becomes a
	// declared external.
	linePkg := filepath.Join(t.TempDir(), "line.ekapkg")
	mustExport(t, fixtureValid, []string{"adr:001-exchange"}, linePkg)
	// Remove the external declaration.
	und := mutatePackage(t, linePkg, func(entries map[string][]byte) {
		var decls Declarations
		mustUnmarshal(t, entries, "declarations.json", &decls)
		decls.ExternalReferences = []ExternalReference{}
		var err error
		entries["declarations.json"], err = marshalLF(decls)
		if err != nil {
			t.Fatal(err)
		}
	})
	repo := newTestRepo(t)
	before := snapshotRepo(t, repo)

	_, err := Import(und, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("undeclared external reference must be rejected")
	}
	var pe *PackageError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %T, want *PackageError: %v", err, err)
	}
	if !strings.Contains(err.Error(), "not declared as an External Reference") {
		t.Errorf("message must name the undeclared reference, got %q", err.Error())
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportUnresolvedDeclaredExternal: a declared external that does not
// resolve in the target repository blocks the import of a non-draft unit
// (Exchange §7.4 step 3; no draft tolerance) — exit-1 class error.
func TestImportUnresolvedDeclaredExternal(t *testing.T) {
	linePkg := filepath.Join(t.TempDir(), "line.ekapkg")
	mustExport(t, fixtureValid, []string{"adr:001-exchange"}, linePkg)
	repo := newTestRepo(t)
	before := snapshotRepo(t, repo)

	_, err := Import(linePkg, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("unresolved declared external on a non-draft unit must block")
	}
	var re *RelationshipError
	if !errors.As(err, &re) {
		t.Fatalf("error = %T, want *RelationshipError: %v", err, err)
	}
	if len(re.Details) != 1 || !strings.Contains(re.Details[0], "sto:login-email") {
		t.Errorf("details must name the failing relationship, got %v", re.Details)
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportDraftTolerance: a draft unit with a declared external that does
// not resolve imports successfully with a warning (rule 5 draft tolerance,
// Exchange §7.4). The draft state is only valid on living content-state
// variants (e.g. spec-), so the package is built from scratch.
func TestImportDraftTolerance(t *testing.T) {
	form := "ns-one/spec:001:1"
	dir := assembleTestPackage(t, testPackageSpec{
		units: []*Unit{specUnit("ns-one", "001", 1, "draft", []Relationship{
			{Type: "depends-on", Target: "ns-other/sto:ghost:1"},
		})},
		externals: []ExternalReference{
			{Source: form, Type: "depends-on", Target: "ns-other/sto:ghost:1"},
		},
	})
	repo := newTestRepo(t)

	res := mustImport(t, dir, ImportOptions{Root: repo})
	if len(res.ImportedArtifacts) != 1 {
		t.Fatalf("draft unit must import, got %v", res.ImportedArtifacts)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "draft tolerance") {
		t.Errorf("import must record the draft-tolerance warning, got %v", res.Warnings)
	}
	report, err := conformance.Validate(repo)
	if err != nil || !report.Pass() {
		t.Fatalf("repository with a draft unit must validate (warnings only): %v %+v", err, report.SortedResults())
	}
}

// TestImportDraftToleranceNonDraftBlocks: the same package with a
// non-draft unit blocks (declared external + no draft tolerance).
func TestImportDraftToleranceNonDraftBlocks(t *testing.T) {
	form := "ns-one/spec:001:1"
	dir := assembleTestPackage(t, testPackageSpec{
		units: []*Unit{specUnit("ns-one", "001", 1, "approved", []Relationship{
			{Type: "depends-on", Target: "ns-other/sto:ghost:1"},
		})},
		externals: []ExternalReference{
			{Source: form, Type: "depends-on", Target: "ns-other/sto:ghost:1"},
		},
	})
	repo := newTestRepo(t)
	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("an unresolved declared external on a non-draft unit must block")
	}
	var re *RelationshipError
	if !errors.As(err, &re) {
		t.Fatalf("error = %T, want *RelationshipError", err)
	}
}

// TestImportGlobalResolution: a relationship target already present in the
// target repository resolves without a declaration (Exchange §7.4 step 2).
func TestImportGlobalResolution(t *testing.T) {
	// Line package: adr depends-on sto (declared external).
	linePkg := filepath.Join(t.TempDir(), "line.ekapkg")
	mustExport(t, fixtureValid, []string{"adr:001-exchange"}, linePkg)
	// Target repository holds the identical sto artifact: the external
	// resolves globally, the import succeeds.
	repo := newTestRepo(t)
	data, err := os.ReadFile(filepath.Join(fixtureValid, "docs/operating/work-items/stories/sto-login-email.md"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "docs/operating/work-items/stories/sto-login-email.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustImport(t, linePkg, ImportOptions{Root: repo})
	if len(res.ImportedArtifacts) != 1 {
		t.Fatalf("imported = %v, want the adr only", res.ImportedArtifacts)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", res.Warnings)
	}
}

// TestImportCrossNamespaceV1Limitation: a cross-namespace relationship to
// an instance beyond v1 cannot be expressed in the repository reference
// grammar — refused with a clear message (v1 limitation, documented).
func TestImportCrossNamespaceV1Limitation(t *testing.T) {
	dir := assembleTestPackage(t, testPackageSpec{
		namespace: "ns-beta",
		units: []*Unit{
			specUnit("ns-alpha", "001-beta", 2, "approved", nil),
			adrUnit("ns-beta", "002-cross", 1, "accepted", []Relationship{
				{Type: "depends-on", Target: "ns-alpha/spec:001-beta:2"},
			}),
		},
	})
	repo := newTestRepo(t)
	before := snapshotRepo(t, repo)

	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("cross-namespace v2 reference must be refused")
	}
	var pe *PackageError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %T, want *PackageError: %v", err, err)
	}
	if !strings.Contains(err.Error(), "cross-namespace") || !strings.Contains(err.Error(), "v1 limitation") {
		t.Errorf("message must explain the v1 limitation, got %q", err.Error())
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportCrossNamespaceRoundTrip: a two-namespace package with an
// ACTUAL cross-namespace relationship (<ns>/<type>:<id>) imports with the
// repository reference form in the frontmatter, and a re-export yields the
// same canonical identity form (round-trip; Exchange §7.4, design decision
// 4). The multi-ns fixture is reused: its two files are copied into a
// fresh repository and the adr gains a cross-ns depends-on reference to
// ns-beta/spec:001-beta (the spec is carried by the package, so the
// reference resolves in-repo and the repository stays valid).
func TestImportCrossNamespaceRoundTrip(t *testing.T) {
	// Build the two-namespace repository from the multi-ns fixture files.
	src := filepath.Join("testdata", "multi-ns")
	repo := newTestRepo(t)
	for _, rel := range []string{
		"docs/decisions/adr-001-alpha.md",
		"docs/specifications/spec-001-beta.md",
	} {
		data, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			t.Fatal(err)
		}
		if rel == "docs/decisions/adr-001-alpha.md" {
			// Extend the fixture with an actual cross-ns relationship.
			data = []byte(strings.Replace(string(data), "depends-on: []",
				"depends-on:\n  - ns-beta/spec:001-beta", 1))
		}
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The extended repository must validate (the cross-ns ref resolves).
	report, err := conformance.Validate(repo)
	if err != nil || !report.Pass() {
		t.Fatalf("extended fixture must validate: %v %+v", err, report.SortedResults())
	}

	// Export the two-namespace package (repository scope) and import it.
	pkg := exportPackage(t, repo)
	target := newTestRepo(t)
	res := mustImport(t, pkg, ImportOptions{Root: target})
	if len(res.ImportedArtifacts) != 2 {
		t.Fatalf("imported = %v, want the adr and the spec", res.ImportedArtifacts)
	}

	// The frontmatter carries the cross-ns reference in repository form
	// (<ns>/<type>:<id>, line-level; the canonical v-suffix is NOT leaked).
	adr, err := os.ReadFile(filepath.Join(target, "docs", "decisions", "adr-001-alpha.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(adr), "ns-beta/spec:001-beta") {
		t.Errorf("relationships must carry the cross-namespace reference, got:\n%s", truncate(adr, 600))
	}
	if strings.Contains(string(adr), "ns-beta/spec:001-beta:1") {
		t.Errorf("relationships must not carry the canonical version suffix, got:\n%s", truncate(adr, 600))
	}

	// Re-export: the relationship must round-trip to the same canonical
	// identity form (ns-beta/spec:001-beta:1).
	reloaded, err := loadPackage(exportPackage(t, target))
	if err != nil {
		t.Fatal(err)
	}
	adrUnit := reloaded.unitByForm["ns-alpha/adr:001-alpha:1"]
	if adrUnit == nil {
		t.Fatal("re-exported package must carry the adr unit")
	}
	want := Relationship{Type: "depends-on", Target: "ns-beta/spec:001-beta:1"}
	found := false
	for _, rel := range adrUnit.Relationships {
		if rel == want {
			found = true
		}
	}
	if !found {
		t.Errorf("re-exported relationships = %+v, want the canonical cross-ns target %+v",
			adrUnit.Relationships, want)
	}
}

// ---------------------------------------------------------------------------
// Conflict scenarios (phase 6; exit 1)
// ---------------------------------------------------------------------------

// TestImportStateConflict: a package unit whose identity exists with
// different state is rejected with a per-identity difference summary.
func TestImportStateConflict(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	// Target repository: identical to the fixture except the story's
	// execution state moved forward (in-progress -> in-review) with a
	// matching change-log entry. The repository stays valid.
	repo := newTestRepo(t)
	data, err := os.ReadFile(filepath.Join(fixtureValid, "docs/operating/work-items/stories/sto-login-email.md"))
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(data), "execution-state: in-progress", "execution-state: in-review", 1)
	modified = strings.Replace(modified, "from: todo\n    to: in-progress", "from: in-progress\n    to: in-review", 1)
	path := filepath.Join(repo, "docs/operating/work-items/stories/sto-login-email.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(modified), 0o644); err != nil {
		t.Fatal(err)
	}
	// R13 (ADR-019): a work item at in-review requires a resolved
	// implementation note — the modified repository must stay valid so
	// the import reaches the state-conflict check.
	notePath := filepath.Join(repo, "docs/operating/notes/cmt-login-email-implementation.json")
	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		t.Fatal(err)
	}
	note := `{
  "namespace": "eka-valid-fixture",
  "type": "cmt",
  "id": "login-email-implementation",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "domain": "Execution",
  "state": {
    "contentState": "draft",
    "existenceState": "active",
    "noteState": "resolved"
  },
  "relationships": {
    "discusses": ["eka-valid-fixture/sto:login-email"]
  },
  "changeLog": [
    {"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "noteState", "from": "-", "to": "open", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "noteState", "from": "open", "to": "resolved", "by": "Engineering Architecture"}
  ],
  "content": {
    "role": "implementation",
    "summary": "The login-email story implementation is complete.",
    "changes": ["login with email flow"],
    "tests": ["login corpus"]
  }
}
`
	if err := os.WriteFile(notePath, []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotRepo(t, repo)

	_, err = Import(pkg, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("a state conflict must abort the import")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %T, want *ConflictError: %v", err, err)
	}
	if len(ce.Conflicts) != 1 || ce.Conflicts[0].Identity != "eka-valid-fixture/sto:login-email:1" {
		t.Fatalf("conflicts = %+v, want the story identity", ce.Conflicts)
	}
	joined := strings.Join(ce.Conflicts[0].Differences, "; ")
	if !strings.Contains(joined, "state differs") {
		t.Errorf("summary must list the state difference, got %q", joined)
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportContentConflict: differing content on an existing identity is
// a conflict (Exchange §13.2: never auto-merged). The target repository is
// the full fixture copy so the modified artifact stays referentially valid.
func TestImportContentConflict(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	repo := copyFixtureRepo(t)
	path := filepath.Join(repo, "docs/decisions/adr-001-exchange.md")
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(orig), "## Consequences\n\nExport and import tooling", "## Consequences\n\nCHANGED. Export and import tooling", 1)
	if err := os.WriteFile(path, []byte(modified), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotRepo(t, repo)

	_, err = Import(pkg, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("a content conflict must abort the import")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %T, want *ConflictError", err)
	}
	found := false
	for _, c := range ce.Conflicts {
		if c.Identity == "eka-valid-fixture/adr:001-exchange:1" {
			found = true
			if !strings.Contains(strings.Join(c.Differences, "; "), "content differs") {
				t.Errorf("summary must list the content difference, got %v", c.Differences)
			}
		}
	}
	if !found {
		t.Errorf("conflicts = %+v, want the adr identity", ce.Conflicts)
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportMetadataConflict: identical payload with different metadata
// (author) is a conflict too (conservative policy, documented).
func TestImportMetadataConflict(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	repo := copyFixtureRepo(t)
	path := filepath.Join(repo, "docs/decisions/adr-001-exchange.md")
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(orig), "author: Engineering Architecture", "author: Someone Else", 1)
	if err := os.WriteFile(path, []byte(modified), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = Import(pkg, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("a metadata difference must abort the import (conservative)")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %T, want *ConflictError", err)
	}
	found := false
	for _, c := range ce.Conflicts {
		if c.Identity == "eka-valid-fixture/adr:001-exchange:1" {
			found = true
			if !strings.Contains(strings.Join(c.Differences, "; "), "metadata differs") {
				t.Errorf("summary must list the metadata difference, got %v", c.Differences)
			}
		}
	}
	if !found {
		t.Errorf("conflicts = %+v, want the adr identity", ce.Conflicts)
	}
}

// ---------------------------------------------------------------------------
// Attachments
// ---------------------------------------------------------------------------

// TestImportAttachments: attachments are written to <root>/<attachment-id>
// with the digest verified (the payload is carried verbatim).
func TestImportAttachments(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	repo := newTestRepo(t)

	res := mustImport(t, pkg, ImportOptions{Root: repo})
	if len(res.AttachmentsImported) != 1 || res.AttachmentsImported[0] != "docs/architecture/diagram.txt" {
		t.Fatalf("attachments imported = %v", res.AttachmentsImported)
	}
	got, err := os.ReadFile(filepath.Join(repo, "docs", "architecture", "diagram.txt"))
	if err != nil {
		t.Fatalf("attachment file missing: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(fixtureValid, "docs", "architecture", "diagram.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("attachment payload must be carried verbatim")
	}
	sum := sha256.Sum256(want)
	if wantDigest := hex.EncodeToString(sum[:]); true {
		entries, _ := readZIP(t, pkg)
		var integrity Integrity
		mustUnmarshal(t, entries, "integrity.json", &integrity)
		if len(integrity.Attachments) != 1 || integrity.Attachments[0].Digest != wantDigest {
			t.Errorf("recorded attachment digest = %+v, want %s", integrity.Attachments, wantDigest)
		}
	}
}

// TestImportAttachmentDuplicateAndConflict: an identical existing file is
// skipped (no-op); a differing existing file is a conflict that aborts.
func TestImportAttachmentDuplicateAndConflict(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	want, err := os.ReadFile(filepath.Join(fixtureValid, "docs", "architecture", "diagram.txt"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("identical existing file is skipped", func(t *testing.T) {
		repo := newTestRepo(t)
		path := filepath.Join(repo, "docs", "architecture", "diagram.txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		res := mustImport(t, pkg, ImportOptions{Root: repo})
		if len(res.AttachmentsSkipped) != 1 || res.AttachmentsSkipped[0] != "docs/architecture/diagram.txt" {
			t.Errorf("attachments skipped = %v", res.AttachmentsSkipped)
		}
		if len(res.AttachmentsImported) != 0 {
			t.Errorf("attachments imported = %v, want none", res.AttachmentsImported)
		}
	})

	t.Run("differing existing file is a conflict", func(t *testing.T) {
		repo := newTestRepo(t)
		path := filepath.Join(repo, "docs", "architecture", "diagram.txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("different content"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := snapshotRepo(t, repo)
		_, err := Import(pkg, ImportOptions{Root: repo})
		if err == nil {
			t.Fatal("a differing attachment must abort the import")
		}
		var ce *ConflictError
		if !errors.As(err, &ce) {
			t.Fatalf("error = %T, want *ConflictError", err)
		}
		if len(ce.Conflicts) != 1 || ce.Conflicts[0].Identity != "docs/architecture/diagram.txt" {
			t.Errorf("conflicts = %+v, want the attachment identity", ce.Conflicts)
		}
		assertRepoUnchanged(t, repo, before)
	})
}

// ---------------------------------------------------------------------------
// Repository validation gate (exit 1)
// ---------------------------------------------------------------------------

// TestImportInvalidTargetRepo: a repository with blocking violations is
// refused before any package processing (validation-before-commit).
func TestImportInvalidTargetRepo(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	// Target repository with a blocking violation (type without id).
	repo := newTestRepo(t)
	bad := "---\nnamespace: eka-cli\ntype: sto\n---\n# Bad\n"
	if err := os.WriteFile(filepath.Join(repo, "docs", "sto-bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotRepo(t, repo)

	_, err := Import(pkg, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("import into an invalid repository must be refused")
	}
	var ive *ImportValidationError
	if !errors.As(err, &ive) {
		t.Fatalf("error = %T, want *ImportValidationError", err)
	}
	if ive.Phase != "pre" {
		t.Errorf("phase = %q, want pre", ive.Phase)
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportNonEKA: a directory without the docs/ knowledge tree is
// refused at the engine level with the docs-tree message (the
// eka.yaml repository-context gate of ADR-018 lives at the CLI, not
// here).
func TestImportNonEKA(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	notEKA := t.TempDir()

	_, err := Import(pkg, ImportOptions{Root: notEKA})
	if err == nil {
		t.Fatal("import into a directory without docs/ must be refused")
	}
	var ive *ImportValidationError
	if !errors.As(err, &ive) {
		t.Fatalf("error = %T, want *ImportValidationError", err)
	}
	if !strings.Contains(err.Error(), "has no docs/ knowledge tree") {
		t.Errorf("message must explain the missing docs tree, got %q", err.Error())
	}
}

// TestImportPostCommitRollback: an injected validator that fails the
// post-commit revalidation triggers a full rollback — the repository is
// byte-identical to its pre-import state (Exchange §11.1 phase 10).
func TestImportPostCommitRollback(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	repo := newTestRepo(t)
	before := snapshotRepo(t, repo)

	calls := 0
	validate := func(root string) (*conformance.Report, error) {
		calls++
		if calls == 2 {
			return &conformance.Report{Root: root, Results: []conformance.Result{{
				File: "docs/x.md", Rule: conformance.Rule1, Severity: conformance.SeverityError,
				Message: "injected post-commit failure",
			}}}, nil
		}
		return conformance.Validate(root)
	}

	_, err := Import(pkg, ImportOptions{Root: repo, Validate: validate})
	if err == nil {
		t.Fatal("a failing post-commit revalidation must fail the import")
	}
	var ive *ImportValidationError
	if !errors.As(err, &ive) {
		t.Fatalf("error = %T, want *ImportValidationError: %v", err, err)
	}
	if ive.Phase != "post" {
		t.Errorf("phase = %q, want post", ive.Phase)
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("message must state the rollback, got %q", err.Error())
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportWritePlanBlockedByFile: a plain file occupying a required
// directory path aborts the import in phase 8 (write-plan construction,
// before any write): the plan's Lstat hits ENOTDIR. This is the
// pre-write failure class — the repository is unchanged trivially.
func TestImportWritePlanBlockedByFile(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	repo := newTestRepo(t)
	// Block the planning folder with a plain file: the plan units cannot
	// be planned.
	if err := os.WriteFile(filepath.Join(repo, "docs", "planning"), []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotRepo(t, repo)

	_, err := Import(pkg, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("the write-plan failure must fail the import")
	}
	if !strings.Contains(err.Error(), "cannot inspect target path") {
		t.Errorf("error must be the phase-8 plan failure, got %q", err.Error())
	}
	// No partial files may remain: the repository equals its snapshot.
	assertRepoUnchanged(t, repo, before)
}

// TestImportRollbackMidCommit: a REAL mid-commit failure — a pre-existing
// read-only directory blocking a LATER write-plan unit — fails the import
// in phase 9 after at least one file was already committed, and the
// rollback leaves zero leftover files/dirs (Exchange §11.1 phase 9/10).
//
// The fixture commit order is deterministic: attachment, ctr, plan v1,
// plan v2, sto, tkt, and the adr LAST (dependency-resolved; the adr
// depends-on the sto). Making docs/decisions read-only blocks exactly the
// adr's staging — the failure message names the adr target path, proving
// the failure happened at the last op, after 6 files were already
// committed.
func TestImportRollbackMidCommit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on Windows; the permission-based blocker is not portable")
	}
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not block root; the blocker is ineffective")
	}
	pkg := exportPackage(t, fixtureValid)
	repo := newTestRepo(t)
	decisions := filepath.Join(repo, "docs", "decisions")
	if err := os.MkdirAll(decisions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(decisions, 0o555); err != nil {
		t.Fatal(err)
	}
	// Restore write permission before the TempDir cleanup removes the tree.
	t.Cleanup(func() { os.Chmod(decisions, 0o755) })
	before := snapshotRepo(t, repo)

	_, err := Import(pkg, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("the mid-commit write failure must fail the import")
	}
	// The failure is a phase-9 staging failure on the LAST op (not a
	// phase-8 plan failure): the message names the adr target path.
	if !strings.Contains(err.Error(), "cannot stage docs/decisions/adr-001-exchange.json") {
		t.Errorf("error must name the mid-commit staging failure, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "cannot inspect target path") {
		t.Errorf("error must not be a phase-8 plan failure, got %q", err.Error())
	}
	// Rollback: zero leftover files/dirs — the repository equals its
	// pre-import snapshot.
	assertRepoUnchanged(t, repo, before)
}

// TestRepoWriterRollbackReportsFailures: rollback must surface removal
// failures instead of swallowing them — a failed rollback leaves the
// repository potentially partially modified, and that must be visible.
// A foreign file keeps one created directory non-empty; the other created
// file/dir are still removed, and the error names the blocked path
// deterministically. (No chmod needed: directory-emptiness semantics are
// portable, so no Windows/root skip is required.)
func TestRepoWriterRollbackReportsFailures(t *testing.T) {
	root := newTestRepo(t)
	w := newRepoWriter(root)
	for _, op := range []writeOp{
		{rel: "docs/blocked/one.md", data: []byte("one")},
		{rel: "docs/free/two.md", data: []byte("two")},
	} {
		if err := w.write(op); err != nil {
			t.Fatal(err)
		}
	}
	// A foreign file appears inside a directory this run created: the
	// directory can no longer be removed (os.Remove requires empty dirs).
	foreign := filepath.Join(root, "docs", "blocked", "foreign.md")
	if err := os.WriteFile(foreign, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := w.rollback()
	if err == nil {
		t.Fatal("rollback must report the failed removal")
	}
	if !strings.Contains(err.Error(), filepath.Join("docs", "blocked")) {
		t.Errorf("error must name the blocked path, got %q", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(root, "docs", "free", "two.md")); !os.IsNotExist(statErr) {
		t.Error("the removable file must still be removed")
	}
	if _, statErr := os.Stat(foreign); statErr != nil {
		t.Error("the foreign file must be left untouched")
	}
}

// ---------------------------------------------------------------------------
// Package-side validation phases 3-4
// ---------------------------------------------------------------------------

// TestImportInvalidStateValue: phase 3 rejects a unit whose state value is
// not in the domain value set.
func TestImportInvalidStateValue(t *testing.T) {
	dir := assembleTestPackage(t, testPackageSpec{
		units: []*Unit{func() *Unit {
			u := specUnit("ns-one", "001", 1, "bogus-state", nil)
			return u
		}()},
	})
	repo := newTestRepo(t)
	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("an invalid state value must be rejected")
	}
	if !strings.Contains(err.Error(), "not a valid value") {
		t.Errorf("message must name the invalid value, got %q", err.Error())
	}
}

// TestImportMissingRequiredSection: phase 4 rejects a unit whose content
// lacks a required section for its type family.
func TestImportMissingRequiredSection(t *testing.T) {
	u := specUnit("ns-one", "001", 1, "approved", nil)
	u.ContentPayload = []byte("\n# Spec\n\nmissing all sections\n")
	dir := assembleTestPackage(t, testPackageSpec{units: []*Unit{u}})
	repo := newTestRepo(t)
	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("missing required sections must be rejected")
	}
	if !strings.Contains(err.Error(), "missing required section") {
		t.Errorf("message must name the missing sections, got %q", err.Error())
	}
}

// TestImportChangeLogInconsistency: phase 3 rejects a change log whose
// last entry does not equal the current value.
func TestImportChangeLogInconsistency(t *testing.T) {
	u := specUnit("ns-one", "001", 1, "approved", nil)
	u.ChangeLog = []ChangeLogEntry{
		{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Test Author")},
		{Date: "2026-08-05", Domain: "content-state", From: "-", To: "draft", By: conformance.User("Test Author")},
	}
	dir := assembleTestPackage(t, testPackageSpec{units: []*Unit{u}})
	repo := newTestRepo(t)
	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("a change-log inconsistency must be rejected")
	}
	if !strings.Contains(err.Error(), "change-log") {
		t.Errorf("message must name the change-log inconsistency, got %q", err.Error())
	}
}

// TestImportDimensionFolderConflict: a knowledge artifact whose dimension
// does not match its type family folder is refused before any write (R6
// compliance by construction).
func TestImportDimensionFolderConflict(t *testing.T) {
	u := specUnit("ns-one", "001", 1, "approved", nil)
	u.Classification.Dimension = "research" // spec- maps to specifications
	dir := assembleTestPackage(t, testPackageSpec{units: []*Unit{u}})
	repo := newTestRepo(t)
	before := snapshotRepo(t, repo)

	_, err := Import(dir, ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("a dimension/folder conflict must be refused")
	}
	if !strings.Contains(err.Error(), "dimension") {
		t.Errorf("message must name the dimension conflict, got %q", err.Error())
	}
	assertRepoUnchanged(t, repo, before)
}

// TestImportDomainField covers the optional `domain` classification field
// on import: absent = OK (derived, packages written before the field keep
// importing), declared-and-matching = OK, unknown domain = package error,
// mismatch = package error.
func TestImportDomainField(t *testing.T) {
	cases := []struct {
		name    string
		domain  string
		wantErr bool
		wantSub string
	}{
		{"absent", "", false, ""},
		{"declared matching", "Architecture", false, ""},
		{"unknown domain", "Bogus", true, "unknown engineering domain"},
		{"mismatch", "Execution", true, "does not match the home domain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := specUnit("ns-one", "001", 1, "approved", nil)
			u.Classification.Domain = c.domain
			dir := assembleTestPackage(t, testPackageSpec{units: []*Unit{u}})
			repo := newTestRepo(t)
			_, err := Import(dir, ImportOptions{Root: repo})
			if c.wantErr {
				if err == nil {
					t.Fatal("a domain violation must be refused")
				}
				var pe *PackageError
				if !errors.As(err, &pe) {
					t.Fatalf("error = %T, want *PackageError", err)
				}
				if !strings.Contains(err.Error(), c.wantSub) {
					t.Errorf("message must contain %q, got %q", c.wantSub, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("import must succeed: %v", err)
			}
		})
	}
}

// TestImportDomainDerivedAtLoad: the unit-level domain derivation agrees
// with the classification written by the exporter (round-trip check that
// the shared conformance mapping is the single source of truth).
func TestImportDomainDerivedAtLoad(t *testing.T) {
	loaded, err := loadPackage(exportPackage(t, fixtureValid))
	if err != nil {
		t.Fatalf("loadPackage failed: %v", err)
	}
	for _, u := range loaded.units {
		derived, ok := u.Domain()
		if !ok {
			t.Fatalf("unit %s has no derived domain", u.CanonicalIdentityForm)
		}
		if got := u.Classification.Domain; got != string(derived) {
			t.Errorf("unit %s: declared domain %q does not equal derived domain %q",
				u.CanonicalIdentityForm, got, derived)
		}
	}
}

// TestImportEmptyPackage: a package without units imports as a no-op.
func TestImportEmptyPackage(t *testing.T) {
	dir := assembleTestPackage(t, testPackageSpec{})
	repo := newTestRepo(t)
	res := mustImport(t, dir, ImportOptions{Root: repo})
	if len(res.ImportedArtifacts) != 0 {
		t.Errorf("imported = %v, want none", res.ImportedArtifacts)
	}
	if !res.Validation.Pass() {
		t.Error("empty import must validate")
	}
}

// TestImportDirectoryLayoutPackage: a directory-layout package imports
// identically to the ZIP container.
func TestImportDirectoryLayoutPackage(t *testing.T) {
	pkg := exportPackage(t, fixtureValid)
	// Write the same package as a directory layout.
	layout := filepath.Join(t.TempDir(), "layout")
	entries, _ := readZIP(t, pkg)
	var list []entry
	for name, data := range entries {
		list = append(list, entry{name: name, data: data})
	}
	if err := writeDir(layout, list); err != nil {
		t.Fatal(err)
	}
	repo := newTestRepo(t)
	res := mustImport(t, layout, ImportOptions{Root: repo})
	if len(res.ImportedArtifacts) != 6 {
		t.Fatalf("imported = %d, want 6", len(res.ImportedArtifacts))
	}
}

// TestImportFromMultiNamespaceFixture: the checked-in multi-namespace
// package (produced by a previous export) imports cleanly.
func TestImportFromMultiNamespaceFixture(t *testing.T) {
	repo := newTestRepo(t)
	res := mustImport(t, "testdata/multi-ns/rsf-line-ns-alpha-1.ekapkg", ImportOptions{Root: repo})
	if len(res.ImportedArtifacts) != 1 || res.ImportedArtifacts[0] != "ns-alpha/adr:001-alpha:1" {
		t.Fatalf("imported = %v", res.ImportedArtifacts)
	}
	report, err := conformance.Validate(repo)
	if err != nil || !report.Pass() {
		t.Fatalf("imported repository must validate: %v %+v", err, report.SortedResults())
	}
}

// TestImportBadPackagePath: a nonexistent package is a package error.
func TestImportBadPackagePath(t *testing.T) {
	repo := newTestRepo(t)
	_, err := Import(filepath.Join(t.TempDir(), "nope.ekapkg"), ImportOptions{Root: repo})
	if err == nil {
		t.Fatal("a nonexistent package must fail")
	}
	var pe *PackageError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %T, want *PackageError", err)
	}
}

// TestImportJSONRoundTripModel verifies the deserialized model equals the
// original export model (unit-for-unit).
func TestImportJSONRoundTripModel(t *testing.T) {
	loaded, err := loadPackage(exportPackage(t, fixtureValid))
	if err != nil {
		t.Fatalf("loadPackage failed: %v", err)
	}
	if loaded.header.Exporter != "eka" || loaded.header.ExportScope != ScopeRepository {
		t.Errorf("header = %+v", loaded.header)
	}
	if len(loaded.units) != 6 || len(loaded.attachments) != 1 {
		t.Fatalf("units = %d, attachments = %d", len(loaded.units), len(loaded.attachments))
	}
	wantOrder := []string{
		"eka-valid-fixture/adr:001-exchange:1",
		"eka-valid-fixture/ctr:gelombang-1:1",
		"eka-valid-fixture/plan:rilis-1:1",
		"eka-valid-fixture/plan:rilis-1:2",
		"eka-valid-fixture/sto:login-email:1",
		"eka-valid-fixture/tkt:sto-login-email:1",
	}
	for i, u := range loaded.units {
		if u.CanonicalIdentityForm != wantOrder[i] {
			t.Fatalf("unit order = %v", loaded.units)
		}
	}
	plan := loaded.unitByForm["eka-valid-fixture/plan:rilis-1:1"]
	if plan == nil || plan.StateVector != (StateVector{ContentState: "approved", PlanningState: "approved", ExistenceState: "active"}) {
		t.Errorf("plan state vector = %+v", plan.StateVector)
	}
	if len(plan.ChangeLog) != 7 {
		t.Errorf("plan change log = %d entries, want 7", len(plan.ChangeLog))
	}
	if plan.Phase != "mvp" {
		t.Errorf("plan phase = %q", plan.Phase)
	}
	adr := loaded.unitByForm["eka-valid-fixture/adr:001-exchange:1"]
	if len(adr.Relationships) != 1 || adr.Relationships[0] != (Relationship{Type: "depends-on", Target: "eka-valid-fixture/sto:login-email:1"}) {
		t.Errorf("adr relationships = %+v", adr.Relationships)
	}
}
