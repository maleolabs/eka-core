package exchange

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maleolabs/eka-core/conformance"
)

// This file covers the export security surface: the identity charset guard
// (MAJOR path-traversal fix), the writer's entry-name defense in depth,
// tamper detection, draft tolerance for dangling references, and the
// closure seed deduplication.

// writeRepoFile creates one file under dir, creating parent directories.
func writeRepoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// validADRBody is a minimal, fully compliant ADR body used by fixtures.
const validADRBody = `---
namespace: %s
type: adr
id: %s
instance-version: 1
revision: 1
content-state: accepted
existence-state: active
dimension: decisions
author: A
created: 2026-08-05
updated: 2026-08-05
depends-on: []
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: A
  - date: 2026-08-05
    domain: content-state
    from: "-"
    to: accepted
    by: A
---

# ADR — fixture

## Context

C

## Decision

D

## Consequences

Co

## Alternatives Considered

A
`

// --- MAJOR: identity charset guard (path traversal) ---------------------

// TestExportIdentityCharsetGuard: identity components that would escape the
// package root (or break the RSF §5.2.3 component charset) refuse the
// export before anything is written. The conformance gate does not catch
// these (documented Rule 2 gap: the filename id segment is not checked
// against the frontmatter id), so the exchange layer must.
func TestExportIdentityCharsetGuard(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		id        string
		wantComp  string
	}{
		{"id traversal", "eka-test", "../../evil-escape", "../../evil-escape"},
		{"id deep traversal", "eka-test", "../../../../evil-escape", "../../../../evil-escape"},
		{"id backslash", "eka-test", `..\evil`, `..\evil`},
		{"id whitespace", "eka-test", "evil escape", "evil escape"},
		{"id leading dot", "eka-test", ".hidden", ".hidden"},
		{"id trailing dot", "eka-test", "evil.", "evil."},
		{"id dot segment", "eka-test", "a..b", "a..b"},
		{"namespace slash", "a/b", "001-evil", "a/b"},
		{"namespace traversal", "../evil", "001-evil", "../evil"},
		{"namespace dot", "ns.", "001-evil", "ns."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeRepoFile(t, repo, "docs/decisions/adr-001-evil.md",
				fmt.Sprintf(validADRBody, tc.namespace, tc.id))

			out := filepath.Join(t.TempDir(), "evil.ekapkg")
			_, err := Export(repo, Options{Output: out})
			if err == nil {
				t.Fatal("export of a repository with a malicious identity component must fail")
			}
			var ce *ContentError
			if !errors.As(err, &ce) {
				t.Fatalf("error = %T, want *ContentError: %v", err, err)
			}
			// The message must name the artifact (canonical identity form)
			// and the offending component.
			form := tc.namespace + "/adr:" + tc.id + ":1"
			if !strings.Contains(ce.Error(), form) {
				t.Errorf("message must name the artifact %q, got %q", form, ce.Error())
			}
			if !strings.Contains(ce.Error(), tc.wantComp) {
				t.Errorf("message must name the offending component %q, got %q", tc.wantComp, ce.Error())
			}
			if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
				t.Error("no package file may be produced for a refused export")
			}
		})
	}
}

// TestExportGuardDirectoryModeNoEscape: the malicious-id refusal must also
// apply in directory mode — nothing may be written into or outside the
// output directory (the guard runs in the load phase, before any write).
func TestExportGuardDirectoryModeNoEscape(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "docs/decisions/adr-001-evil.md",
		fmt.Sprintf(validADRBody, "eka-test", "../../../../evil-escape"))

	outDir := filepath.Join(t.TempDir(), "pkgdir")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Export(repo, Options{Output: outDir})
	if err == nil {
		t.Fatal("export of a malicious repository must fail in directory mode")
	}
	var ce *ContentError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %T, want *ContentError: %v", err, err)
	}
	// Nothing written at all: the output directory stays empty.
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("output directory must stay empty, found %d entries", len(entries))
	}
	// No stray file outside the output directory (e.g. evil-escape-v1).
	parent := filepath.Dir(outDir)
	walked, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range walked {
		if e.Name() == filepath.Base(outDir) {
			continue
		}
		t.Errorf("unexpected file created outside the output directory: %s", e.Name())
	}
}

// TestCheckEntryName: the writer's entry-name contract (defense in depth).
// The identity guard runs before any write, so these names cannot reach
// the writer in the export pipeline; the validator is unit-tested directly
// with the crafted names an attacker would use.
func TestCheckEntryName(t *testing.T) {
	for _, name := range []string{
		"header.json",
		"manifest.json",
		"units/eka-test/adr-001-v1/unit.json",
		"units/eka-test/adr-001-v1/content",
		"attachments/docs/architecture/diagram.txt",
	} {
		if err := checkEntryName(name); err != nil {
			t.Errorf("valid entry name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{
		"",
		".",
		"..",
		"../evil",
		"units/../evil",
		"units/../../evil-escape-v1/unit.json",
		"/etc/passwd",
		"units\\evil",
		"units:evil",
		"units/..\\evil",
	} {
		if err := checkEntryName(name); err == nil {
			t.Errorf("malicious entry name %q must be rejected", name)
		}
	}
}

// TestWriteDirRejectsMaliciousEntries: writeDir refuses entries that would
// escape the package root and creates nothing.
func TestWriteDirRejectsMaliciousEntries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pkg")
	entries := []entry{
		{name: "units/../../evil-escape-v1/unit.json", data: []byte("{}")},
	}
	if err := writeDir(dir, entries); err == nil {
		t.Fatal("writeDir must reject an escaping entry name")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("no package directory may be created for a refused entry")
	}
}

// TestWriteDirStaysInsideRoot: every file writeDir produces lives inside
// the package root, byte-identical to the entry data.
func TestWriteDirStaysInsideRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pkg")
	entries := []entry{
		{name: "header.json", data: []byte("{}\n")},
		{name: "units/eka-test/adr-001-v1/unit.json", data: []byte("{}\n")},
		{name: "units/eka-test/adr-001-v1/content", data: []byte("body")},
		{name: "attachments/docs/a.txt", data: []byte("att")},
	}
	if err := writeDir(dir, entries); err != nil {
		t.Fatal(err)
	}
	walked := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			walked[filepath.ToSlash(rel)] = data
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(walked) != len(entries) {
		t.Fatalf("walked %d files, want %d", len(walked), len(entries))
	}
	for _, e := range entries {
		got, ok := walked[e.name]
		if !ok {
			t.Errorf("missing %s", e.name)
			continue
		}
		if !bytes.Equal(got, e.data) {
			t.Errorf("entry %s differs", e.name)
		}
	}
}

// TestWriteZIPRejectsMaliciousEntries: writeZIP refuses escaping entry
// names (zip-slip defense in depth).
func TestWriteZIPRejectsMaliciousEntries(t *testing.T) {
	out := filepath.Join(t.TempDir(), "evil.ekapkg")
	entries := []entry{
		{name: "units/../evil", data: []byte("x")},
	}
	if err := writeZIP(out, entries); err == nil {
		t.Fatal("writeZIP must reject an escaping entry name")
	}
}

// --- MINOR: tamper detection ---------------------------------------------

// verifyPackageIntegrity re-verifies a package file's integrity block
// against its own bytes: the package digest (SHA-256 over every entry in
// sorted name order except manifest.json and integrity.json) and every
// per-unit digest (unit.json || content). It collects every mismatch and
// returns nil only when the package is intact.
func verifyPackageIntegrity(path string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("cannot open package: %w", err)
	}
	defer r.Close()
	entries := map[string][]byte{}
	var order []string
	for _, f := range r.File {
		data, err := readZIPFile(f)
		if err != nil {
			return fmt.Errorf("cannot read zip entry %s: %w", f.Name, err)
		}
		entries[f.Name] = data
		order = append(order, f.Name)
	}

	var problems []string

	// Package digest.
	var integrity Integrity
	if err := json.Unmarshal(entries["integrity.json"], &integrity); err != nil {
		return fmt.Errorf("cannot decode integrity.json: %w", err)
	}
	var buf bytes.Buffer
	for _, name := range order {
		if name == "integrity.json" || name == "manifest.json" {
			continue
		}
		buf.Write(entries[name])
	}
	sum := sha256.Sum256(buf.Bytes())
	if got := hex.EncodeToString(sum[:]); got != integrity.PackageDigest {
		problems = append(problems, fmt.Sprintf(
			"package digest mismatch: recorded %s, recomputed %s", integrity.PackageDigest, got))
	}

	// Per-unit digests: unit.json || content.
	var manifest Manifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		return fmt.Errorf("cannot decode manifest.json: %w", err)
	}
	for _, u := range manifest.Units {
		dir := u.ContentFile[:strings.LastIndex(u.ContentFile, "/")]
		var d bytes.Buffer
		d.Write(entries[dir+"/unit.json"])
		d.Write(entries[u.ContentFile])
		s := sha256.Sum256(d.Bytes())
		if got := hex.EncodeToString(s[:]); got != u.UnitDigest {
			problems = append(problems, fmt.Sprintf(
				"unit digest mismatch for %s: recorded %s, recomputed %s",
				u.CanonicalIdentityForm, u.UnitDigest, got))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("integrity verification failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

// rewriteZIPTampered copies the package at src to dst, flipping one byte
// of the named entry (entry order and zero timestamps preserved).
func rewriteZIPTampered(t *testing.T, src, dst, target string) {
	t.Helper()
	entries, order := readZIP(t, src)
	data := append([]byte(nil), entries[target]...)
	data[0] ^= 0xFF
	entries[target] = data

	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, name := range order {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.Modified = time.Time{}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("cannot create zip entry %s: %v", name, err)
		}
		if _, err := w.Write(entries[name]); err != nil {
			t.Fatalf("cannot write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestExportContentTamperDetection: flip one byte in a content file and
// both the package digest and the unit digest must fail verification.
func TestExportContentTamperDetection(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pristine.ekapkg")
	mustExport(t, fixtureValid, nil, out)
	if err := verifyPackageIntegrity(out); err != nil {
		t.Fatalf("pristine package must verify: %v", err)
	}

	contentName := unitPath("eka-valid-fixture", "adr", "001-exchange", 1) + "/content"
	tampered := filepath.Join(t.TempDir(), "tampered.ekapkg")
	rewriteZIPTampered(t, out, tampered, contentName)

	err := verifyPackageIntegrity(tampered)
	if err == nil {
		t.Fatal("tampered package must fail integrity verification")
	}
	if !strings.Contains(err.Error(), "package digest") {
		t.Errorf("verification must report the package digest mismatch, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unit digest mismatch for eka-valid-fixture/adr:001-exchange:1") {
		t.Errorf("verification must name the tampered unit, got: %v", err)
	}
}

// --- MINOR: draft tolerance (dangling references) ------------------------

// TestExportDraftDanglingReference: a draft artifact may carry a dangling
// reference. The export succeeds; the dangling reference is neither
// serialized as a unit relationship nor declared external, and the
// validation report (carried in the result) notes the warning.
func TestExportDraftDanglingReference(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "docs/specifications/spec-001-draft.md", `---
namespace: eka-draft
type: spec
id: 001-draft
instance-version: 1
revision: 1
content-state: draft
existence-state: active
dimension: specifications
author: A
created: 2026-08-05
updated: 2026-08-05
depends-on:
  - sto:missing-target
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: A
  - date: 2026-08-05
    domain: content-state
    from: "-"
    to: draft
    by: A
---

# SPEC-001 — Draft

## Purpose

Draft purpose.

## Content

Draft body.
`)

	out := filepath.Join(t.TempDir(), "draft.ekapkg")
	res := mustExport(t, repo, nil, out) // exit-0 path: warnings never block.

	entries, _ := readZIP(t, out)

	// 1. The dangling reference is NOT in the unit relationships.
	var unit Unit
	mustUnmarshal(t, entries, unitPath("eka-draft", "spec", "001-draft", 1)+"/unit.json", &unit)
	if len(unit.Relationships) != 0 {
		t.Errorf("dangling reference must not be serialized as a relationship, got %+v", unit.Relationships)
	}

	// 2. The dangling reference is NOT declared external.
	var declarations Declarations
	mustUnmarshal(t, entries, "declarations.json", &declarations)
	if len(declarations.ExternalReferences) != 0 {
		t.Errorf("dangling reference must not be declared external, got %+v", declarations.ExternalReferences)
	}

	// 3. The result/warnings note it: the carried validation report
	// contains the draft warning naming the dangling reference.
	if res.Validation == nil {
		t.Fatal("result must carry the validation report")
	}
	found := false
	for _, r := range res.Validation.SortedResults() {
		if strings.Contains(r.Message, "unresolved reference") &&
			strings.Contains(r.Message, "sto:missing-target") {
			found = true
		}
	}
	if !found {
		t.Error("validation report must warn about the dangling reference; warnings: " +
			formatResults(res.Validation.SortedResults()))
	}
}

func formatResults(results []conformance.Result) string {
	var parts []string
	for _, r := range results {
		parts = append(parts, r.Message)
	}
	return strings.Join(parts, " | ")
}

// --- MINOR: closure seed deduplication -----------------------------------

// TestSeedDedupeInstanceFormWins: when the same line is specified both as
// a line reference and as an instance reference, the seed set collapses to
// the instance form (canonical, more precise; the unit set is unchanged).
func TestSeedDedupeInstanceFormWins(t *testing.T) {
	out := filepath.Join(t.TempDir(), "seed.ekapkg")
	res := mustExport(t, fixtureValid, []string{"plan:rilis-1", "plan:rilis-1:2"}, out)
	if res.Units != 2 {
		t.Fatalf("units = %d, want 2 (line union)", res.Units)
	}
	entries, _ := readZIP(t, out)
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	want := []string{"eka-valid-fixture/plan:rilis-1:2"}
	if !reflect.DeepEqual(manifest.Closure.Seeds, want) {
		t.Errorf("seeds = %v, want %v", manifest.Closure.Seeds, want)
	}
}

// TestSeedDedupeExactDuplicates: repeated targets collapse to one seed
// entry, and multiple instance seeds of the same line all stay.
func TestSeedDedupeExactDuplicates(t *testing.T) {
	out := filepath.Join(t.TempDir(), "seed.ekapkg")
	res := mustExport(t, fixtureValid,
		[]string{"plan:rilis-1:2", "plan:rilis-1:2", "plan:rilis-1:1", "plan:rilis-1"}, out)
	if res.Units != 2 {
		t.Fatalf("units = %d, want 2", res.Units)
	}
	entries, _ := readZIP(t, out)
	var manifest Manifest
	mustUnmarshal(t, entries, "manifest.json", &manifest)
	want := []string{
		"eka-valid-fixture/plan:rilis-1:1",
		"eka-valid-fixture/plan:rilis-1:2",
	}
	if !reflect.DeepEqual(manifest.Closure.Seeds, want) {
		t.Errorf("seeds = %v, want %v", manifest.Closure.Seeds, want)
	}
}
