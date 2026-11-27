package contexts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/maleolabs/eka-core/v2/exchange"
	"github.com/maleolabs/eka-core/v2/runtime"
)

// This file tests the Standard 1.5 path-to-constraint retrieval door
// (UnitsForPath) and its declaration extraction (exchange.AppliesTo, covered
// in the exchange package).

// pathJSON builds a minimal conformant JSON-native artifact declaring the
// given `appliesTo` patterns, for the fixture's repo namespace.
func pathJSON(t *testing.T, typeToken, id, dimension string, patterns []string) string {
	t.Helper()
	applies := make([]any, len(patterns))
	for i, p := range patterns {
		applies[i] = p
	}
	doc := map[string]any{
		"namespace":       "eka-view-fixture",
		"type":            typeToken,
		"id":              id,
		"instanceVersion": 1,
		"revision":        1,
		"state":           map[string]any{"contentState": "draft", "existenceState": "active"},
		"dimension":       dimension,
		"changeLog": []any{
			map[string]any{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering"},
			map[string]any{"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering"},
		},
		"content": map[string]any{"purpose": "p", "content": "c", "appliesTo": applies},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeRepoFile writes one file into the seeded repository tree.
func writeRepoFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	full := filepath.Join(repo, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedPathRepo seeds the fixture repo with three applying units across two
// authority strata: req- (Discovery, stratum 1) and two std- units
// (Architecture, stratum 2), all binding src/**.
func seedPathRepo(t *testing.T) {
	t.Helper()
	seedContextRepo(t, func(repo string) {
		writeRepoFile(t, repo, "docs/requirements/req-aaa.json",
			pathJSON(t, "req", "aaa", "requirements", []string{"src/**"}))
		writeRepoFile(t, repo, "docs/standards/std-aaa.json",
			pathJSON(t, "std", "aaa", "standards", []string{"src/**"}))
		writeRepoFile(t, repo, "docs/standards/std-zzz.json",
			pathJSON(t, "std", "zzz", "standards", []string{"src/**", "docs/*.md"}))
	})
}

// TestUnitsForPathOrdersByStratumThenIdentity: matched units are ordered by
// authority stratum ascending, then canonical identity form.
func TestUnitsForPathOrdersByStratumThenIdentity(t *testing.T) {
	seedPathRepo(t)
	r := openRuntime(t)
	got, err := New(r).UnitsForPath("eka-view-fixture", "eka-view-fixture", "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	var forms []string
	for _, e := range got {
		forms = append(forms, e.CanonicalForm)
		if e.Role != "appliesTo" {
			t.Errorf("%s role = %q, want appliesTo", e.CanonicalForm, e.Role)
		}
	}
	want := []string{
		"eka-view-fixture/req:aaa:1", // Discovery, stratum 1
		"eka-view-fixture/std:aaa:1", // Architecture, stratum 2
		"eka-view-fixture/std:zzz:1",
	}
	if !reflect.DeepEqual(forms, want) {
		t.Fatalf("forms = %v, want %v", forms, want)
	}
	if got[0].Stratum != 1 || got[1].Stratum != 2 || got[2].Stratum != 2 {
		t.Errorf("strata = %d,%d,%d, want 1,2,2", got[0].Stratum, got[1].Stratum, got[2].Stratum)
	}
}

// TestUnitsForPathNoMatchIsEmpty: a path matched by no declaration yields an
// empty set, never an error.
func TestUnitsForPathNoMatchIsEmpty(t *testing.T) {
	seedPathRepo(t)
	r := openRuntime(t)
	got, err := New(r).UnitsForPath("eka-view-fixture", "eka-view-fixture", "unrelated/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("no-match set = %v, want empty", got)
	}
}

// TestUnitsForPathIsRepositoryScoped: a path binds only against declarations
// of its own repository (the provenance pair), never a sibling repository's.
func TestUnitsForPathIsRepositoryScoped(t *testing.T) {
	seedPathRepo(t)
	r := openRuntime(t)
	got, err := New(r).UnitsForPath("eka-view-fixture", "some-other-repo", "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("sibling-repo set = %v, want empty (repository-scoped)", got)
	}
}

// This file's remaining tests exercise the Standard 1.5 repository-level
// lint primitive (UnmatchedScopes).

// seedScopeRepo seeds three declaring std- units across one repository:
// std-a binds src/**; std-b declares docs/** (matching) and never/**
// (not); std-c declares missing/** (not).
func seedScopeRepo(t *testing.T) {
	t.Helper()
	seedContextRepo(t, func(repo string) {
		writeRepoFile(t, repo, "docs/standards/std-a.json",
			pathJSON(t, "std", "a", "standards", []string{"src/**"}))
		writeRepoFile(t, repo, "docs/standards/std-b.json",
			pathJSON(t, "std", "b", "standards", []string{"docs/**", "never/**"}))
		writeRepoFile(t, repo, "docs/standards/std-c.json",
			pathJSON(t, "std", "c", "standards", []string{"missing/**"}))
	})
}

// TestUnmatchedScopesWarnsOnlyUnmatched: a matching pattern warns nothing;
// a non-matching one warns exactly once; a unit with one matching and one
// non-matching pattern warns exactly the non-matching one.
func TestUnmatchedScopesWarnsOnlyUnmatched(t *testing.T) {
	seedScopeRepo(t)
	r := openRuntime(t)
	got, err := New(r).UnmatchedScopes("eka-view-fixture", "eka-view-fixture",
		[]string{"src/main.go", "docs/readme.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := []ScopeWarning{
		{CanonicalForm: "eka-view-fixture/std:b:1", Pattern: "never/**"},
		{CanonicalForm: "eka-view-fixture/std:c:1", Pattern: "missing/**"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %+v, want %+v", got, want)
	}
}

// TestUnmatchedScopesEmptyFilesWarnsAll: an empty file list (a valid
// state — the scope is declared ahead of the code) warns every declared
// pattern, in CanonicalForm-then-Pattern order.
func TestUnmatchedScopesEmptyFilesWarnsAll(t *testing.T) {
	seedScopeRepo(t)
	r := openRuntime(t)
	got, err := New(r).UnmatchedScopes("eka-view-fixture", "eka-view-fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ScopeWarning{
		{CanonicalForm: "eka-view-fixture/std:a:1", Pattern: "src/**"},
		{CanonicalForm: "eka-view-fixture/std:b:1", Pattern: "docs/**"},
		{CanonicalForm: "eka-view-fixture/std:b:1", Pattern: "never/**"},
		{CanonicalForm: "eka-view-fixture/std:c:1", Pattern: "missing/**"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %+v, want %+v", got, want)
	}
}

// TestUnmatchedScopesNoDeclarationsIsEmpty: the base fixture declares no
// `appliesTo` anywhere — an empty slice, no error.
func TestUnmatchedScopesNoDeclarationsIsEmpty(t *testing.T) {
	seedContextRepo(t, nil)
	r := openRuntime(t)
	got, err := New(r).UnmatchedScopes("eka-view-fixture", "eka-view-fixture", []string{"src/main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("warnings = %+v, want empty", got)
	}
}

// TestUnmatchedScopesIsRepositoryScoped: a sibling repository's
// declaration is never surfaced (the provenance-pair scope).
func TestUnmatchedScopesIsRepositoryScoped(t *testing.T) {
	seedScopeRepo(t)
	r := openRuntime(t)
	got, err := New(r).UnmatchedScopes("eka-view-fixture", "some-other-repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("sibling-repo warnings = %+v, want empty (repository-scoped)", got)
	}
}

// TestUnmatchedScopesMalformedIsError: a malformed declaration is an
// error, never ignored.
func TestUnmatchedScopesMalformedIsError(t *testing.T) {
	seedMalformedScopeRepo(t)
	r := openRuntime(t)
	if _, err := New(r).UnmatchedScopes("eka-view-fixture", "eka-view-fixture", []string{"src/main.go"}); err == nil {
		t.Fatal("malformed `appliesTo` must be an error")
	}
}

// seedMalformedScopeRepo seeds one repository whose stored unit declares
// a malformed `appliesTo` (an uninterpretable pattern). The docs
// conformance gate rejects such a declaration (conformance/applicability.go),
// so the unit reaches the store through a source-only snapshot — the one
// seed path that does not re-run the gate.
func seedMalformedScopeRepo(t *testing.T) {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copyFixtureLocal(t, filepath.Join("..", "view", "testdata", "valid"))
	writeSnapshotRepo(t, repo, []*exchange.Unit{{
		Identity:              exchange.Identity{Namespace: "eka-view-fixture", Type: "std", ID: "bad", InstanceVersion: 1},
		CanonicalIdentityForm: "eka-view-fixture/std:bad:1",
		Revision:              1,
		StateVector:           exchange.StateVector{ContentState: "draft"},
		ChangeLog:             []exchange.ChangeLogEntry{},
		Relationships:         []exchange.Relationship{},
		Content:               exchange.ContentRef{Representation: exchange.StructuredJSON, File: "content"},
		ContentPayload:        []byte(`{"purpose":"p","content":"c","appliesTo":["a**b"]}`),
	}})
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := runtime.Authoring.Sync(r, repo, runtime.SyncOptions{Pull: true}); err != nil {
		t.Fatal(err)
	}
}

// writeSnapshotRepo emits a source-only snapshot of units into
// <repo>/exchange/snapshots — the store seed used to bypass the docs
// conformance gate.
func writeSnapshotRepo(t *testing.T, repo string, units []*exchange.Unit) {
	t.Helper()
	pkg := &exchange.Package{
		Header: exchange.Header{
			SerializationVersion:  exchange.SerializationVersion,
			ExchangeFormatVersion: exchange.ExchangeFormatVersion,
			SpecificationVersion:  exchange.SpecificationVersion,
			Exporter:              exchange.Exporter,
			PackageIdentityLabel:  exchange.PackageIdentityLabel(exchange.ScopeRepository, "eka-view-fixture"),
			ExportScope:           exchange.ScopeRepository,
			Namespace:             "eka-view-fixture",
		},
		Units: units,
		Declarations: exchange.Declarations{
			Closure:            exchange.ClosureDeclaration{Scope: exchange.ScopeRepository, Seeds: []string{}},
			ExternalReferences: []exchange.ExternalReference{},
			Extensions:         []exchange.ExtensionDecl{},
		},
	}
	files, _, err := exchange.EmitSource(pkg)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo, "exchange", "snapshots")
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
