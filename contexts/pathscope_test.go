package contexts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
)

// This file tests the Standard 1.5 path-to-constraint retrieval door
// (UnitsForPath) and its declaration extraction (declaredScope).

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

// TestDeclaredScopeMalformedIsError: a malformed declaration is an error, so
// a unit with one can never be silently ignored.
func TestDeclaredScopeMalformedIsError(t *testing.T) {
	for name, payload := range map[string]string{
		"non-string element": `{"appliesTo":["ok",42]}`,
		"non-array value":    `{"appliesTo":"src/**"}`,
		"uninterpretable":    `{"appliesTo":["a**b"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			u := &exchange.Unit{ContentPayload: []byte(payload)}
			if _, _, err := declaredScope(u); err == nil {
				t.Fatalf("malformed declaration must be an error, got nil")
			}
		})
	}
}

// TestDeclaredScopeAbsentBindsNothing: no declaration (or a non-object
// payload) binds nothing and is not an error.
func TestDeclaredScopeAbsentBindsNothing(t *testing.T) {
	for name, payload := range map[string]string{
		"no appliesTo key":   `{"purpose":"p"}`,
		"empty object":       `{}`,
		"non-object payload": `"just a string"`,
	} {
		t.Run(name, func(t *testing.T) {
			u := &exchange.Unit{ContentPayload: []byte(payload)}
			patterns, declared, err := declaredScope(u)
			if err != nil {
				t.Fatalf("absent declaration must not error: %v", err)
			}
			if declared || patterns != nil {
				t.Fatalf("declared=%v patterns=%v, want false/nil", declared, patterns)
			}
		})
	}
}
