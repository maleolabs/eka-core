package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

// This file tests the additive read-only APIs: Scan (classification
// without rule evaluation) and the exported ParseReference. Both must not
// affect Validate behavior — the existing validate/parse tests cover that
// contract.

// TestScanValidFixture verifies Scan classifies the shared valid fixture
// exactly like Validate does.
func TestScanValidFixture(t *testing.T) {
	artifacts, err := Scan(filepath.Join("testdata", "valid"))
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 6 {
		t.Fatalf("artifacts = %d, want 6", len(artifacts))
	}
	// The scanned artifacts carry the parsed fields (including the
	// additive Author metadata).
	found := map[string]bool{}
	for _, a := range artifacts {
		found[a.Type+":"+a.ID] = true
		if a.Author == "" {
			t.Errorf("%s:%s: author must be parsed from frontmatter", a.Type, a.ID)
		}
		if a.Namespace != "eka-valid-fixture" {
			t.Errorf("%s:%s: namespace = %q", a.Type, a.ID, a.Namespace)
		}
	}
	for _, want := range []string{"adr:001-login-serialization", "ctr:gelombang-1", "plan:rilis-1", "sto:login-email", "tkt:sto-login-email"} {
		if !found[want] {
			t.Errorf("missing artifact %s in scan result", want)
		}
	}
}

// TestScanSkipsTestdataAndDotDirs verifies the shared scan policy: files
// under testdata/ and dot-directories are not classified.
func TestScanSkipsTestdataAndDotDirs(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	artifact := `---
namespace: eka-test
type: adr
id: 001-a
instance-version: 1
revision: 1
content-state: accepted
existence-state: active
dimension: decisions
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

# A

## Context

C

## Decision

D

## Consequences

Co

## Alternatives Considered

A
`
	write("docs/decisions/adr-001-a.md", artifact)
	write("docs/testdata/adr-hidden.md", artifact) // testdata dir: skipped
	write("docs/.hidden/adr-hidden2.md", artifact) // dot dir: skipped

	artifacts, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts = %d, want 1 (testdata and dot-dirs must be skipped)", len(artifacts))
	}
	if artifacts[0].RelPath != filepath.Join("docs", "decisions", "adr-001-a.md") {
		t.Errorf("artifact path = %q", artifacts[0].RelPath)
	}
}

// TestScanConventionDocsOnly verifies a repository without artifacts
// yields an empty result.
func TestScanConventionDocsOnly(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# Docs\n")
	write("docs/exchange/protocol.md", "---\nprotocol: v1\n---\n# Protocol\n")
	artifacts, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("artifacts = %d, want 0", len(artifacts))
	}
}

// TestScanStructuralMismatch verifies the R0 type-XOR-id case is excluded
// from Scan results (Scan is classification-only; Validate reports it).
func TestScanStructuralMismatch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "decisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "---\nnamespace: eka-test\ntype: adr\n---\n# Bad\n"
	if err := os.WriteFile(filepath.Join(root, "docs", "decisions", "adr-bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("artifacts = %d, want 0 (type XOR id is not an artifact)", len(artifacts))
	}
}

// TestScanBadRoot verifies the same root errors as Validate.
func TestScanBadRoot(t *testing.T) {
	if _, err := Scan(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("nonexistent root must fail")
	}
	file := filepath.Join(t.TempDir(), "file.md")
	if err := os.WriteFile(file, []byte("# x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(file); err == nil {
		t.Error("file root must fail")
	}
}

// TestParseReferenceExported verifies the exported wrapper keeps the
// reference grammar semantics.
func TestParseReferenceExported(t *testing.T) {
	tests := []struct {
		in    string
		ns    string
		typ   string
		want  reference
		errIs bool
	}{
		{"sto:login-email", "eka-ns", "sto", reference{Namespace: "eka-ns", Type: "sto", ID: "login-email"}, false},
		{"sto:login-email:2", "eka-ns", "sto", reference{Namespace: "eka-ns", Type: "sto", ID: "login-email", Version: 2, HasVersion: true}, false},
		{"other-ns/sto:login-email", "eka-ns", "sto", reference{Namespace: "other-ns", Type: "sto", ID: "login-email"}, false},
		{"bogus:1", "eka-ns", "sto", reference{}, true},
		{"", "eka-ns", "sto", reference{}, true},
	}
	for _, tc := range tests {
		got, err := ParseReference(tc.in, tc.ns, tc.typ)
		if tc.errIs {
			if err == nil {
				t.Errorf("ParseReference(%q) must fail", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseReference(%q) failed: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseReference(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}
