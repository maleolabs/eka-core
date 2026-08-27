package exchange

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
)

// testdata roots.
const (
	fixtureValid   = "testdata/valid"
	fixtureMultiNS = "testdata/multi-ns"
	fixtureInvalid = "testdata/invalid"
	fixtureEmpty   = "testdata/empty"
)

// mustExport runs Export with the given targets and output and fails the
// test on error.
func mustExport(t *testing.T, root string, targets []string, output string) *Result {
	t.Helper()
	res, err := Export(root, Options{Targets: targets, Output: output})
	if err != nil {
		t.Fatalf("Export(%s, %v) failed: %v", root, targets, err)
	}
	return res
}

// readZIP opens a package file and returns name -> content plus the entry
// order.
func readZIP(t *testing.T, path string) (map[string][]byte, []string) {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("cannot open package %s: %v", path, err)
	}
	defer r.Close()
	entries := map[string][]byte{}
	var order []string
	for _, f := range r.File {
		data, err := readZIPFile(f)
		if err != nil {
			t.Fatalf("cannot read zip entry %s: %v", f.Name, err)
		}
		entries[f.Name] = data
		order = append(order, f.Name)
	}
	return entries, order
}

func readZIPFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mustUnmarshal decodes a package JSON block into v.
func mustUnmarshal(t *testing.T, entries map[string][]byte, name string, v any) {
	t.Helper()
	data, ok := entries[name]
	if !ok {
		t.Fatalf("package has no %s entry", name)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("cannot decode %s: %v", name, err)
	}
}

// TestExportEmptyRepository: a repository without artifacts exports a
// valid package with zero units and an empty seed set.
func TestExportEmptyRepository(t *testing.T) {
	out := filepath.Join(t.TempDir(), "empty.ekapkg")
	res := mustExport(t, fixtureEmpty, nil, out)

	if res.Label != "rsf-repo-"+SerializationVersion {
		t.Errorf("label = %q, want rsf-repo-1 (namespace component omitted for empty packages)", res.Label)
	}
	entries, _ := readZIP(t, out)

	var header Header
	mustUnmarshal(t, entries, "header.json", &header)
	if header.ExportScope != ScopeRepository {
		t.Errorf("scope = %q, want repo", header.ExportScope)
	}
	if header.PackageIdentityLabel != res.Label {
		t.Errorf("header label = %q, want %q", header.PackageIdentityLabel, res.Label)
	}
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	if len(manifest.Units) != 0 {
		t.Errorf("manifest units = %d, want 0", len(manifest.Units))
	}
	if manifest.Counts.Units != 0 {
		t.Errorf("counts.units = %d, want 0", manifest.Counts.Units)
	}
	if len(manifest.Closure.Seeds) != 0 {
		t.Errorf("repo closure seeds = %v, want empty", manifest.Closure.Seeds)
	}
	var declarations Declarations
	mustUnmarshal(t, entries, "declarations.json", &declarations)
	if len(declarations.ExternalReferences) != 0 {
		t.Errorf("externals = %v, want none", declarations.ExternalReferences)
	}
}

// TestExportValidRepository: full-featured repository export; every state
// domain, change log, relationship, classification, phase, revision and
// metadata field must be carried losslessly.
func TestExportValidRepository(t *testing.T) {
	out := filepath.Join(t.TempDir(), "valid.ekapkg")
	res := mustExport(t, fixtureValid, nil, out)

	if res.Label != "rsf-repo-eka-valid-fixture-"+SerializationVersion {
		t.Errorf("label = %q", res.Label)
	}
	if res.Units != 6 {
		t.Errorf("units = %d, want 6", res.Units)
	}
	if res.Attachments != 1 {
		t.Errorf("attachments = %d, want 1", res.Attachments)
	}
	if res.ExternalReferences != 0 {
		t.Errorf("repo-scope externals = %d, want 0", res.ExternalReferences)
	}

	entries, _ := readZIP(t, out)
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	if manifest.Counts.Units != 6 || manifest.Counts.Attachments != 1 || manifest.Counts.Extensions != 0 {
		t.Errorf("counts = %+v", manifest.Counts)
	}

	// Manifest order must be canonical identity order.
	wantOrder := []string{
		"eka-valid-fixture/adr:001-exchange:1",
		"eka-valid-fixture/ctr:gelombang-1:1",
		"eka-valid-fixture/plan:rilis-1:1",
		"eka-valid-fixture/plan:rilis-1:2",
		"eka-valid-fixture/sto:login-email:1",
		"eka-valid-fixture/tkt:sto-login-email:1",
	}
	var gotOrder []string
	for _, u := range manifest.Units {
		gotOrder = append(gotOrder, u.CanonicalIdentityForm)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("manifest order = %v, want %v", gotOrder, wantOrder)
	}

	// Plan v1 unit: full composition.
	var plan Unit
	mustUnmarshal(t, entries, unitPath("eka-valid-fixture", "plan", "rilis-1", 1)+"/unit.json", &plan)
	if plan.Identity != (Identity{Namespace: "eka-valid-fixture", Type: "plan", ID: "rilis-1", InstanceVersion: 1}) {
		t.Errorf("identity = %+v", plan.Identity)
	}
	if plan.CanonicalIdentityForm != "eka-valid-fixture/plan:rilis-1:1" {
		t.Errorf("canonical form = %q", plan.CanonicalIdentityForm)
	}
	if plan.Revision != 1 || plan.Author.Name != "Engineering Architecture" ||
		plan.Created != "2026-08-05" || plan.Updated != "2026-08-05" {
		t.Errorf("metadata = rev %d author %q created %q updated %q",
			plan.Revision, plan.Author, plan.Created, plan.Updated)
	}
	if plan.StateVector != (StateVector{ContentState: "approved", PlanningState: "approved", ExistenceState: "active"}) {
		t.Errorf("state vector = %+v", plan.StateVector)
	}
	if plan.Phase != "mvp" {
		t.Errorf("phase = %q, want mvp", plan.Phase)
	}
	if !reflect.DeepEqual(plan.Classification, Classification{Dimension: "planning", Domain: "Planning"}) {
		t.Errorf("classification = %+v", plan.Classification)
	}
	if len(plan.ChangeLog) != 7 {
		t.Errorf("change log entries = %d, want 7", len(plan.ChangeLog))
	}
	if plan.ChangeLog[0] != (ChangeLogEntry{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering Architecture")}) {
		t.Errorf("first change log entry = %+v", plan.ChangeLog[0])
	}
	if plan.Content.Representation != StructuredJSON || plan.Content.File != "content" {
		t.Errorf("content ref = %+v", plan.Content)
	}

	// ADR unit: relationship carried by Identity.
	var adr Unit
	mustUnmarshal(t, entries, unitPath("eka-valid-fixture", "adr", "001-exchange", 1)+"/unit.json", &adr)
	wantRel := Relationship{Type: "depends-on", Target: "eka-valid-fixture/sto:login-email:1"}
	if len(adr.Relationships) != 1 || adr.Relationships[0] != wantRel {
		t.Errorf("adr relationships = %+v, want [%+v]", adr.Relationships, wantRel)
	}

	// Plan v2 supersedes v1: versioned relationship target.
	var plan2 Unit
	mustUnmarshal(t, entries, unitPath("eka-valid-fixture", "plan", "rilis-1", 2)+"/unit.json", &plan2)
	wantSupersedes := Relationship{Type: "supersedes", Target: "eka-valid-fixture/plan:rilis-1:1"}
	if len(plan2.Relationships) != 1 || plan2.Relationships[0] != wantSupersedes {
		t.Errorf("plan v2 relationships = %+v, want [%+v]", plan2.Relationships, wantSupersedes)
	}

	// Ticket unit: empty state vector block (present, empty), classification
	// empty (dimension-wise) but carries the derived Execution domain,
	// derives-from carried.
	var tkt Unit
	mustUnmarshal(t, entries, unitPath("eka-valid-fixture", "tkt", "sto-login-email", 1)+"/unit.json", &tkt)
	if tkt.StateVector != (StateVector{}) {
		t.Errorf("ticket state vector = %+v, want empty", tkt.StateVector)
	}
	if !reflect.DeepEqual(tkt.Classification, Classification{Domain: "Execution"}) {
		t.Errorf("ticket classification = %+v, want derived Execution domain", tkt.Classification)
	}
	wantDerives := Relationship{Type: "derives-from", Target: "eka-valid-fixture/ctr:gelombang-1:1"}
	if len(tkt.Relationships) != 1 || tkt.Relationships[0] != wantDerives {
		t.Errorf("ticket relationships = %+v, want [%+v]", tkt.Relationships, wantDerives)
	}

	// Content payloads are the canonical structured-json payloads
	// (compiled from the markdown bodies; spec-standard-v2 §3.3/§6).
	content := string(entries[unitPath("eka-valid-fixture", "tkt", "sto-login-email", 1)+"/content"])
	if !strings.Contains(content, "Generated \u2014 State Projection.") {
		t.Errorf("ticket content must carry the projection header, got:\n%s", content)
	}
	if !strings.Contains(content, "\"commands\":") {
		t.Errorf("ticket content must be the structured content object, got:\n%s", content)
	}
	if strings.Contains(content, "namespace: eka-valid-fixture") {
		t.Errorf("content must not contain frontmatter")
	}
	// The ADR content is the canonical JSON payload of the compiled
	// sections (camelCase keys, required sections first).
	adrContent := string(entries[unitPath("eka-valid-fixture", "adr", "001-exchange", 1)+"/content"])
	for _, want := range []string{"\"context\":", "\"decision\":", "\"consequences\":", "\"alternativesConsidered\":"} {
		if !strings.Contains(adrContent, want) {
			t.Errorf("adr content must carry %s, got:\n%s", want, adrContent)
		}
	}
	ctxIdx := strings.Index(adrContent, "\"context\"")
	altIdx := strings.Index(adrContent, "\"alternativesConsidered\"")
	if ctxIdx > altIdx || ctxIdx < 0 {
		t.Errorf("required section keys must precede extra keys (context before alternativesConsidered):\n%s", adrContent)
	}
}

// unitPath is the package path of a unit directory.
func unitPath(ns, typeToken, id string, iv int) string {
	return "units/" + ns + "/" + typeToken + "-" + id + "-v" + strconv.Itoa(iv)
}

// TestExportInvalidRepository: blocking violations refuse the export and
// no package file is produced.
func TestExportInvalidRepository(t *testing.T) {
	out := filepath.Join(t.TempDir(), "invalid.ekapkg")
	_, err := Export(fixtureInvalid, Options{Output: out})
	if err == nil {
		t.Fatal("export of an invalid repository must fail")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %T, want *ValidationError", err)
	}
	if ve.Report == nil || ve.Report.Pass() {
		t.Fatal("carried report must be non-passing")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("no package file may be produced for an invalid repository")
	}
	if !strings.Contains(ve.Error(), "export refused") {
		t.Errorf("error message = %q, want refusal wording", ve.Error())
	}
}

// TestExportSingleInstance: a versioned target exports exactly one unit.
func TestExportSingleInstance(t *testing.T) {
	out := filepath.Join(t.TempDir(), "instance.ekapkg")
	res := mustExport(t, fixtureValid, []string{"plan:rilis-1:2"}, out)
	if res.Label != "rsf-instance-eka-valid-fixture-"+SerializationVersion {
		t.Errorf("label = %q", res.Label)
	}
	if res.Units != 1 {
		t.Fatalf("units = %d, want 1", res.Units)
	}
	entries, _ := readZIP(t, out)
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	if manifest.Units[0].CanonicalIdentityForm != "eka-valid-fixture/plan:rilis-1:2" {
		t.Errorf("unit = %q", manifest.Units[0].CanonicalIdentityForm)
	}
	if manifest.Counts.Units != 1 {
		t.Errorf("counts.units = %d, want 1", manifest.Counts.Units)
	}
	// Instance scope carries the instance seed.
	wantSeeds := []string{"eka-valid-fixture/plan:rilis-1:2"}
	if !reflect.DeepEqual(manifest.Closure.Seeds, wantSeeds) {
		t.Errorf("seeds = %v, want %v", manifest.Closure.Seeds, wantSeeds)
	}
}

// TestExportSingleLine: an unversioned target exports every instance of
// the line.
func TestExportSingleLine(t *testing.T) {
	out := filepath.Join(t.TempDir(), "line.ekapkg")
	res := mustExport(t, fixtureValid, []string{"plan:rilis-1"}, out)
	if res.Label != "rsf-line-eka-valid-fixture-"+SerializationVersion {
		t.Errorf("label = %q", res.Label)
	}
	if res.Units != 2 {
		t.Fatalf("units = %d, want 2", res.Units)
	}
	entries, _ := readZIP(t, out)
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	var forms []string
	for _, u := range manifest.Units {
		forms = append(forms, u.CanonicalIdentityForm)
	}
	want := []string{"eka-valid-fixture/plan:rilis-1:1", "eka-valid-fixture/plan:rilis-1:2"}
	if !reflect.DeepEqual(forms, want) {
		t.Errorf("units = %v, want %v", forms, want)
	}
	// Line seed: cross-namespace line reference, no version defaulting.
	if !reflect.DeepEqual(manifest.Closure.Seeds, []string{"eka-valid-fixture/plan:rilis-1"}) {
		t.Errorf("seeds = %v", manifest.Closure.Seeds)
	}
}

// TestExportCollection: multiple targets export the union of the resolved
// lines/instances.
func TestExportCollection(t *testing.T) {
	out := filepath.Join(t.TempDir(), "collection.ekapkg")
	res := mustExport(t, fixtureValid, []string{"plan:rilis-1", "sto:login-email", "adr:001-exchange:1"}, out)
	if res.Label != "rsf-collection-eka-valid-fixture-"+SerializationVersion {
		t.Errorf("label = %q", res.Label)
	}
	if res.Units != 4 { // 2 plan instances + sto + adr instance
		t.Fatalf("units = %d, want 4", res.Units)
	}
	entries, _ := readZIP(t, out)
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	wantSeeds := []string{
		"eka-valid-fixture/adr:001-exchange:1",
		"eka-valid-fixture/plan:rilis-1",
		"eka-valid-fixture/sto:login-email",
	}
	if !reflect.DeepEqual(manifest.Closure.Seeds, wantSeeds) {
		t.Errorf("seeds = %v, want %v", manifest.Closure.Seeds, wantSeeds)
	}
}

// TestExportDeterministic: two exports of the same repository state are
// byte-identical; zip entries are sorted and carry zero timestamps.
func TestExportDeterministic(t *testing.T) {
	dir := t.TempDir()
	out1 := filepath.Join(dir, "a.ekapkg")
	out2 := filepath.Join(dir, "b.ekapkg")
	mustExport(t, fixtureValid, nil, out1)
	mustExport(t, fixtureValid, nil, out2)

	data1, err := os.ReadFile(out1)
	if err != nil {
		t.Fatal(err)
	}
	data2, err := os.ReadFile(out2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data1, data2) {
		t.Error("two exports of identical state must be byte-identical")
	}

	// Entry order must be sorted.
	_, order := readZIP(t, out1)
	if !sort.StringsAreSorted(order) {
		t.Errorf("zip entries must be sorted, got %v", order)
	}

	// Timestamps must be zero (no variable time in the container).
	r, err := zip.OpenReader(out1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, f := range r.File {
		if f.ModifiedDate != 0 || f.ModifiedTime != 0 {
			t.Errorf("entry %s carries a timestamp: date=%d time=%d", f.Name, f.ModifiedDate, f.ModifiedTime)
		}
	}
}

// TestExportAttachments: non-.md files under docs/ are carried verbatim
// with a digest in the Integrity Block.
func TestExportAttachments(t *testing.T) {
	out := filepath.Join(t.TempDir(), "att.ekapkg")
	res := mustExport(t, fixtureValid, nil, out)
	if res.Attachments != 1 {
		t.Fatalf("attachments = %d, want 1", res.Attachments)
	}
	entries, _ := readZIP(t, out)

	const attID = "docs/architecture/diagram.txt"
	payload, ok := entries["attachments/"+attID]
	if !ok {
		t.Fatalf("attachment %s missing from package", attID)
	}
	want, err := os.ReadFile(filepath.Join(fixtureValid, attID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, want) {
		t.Error("attachment payload must be carried verbatim")
	}

	var integrity Integrity
	mustUnmarshal(t, entries, "integrity.json", &integrity)
	sum := sha256.Sum256(want)
	wantDigest := hex.EncodeToString(sum[:])
	if len(integrity.Attachments) != 1 || integrity.Attachments[0].ID != attID ||
		integrity.Attachments[0].Digest != wantDigest {
		t.Errorf("integrity attachments = %+v, want digest %s", integrity.Attachments, wantDigest)
	}

	// Manifest counts the attachment.
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	if manifest.Counts.Attachments != 1 {
		t.Errorf("counts.attachments = %d, want 1", manifest.Counts.Attachments)
	}
}

// TestPackageIntegrity: recompute every digest from the produced package
// and verify it against the recorded values.
func TestPackageIntegrity(t *testing.T) {
	out := filepath.Join(t.TempDir(), "integ.ekapkg")
	mustExport(t, fixtureValid, nil, out)
	entries, order := readZIP(t, out)

	var integrity Integrity
	mustUnmarshal(t, entries, "integrity.json", &integrity)

	// Package digest: SHA-256 over every entry in sorted order except
	// manifest.json and integrity.json (documented deviation 5: the
	// manifest echoes the digest, so it is excluded from its own input).
	var buf bytes.Buffer
	for _, name := range order {
		if name == "integrity.json" || name == "manifest.json" {
			continue
		}
		buf.Write(entries[name])
	}
	sum := sha256.Sum256(buf.Bytes())
	if got := hex.EncodeToString(sum[:]); got != integrity.PackageDigest {
		t.Errorf("package digest = %s, want %s", integrity.PackageDigest, got)
	}
	// The manifest echoes the authoritative package digest.
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	if manifest.PackageDigest != integrity.PackageDigest {
		t.Errorf("manifest package_digest = %q, want the integrity value %q",
			manifest.PackageDigest, integrity.PackageDigest)
	}

	// Per-unit digests: unit.json || content.
	if len(manifest.Units) != len(integrity.Units) {
		t.Fatalf("manifest units = %d, integrity units = %d", len(manifest.Units), len(integrity.Units))
	}
	for i, u := range manifest.Units {
		if integrity.Units[i].CanonicalIdentityForm != u.CanonicalIdentityForm {
			t.Fatalf("integrity order mismatch at %d: %s vs %s",
				i, integrity.Units[i].CanonicalIdentityForm, u.CanonicalIdentityForm)
		}
		if integrity.Units[i].Digest != u.UnitDigest {
			t.Errorf("manifest/integrity digest mismatch for %s", u.CanonicalIdentityForm)
		}
		var d bytes.Buffer
		d.Write(entries[u.ContentFile[:strings.LastIndex(u.ContentFile, "/")]+"/unit.json"])
		d.Write(entries[u.ContentFile])
		s := sha256.Sum256(d.Bytes())
		if got := hex.EncodeToString(s[:]); got != u.UnitDigest {
			t.Errorf("recomputed digest for %s = %s, want %s", u.CanonicalIdentityForm, got, u.UnitDigest)
		}
	}
}

// TestExternalReferenceDeclaration: a relationship target outside the
// exported scope is declared in declarations.json.
func TestExternalReferenceDeclaration(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ext.ekapkg")
	res := mustExport(t, fixtureValid, []string{"adr:001-exchange"}, out)
	if res.ExternalReferences != 1 {
		t.Fatalf("externals = %d, want 1", res.ExternalReferences)
	}
	entries, _ := readZIP(t, out)
	var declarations Declarations
	mustUnmarshal(t, entries, "declarations.json", &declarations)
	want := []ExternalReference{{
		Source: "eka-valid-fixture/adr:001-exchange:1",
		Type:   "depends-on",
		Target: "eka-valid-fixture/sto:login-email:1",
	}}
	if !reflect.DeepEqual(declarations.ExternalReferences, want) {
		t.Errorf("external references = %+v, want %+v", declarations.ExternalReferences, want)
	}

	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	if manifest.Counts.ExternalReferences != 1 {
		t.Errorf("counts.external_references = %d, want 1", manifest.Counts.ExternalReferences)
	}

	// The unit still carries the relationship; the target is just not in
	// the package.
	var adr Unit
	mustUnmarshal(t, entries, unitPath("eka-valid-fixture", "adr", "001-exchange", 1)+"/unit.json", &adr)
	if len(adr.Relationships) != 1 {
		t.Errorf("relationships = %+v", adr.Relationships)
	}
}

// TestExportDirectoryMode: --output naming an existing directory (or a
// path ending in a separator) writes the directory layout with the same
// logical structure as the ZIP container.
func TestExportDirectoryMode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output func(t *testing.T) string
	}{
		{"existing directory", func(t *testing.T) string {
			dir := filepath.Join(t.TempDir(), "pkgdir")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
		{"trailing separator", func(t *testing.T) string {
			base := t.TempDir()
			return base + string(os.PathSeparator) // non-existing path + separator
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.output(t)
			res := mustExport(t, fixtureValid, nil, dir)
			if !res.Directory {
				t.Fatal("export must report directory mode")
			}

			// The directory layout mirrors the zip entries.
			zipOut := filepath.Join(t.TempDir(), "mirror.ekapkg")
			mustExport(t, fixtureValid, nil, zipOut)
			zipEntries, _ := readZIP(t, zipOut)

			for name, want := range zipEntries {
				got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
				if err != nil {
					t.Fatalf("directory layout missing %s: %v", name, err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("entry %s differs between zip and directory layout", name)
				}
			}
			// No extra files.
			walked := map[string]bool{}
			filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					rel, _ := filepath.Rel(dir, path)
					walked[filepath.ToSlash(rel)] = true
				}
				return nil
			})
			for name := range zipEntries {
				if !walked[name] {
					t.Errorf("directory layout must contain exactly the zip entries; missing %s", name)
				}
			}
			if len(walked) != len(zipEntries) {
				t.Errorf("directory layout has %d files, zip has %d", len(walked), len(zipEntries))
			}
		})
	}
}

// TestExportAutoName: with no --output the package is auto-named
// <label>.ekapkg in the current directory.
func TestExportAutoName(t *testing.T) {
	absFixture, err := filepath.Abs(fixtureValid)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	res := mustExport(t, absFixture, nil, "")
	want := filepath.Join(dir, "rsf-repo-eka-valid-fixture-"+SerializationVersion+PackageExtension)
	if res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("auto-named package missing: %v", err)
	}
}

// TestExportTargetErrors: malformed targets, unknown types and missing
// artifacts are usage errors (exit code 2) with clear messages.
func TestExportTargetErrors(t *testing.T) {
	tests := []struct {
		name    string
		root    string
		target  string
		wantMsg string
	}{
		{"malformed", fixtureValid, ":::", "invalid export target"},
		{"unknown type", fixtureValid, "bogus:1", "unknown type token"},
		{"missing artifact", fixtureValid, "sto:missing", "does not exist"},
		{"missing artifact lists type", fixtureValid, "sto:missing", "available sto artifacts"},
		{"versioned missing", fixtureValid, "plan:rilis-1:9", "does not exist"},
		{"empty id", fixtureValid, "sto:", "invalid export target"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Export(tc.root, Options{Targets: []string{tc.target}, Output: filepath.Join(t.TempDir(), "x.ekapkg")})
			if err == nil {
				t.Fatalf("target %q must fail", tc.target)
			}
			if !IsUsageError(err) {
				t.Fatalf("error = %T, want *UsageError: %v", err, err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("message %q must contain %q", err.Error(), tc.wantMsg)
			}
		})
	}

	// Unknown type on a collection target too.
	_, err := Export(fixtureValid, Options{Targets: []string{"adr:001-exchange", "nope:1"}, Output: filepath.Join(t.TempDir(), "x.ekapkg")})
	if err == nil || !IsUsageError(err) {
		t.Fatalf("collection with bad target must be a usage error, got %v", err)
	}
}

// TestExportAmbiguousTarget: a same-namespace target that exists in
// several namespaces is rejected with the candidate list.
func TestExportAmbiguousTarget(t *testing.T) {
	// Build a two-namespace repo where both namespaces hold the same
	// (type, id) pair. Rule 6 requires the dimension to match the home
	// folder, so each copy lives in its own dimension folder.
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/decisions/adr-001-shared.md", `---
namespace: ns-one
type: adr
id: 001-shared
instance-version: 1
revision: 1
content-state: accepted
existence-state: active
dimension: decisions
author: A
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on: []
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: A
  - date: 2026-08-05
    domain: content-state
    from: proposed
    to: accepted
    by: A
---

# ADR-001 — Shared (one)

## Context

C

## Decision

D

## Consequences

Co

## Alternatives Considered

A
`)
	write("docs/research/adr-001-shared.md", `---
namespace: ns-two
type: adr
id: 001-shared
instance-version: 1
revision: 1
content-state: accepted
existence-state: active
dimension: research
author: A
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on: []
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: A
  - date: 2026-08-05
    domain: content-state
    from: proposed
    to: accepted
    by: A
---

# ADR-001 — Shared (two)

## Context

C

## Decision

D

## Consequences

Co

## Alternatives Considered

A
`)

	_, err := Export(dir, Options{Targets: []string{"adr:001-shared"}, Output: filepath.Join(t.TempDir(), "x.ekapkg")})
	if err == nil || !IsUsageError(err) {
		t.Fatalf("ambiguous target must be a usage error, got %v", err)
	}
	if !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "ns-one") || !strings.Contains(err.Error(), "ns-two") {
		t.Errorf("message must list the candidate namespaces, got %q", err.Error())
	}
}

// TestExportScopeOverride: an explicit Options.Scope contradicting the
// targets is a usage error.
func TestExportScopeOverride(t *testing.T) {
	_, err := Export(fixtureValid, Options{Targets: nil, Scope: ScopeLine, Output: filepath.Join(t.TempDir(), "x.ekapkg")})
	if err == nil || !IsUsageError(err) {
		t.Fatalf("contradicting scope must be a usage error, got %v", err)
	}
	if !strings.Contains(err.Error(), "contradicts") {
		t.Errorf("message = %q", err.Error())
	}

	// Matching explicit scope is accepted.
	res := mustExport(t, fixtureValid, nil, filepath.Join(t.TempDir(), "ok.ekapkg"))
	if res.Package.Header.ExportScope != ScopeRepository {
		t.Errorf("scope = %q", res.Package.Header.ExportScope)
	}
}

// TestNamespaceHandling: single-namespace packages use that namespace;
// multi-namespace packages use the lexicographically smallest.
func TestNamespaceHandling(t *testing.T) {
	// Single namespace.
	out := filepath.Join(t.TempDir(), "single.ekapkg")
	res := mustExport(t, fixtureValid, nil, out)
	if res.Package.Header.Namespace != "eka-valid-fixture" {
		t.Errorf("namespace = %q", res.Package.Header.Namespace)
	}
	entries, _ := readZIP(t, out)
	var header Header
	mustUnmarshal(t, entries, "header.json", &header)
	if header.Namespace != "eka-valid-fixture" || header.PackageIdentityLabel != res.Label {
		t.Errorf("header = %+v", header)
	}

	// Multi-namespace: label uses the lexicographically smallest.
	out2 := filepath.Join(t.TempDir(), "multi.ekapkg")
	res2 := mustExport(t, fixtureMultiNS, nil, out2)
	if res2.Label != "rsf-repo-ns-alpha-"+SerializationVersion {
		t.Errorf("multi-ns label = %q, want rsf-repo-ns-alpha-1", res2.Label)
	}
	if res2.Units != 2 {
		t.Errorf("multi-ns units = %d, want 2", res2.Units)
	}
	entries2, _ := readZIP(t, out2)
	mustUnmarshal(t, entries2, "header.json", &header)
	if header.Namespace != "ns-alpha" {
		t.Errorf("multi-ns header namespace = %q, want ns-alpha", header.Namespace)
	}

	// Cross-namespace target resolves the line.
	out3 := filepath.Join(t.TempDir(), "cross.ekapkg")
	res3 := mustExport(t, fixtureMultiNS, []string{"ns-beta/spec:001-beta"}, out3)
	if res3.Units != 1 || res3.Label != "rsf-line-ns-beta-"+SerializationVersion {
		t.Errorf("cross-ns export: units=%d label=%q", res3.Units, res3.Label)
	}
}

// TestHeaderContractFacts: the header declares the version triple and the
// exporter identity.
func TestHeaderContractFacts(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hdr.ekapkg")
	mustExport(t, fixtureValid, nil, out)
	entries, _ := readZIP(t, out)
	var header Header
	mustUnmarshal(t, entries, "header.json", &header)
	if header.SerializationVersion != SerializationVersion || header.ExchangeFormatVersion != "1" ||
		header.SpecificationVersion != "1.1" || header.Exporter != "eka" {
		t.Errorf("header facts = %+v", header)
	}
	// Manifest echoes the same versions (RSF §8.1 self-consistency).
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	if manifest.SerializationVersion != header.SerializationVersion ||
		manifest.ExchangeFormatVersion != header.ExchangeFormatVersion ||
		manifest.SpecificationVersion != header.SpecificationVersion {
		t.Error("manifest must echo the header version facts")
	}
}

// TestNoTimestampsInPackage: JSON blocks must not carry any timestamp
// field, and the header must omit the creation timestamp (v1
// byte-determinism decision, RSF §4.3).
func TestNoTimestampsInPackage(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ts.ekapkg")
	mustExport(t, fixtureValid, nil, out)
	entries, _ := readZIP(t, out)
	header, ok := entries["header.json"]
	if !ok {
		t.Fatal("header.json missing")
	}
	if strings.Contains(string(header), "timestamp") || strings.Contains(string(header), "created") {
		t.Errorf("header must not carry creation metadata:\n%s", header)
	}
}

// TestResultModelMatchesBytes: the returned Result.Package reflects the
// written package.
func TestResultModelMatchesBytes(t *testing.T) {
	out := filepath.Join(t.TempDir(), "res.ekapkg")
	res := mustExport(t, fixtureValid, nil, out)
	if res.Package == nil || res.Validation == nil {
		t.Fatal("result must carry the package and validation report")
	}
	if !res.Validation.Pass() {
		t.Fatal("validation gate must pass")
	}
	if res.Package.Header.PackageIdentityLabel != res.Label {
		t.Errorf("label mismatch: %q vs %q", res.Package.Header.PackageIdentityLabel, res.Label)
	}
	if len(res.Package.Manifest.Units) != 6 || len(res.Package.Integrity.Units) != 6 {
		t.Error("manifest/integrity unit lists must match the package")
	}
	if res.Package.Integrity.PackageDigest == "" {
		t.Error("package digest must be computed")
	}
}
