package metadata

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validYAML is the canonical valid eka.yaml used across the tests.
const validYAML = "version: 1\nproject: atrium\nname: api\nnamespace: atrium-api\n"

// TestSchemaVersionConstant: the exported SchemaVersion is the single
// source of the eka.yaml schema version — Parse accepts exactly that
// version and refuses every other one. Locking the value keeps the
// constant and the parser in lockstep (a wrong constant would fail the
// accept/refuse pair below).
func TestSchemaVersionConstant(t *testing.T) {
	if SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1 (the ratified eka.yaml schema version)", SchemaVersion)
	}
	if _, err := Parse([]byte(fmt.Sprintf("version: %d\nproject: atrium\nname: api\nnamespace: atrium-api\n", SchemaVersion))); err != nil {
		t.Errorf("Parse with SchemaVersion = %d: %v; want accept", SchemaVersion, err)
	}
	if _, err := Parse([]byte(fmt.Sprintf("version: %d\nproject: atrium\nname: api\nnamespace: atrium-api\n", SchemaVersion+1))); err == nil {
		t.Errorf("Parse with SchemaVersion+1 = %d must refuse", SchemaVersion+1)
	}
}

// TestParseValid: a well-formed eka.yaml decodes into the metadata
// triple.
func TestParseValid(t *testing.T) {
	m, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Version != 1 || m.Project != "atrium" || m.Name != "api" || m.Namespace != "atrium-api" {
		t.Errorf("metadata = %+v, want version 1 / atrium / api / atrium-api", m)
	}
}

// TestParseValidation is the validation table: every malformed input
// must be refused with a deterministic "metadata: " prefixed error.
func TestParseValidation(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string // substring of the error; "" = must not error
	}{
		{"valid", validYAML, ""},
		{"missing version", "project: atrium\nname: api\nnamespace: atrium-api\n",
			"metadata: eka.yaml version must be 1, got 0"},
		{"wrong version", "version: 2\nproject: atrium\nname: api\nnamespace: atrium-api\n",
			"metadata: eka.yaml version must be 1, got 2"},
		{"version zero", "version: 0\nproject: atrium\nname: api\nnamespace: atrium-api\n",
			"metadata: eka.yaml version must be 1, got 0"},
		{"missing project", "version: 1\nname: api\nnamespace: atrium-api\n",
			`metadata: eka.yaml field "project" is required and must not be empty`},
		{"missing name", "version: 1\nproject: atrium\nnamespace: atrium-api\n",
			`metadata: eka.yaml field "name" is required and must not be empty`},
		{"missing namespace", "version: 1\nproject: atrium\nname: api\n",
			`metadata: eka.yaml field "namespace" is required and must not be empty`},
		{"empty project", "version: 1\nproject: \"\"\nname: api\nnamespace: atrium-api\n",
			`metadata: eka.yaml field "project" is required and must not be empty`},
		{"uppercase", "version: 1\nproject: Atrium\nname: api\nnamespace: atrium-api\n",
			`metadata: eka.yaml field "project" must match ^[a-z0-9]+(-[a-z0-9]+)*$`},
		{"underscore", "version: 1\nproject: atrium\nname: api\nnamespace: atrium_api\n",
			`metadata: eka.yaml field "namespace" must match ^[a-z0-9]+(-[a-z0-9]+)*$`},
		{"leading hyphen", "version: 1\nproject: atrium\nname: -api\nnamespace: atrium-api\n",
			`metadata: eka.yaml field "name" must match ^[a-z0-9]+(-[a-z0-9]+)*$`},
		{"trailing hyphen", "version: 1\nproject: atrium\nname: api-\nnamespace: atrium-api\n",
			`metadata: eka.yaml field "name" must match ^[a-z0-9]+(-[a-z0-9]+)*$`},
		{"double hyphen", "version: 1\nproject: atrium\nname: api\nnamespace: atrium--api\n",
			`metadata: eka.yaml field "namespace" must match ^[a-z0-9]+(-[a-z0-9]+)*$`},
		{"reserved name runtime", "version: 1\nproject: atrium\nname: runtime\nnamespace: atrium-api\n",
			`metadata: eka.yaml name "runtime" is reserved for workspace-native knowledge`},
		{"duplicate version key", "version: 1\nversion: 2\nproject: atrium\nname: api\nnamespace: atrium-api\n",
			"metadata: cannot parse eka.yaml: yaml: unmarshal errors:"},
		{"duplicate namespace key", "version: 1\nproject: atrium\nname: api\nnamespace: atrium-api\nnamespace: atrium-web\n",
			"metadata: cannot parse eka.yaml: yaml: unmarshal errors:"},
		{"unknown top-level key", "version: 1\nproject: atrium\nname: api\nnamespace: atrium-api\nbogus: x\n",
			"metadata: cannot parse eka.yaml: yaml: unmarshal errors:"},
		{"non-yaml garbage", "not yaml: [unclosed\n",
			"metadata: cannot parse eka.yaml:"},
		{"empty file", "", "metadata: eka.yaml is empty"},
		{"whitespace only", "\n\n", "metadata: eka.yaml is empty"},
		{"multiple documents", "version: 1\nproject: atrium\nname: api\nnamespace: atrium-api\n---\nversion: 2\n",
			"metadata: eka.yaml must contain exactly one YAML document"},
		{"string version", "version: \"1\"\nproject: atrium\nname: api\nnamespace: atrium-api\n",
			"metadata: cannot parse eka.yaml:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse must succeed, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Parse must error")
			}
			if !strings.HasPrefix(err.Error(), "metadata: ") {
				t.Errorf("error must be prefixed \"metadata: \", got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestValidIdent is the shared identifier rule table: the same pattern
// as bootstrap.IsValidIdent (duplicated by design — the rule is locked
// by tests in both packages so they cannot drift).
func TestValidIdent(t *testing.T) {
	valid := []string{"a", "0", "api", "atrium-api", "a0-b1", "a-b-c", "ekarefimpl", "x9"}
	for _, s := range valid {
		if !ValidIdent(s) {
			t.Errorf("ValidIdent(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "-", "A", "Atrium", "a_b", "-a", "a-", "a--b", "a b", "a/b", "a:b", "a-b-", " runtime", "runtime "}
	for _, s := range invalid {
		if ValidIdent(s) {
			t.Errorf("ValidIdent(%q) = true, want false", s)
		}
	}
}

// TestMarshalRoundTrip: Marshal renders the canonical eka.yaml bytes
// (the exact format bootstrap generates and the ADR-020 alignment
// rewrites) and the output round-trips through Parse byte-for-byte.
func TestMarshalRoundTrip(t *testing.T) {
	m, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	got := string(m.Marshal())
	if got != validYAML {
		t.Errorf("Marshal = %q, want %q (the canonical byte format)", got, validYAML)
	}
	// Round-trip: parsing the rendered bytes reproduces the metadata.
	back, err := Parse(m.Marshal())
	if err != nil {
		t.Fatalf("Parse(Marshal): %v", err)
	}
	if back != m {
		t.Errorf("round-trip = %+v, want %+v", back, m)
	}
}

// TestMarshalAlignmentRewrite: the alignment rewrite (ADR-020) keeps
// version/project/name and replaces only the namespace; the rendered
// file stays in the canonical format.
func TestMarshalAlignmentRewrite(t *testing.T) {
	m, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	aligned := Metadata{Version: m.Version, Project: m.Project, Name: m.Name, Namespace: "feather"}
	got := string(aligned.Marshal())
	want := "version: 1\nproject: atrium\nname: api\nnamespace: feather\n"
	if got != want {
		t.Errorf("Marshal = %q, want %q", got, want)
	}
	if _, err := Parse(aligned.Marshal()); err != nil {
		t.Errorf("aligned bytes must parse: %v", err)
	}
}

// writeEKA writes eka.yaml content into dir.
func writeEKA(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFindAtStartingDir: eka.yaml at the starting directory is found
// directly, and the returned directory is the starting directory.
func TestFindAtStartingDir(t *testing.T) {
	dir := t.TempDir()
	writeEKA(t, dir, validYAML)

	m, foundDir, found, err := Find(dir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !found {
		t.Fatal("Find must find eka.yaml at the starting dir")
	}
	if foundDir != filepath.Clean(dir) {
		t.Errorf("found dir = %q, want %q", foundDir, filepath.Clean(dir))
	}
	if m.Project != "atrium" || m.Name != "api" || m.Namespace != "atrium-api" {
		t.Errorf("metadata = %+v", m)
	}
}

// TestFindFromSubdirectory: a nested directory resolves via the walk-up
// to the repository root's eka.yaml.
func TestFindFromSubdirectory(t *testing.T) {
	dir := t.TempDir()
	writeEKA(t, dir, validYAML)
	sub := filepath.Join(dir, "docs", "architecture")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	m, foundDir, found, err := Find(sub)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !found {
		t.Fatal("Find must find eka.yaml from a subdirectory")
	}
	if foundDir != filepath.Clean(dir) {
		t.Errorf("found dir = %q, want the repository root %q", foundDir, filepath.Clean(dir))
	}
	if m.Name != "api" {
		t.Errorf("name = %q, want api", m.Name)
	}
}

// TestFindNestedRepoNearestWins: an outer repository with eka.yaml and
// an inner directory with its own eka.yaml — the inner (nearest) file
// wins.
func TestFindNestedRepoNearestWins(t *testing.T) {
	outer := t.TempDir()
	writeEKA(t, outer, validYAML)
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	innerYAML := "version: 1\nproject: feather\nname: inner\nnamespace: feather-inner\n"
	writeEKA(t, inner, innerYAML)
	deep := filepath.Join(inner, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	m, foundDir, found, err := Find(deep)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !found {
		t.Fatal("Find must find the inner eka.yaml")
	}
	if foundDir != filepath.Clean(inner) {
		t.Errorf("found dir = %q, want the inner repo root %q", foundDir, filepath.Clean(inner))
	}
	if m.Project != "feather" || m.Name != "inner" || m.Namespace != "feather-inner" {
		t.Errorf("metadata = %+v, want the inner identity", m)
	}
}

// TestFindNone: walking to the filesystem root finds no eka.yaml — the
// result is (metadata{}, "", false, nil). The walk may traverse up to
// the filesystem root; only found==false is asserted (no eka.yaml
// exists above the temp dirs in this environment).
func TestFindNone(t *testing.T) {
	dir := t.TempDir()
	m, foundDir, found, err := Find(dir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found {
		t.Error("Find must report not found")
	}
	if foundDir != "" {
		t.Errorf("found dir = %q, want \"\"", foundDir)
	}
	if m != (Metadata{}) {
		t.Errorf("metadata = %+v, want zero value", m)
	}
}

// TestFindParseErrorPropagates: a broken eka.yaml at any level of the
// walk is an error (the file exists, so its content is authoritative) —
// the walk never silently skips it.
func TestFindParseErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	writeEKA(t, dir, "not yaml: [unclosed\n")
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{dir, sub} {
		if _, _, found, err := Find(d); err == nil || found {
			t.Errorf("Find(%s) = found %v, err %v; want a parse error", d, found, err)
		} else if !strings.HasPrefix(err.Error(), "metadata: ") {
			t.Errorf("error must be prefixed \"metadata: \", got: %v", err)
		}
	}
}

// TestFindCleansRelativePaths: a relative starting directory resolves
// against the working directory before the walk.
func TestFindCleansRelativePaths(t *testing.T) {
	dir := t.TempDir()
	writeEKA(t, dir, validYAML)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	m, foundDir, found, err := Find(".")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !found || foundDir != filepath.Clean(dir) {
		t.Errorf("found %v, dir %q; want found with %q", found, foundDir, filepath.Clean(dir))
	}
	if m.Name != "api" {
		t.Errorf("name = %q, want api", m.Name)
	}
}
