package contexts

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/runtime"
)

// This file tests the Standard 1.7 declared-checks retrieval door
// (DeclaredChecks) and its declaration extraction (declaredChecksOf).

// checksJSON builds a minimal conformant JSON-native artifact declaring the
// given `checks` array, for the fixture's repo namespace.
func checksJSON(t *testing.T, typeToken, id, dimension string, checks []any) string {
	t.Helper()
	doc := map[string]any{
		"namespace":       "eka-view-fixture",
		"type":            typeToken,
		"id":              id,
		"instanceVersion": 1,
		"revision":        1,
		"dimension":       dimension,
		"state":           map[string]any{"contentState": "draft", "existenceState": "active"},
		"changeLog": []any{
			map[string]any{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering"},
			map[string]any{"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering"},
		},
		"content": map[string]any{"purpose": "p", "content": "c", "checks": checks},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkObj builds one check object for checksJSON; scope nil omits the `scope`
// key (an absent scope).
func checkObj(command string, scope ...string) map[string]any {
	obj := map[string]any{"command": command}
	if scope != nil {
		items := make([]any, len(scope))
		for i, s := range scope {
			items[i] = s
		}
		obj["scope"] = items
	}
	return obj
}

// TestDeclaredChecksOrdersDeterministically: checks are ordered by canonical
// identity form, then command, then first scope pattern; every entry carries
// the declaring artifact's canonical form.
func TestDeclaredChecksOrdersDeterministically(t *testing.T) {
	seedContextRepo(t, func(repo string) {
		writeRepoFile(t, repo, "docs/standards/std-a.json",
			checksJSON(t, "std", "a", "standards", []any{
				checkObj("make test", "src/**"),
				checkObj("make lint", "docs/**"),
			}))
		writeRepoFile(t, repo, "docs/standards/std-b.json",
			checksJSON(t, "std", "b", "standards", []any{
				checkObj("make test", "src/**"),
			}))
	})
	r := openRuntime(t)
	got, err := New(r).DeclaredChecks("eka-view-fixture", "eka-view-fixture")
	if err != nil {
		t.Fatal(err)
	}
	var forms, commands []string
	for _, c := range got {
		forms = append(forms, c.CanonicalForm)
		commands = append(commands, c.Command)
	}
	wantForms := []string{"eka-view-fixture/std:a:1", "eka-view-fixture/std:a:1", "eka-view-fixture/std:b:1"}
	wantCommands := []string{"make lint", "make test", "make test"}
	if !reflect.DeepEqual(forms, wantForms) {
		t.Fatalf("forms = %v, want %v", forms, wantForms)
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if got[0].Scope[0].String() != "docs/**" {
		t.Errorf("first scope = %q, want docs/**", got[0].Scope[0].String())
	}
}

// TestDeclaredChecksEmptyAndAbsentScope: an absent `scope` and a declared empty
// scope both yield an empty Scope (matches no path), not an error.
func TestDeclaredChecksEmptyAndAbsentScope(t *testing.T) {
	seedContextRepo(t, func(repo string) {
		writeRepoFile(t, repo, "docs/standards/std-absent.json",
			checksJSON(t, "std", "absent", "standards", []any{checkObj("make test")}))
		writeRepoFile(t, repo, "docs/standards/std-empty.json",
			checksJSON(t, "std", "empty", "standards", []any{checkObj("make test", []string{}...)}))
	})
	r := openRuntime(t)
	got, err := New(r).DeclaredChecks("eka-view-fixture", "eka-view-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("checks = %d, want 2", len(got))
	}
	for _, c := range got {
		if len(c.Scope) != 0 {
			t.Errorf("%s scope = %v, want empty", c.CanonicalForm, c.Scope)
		}
	}
}

// TestDeclaredChecksIsRepositoryScoped: a sibling repository's declaration is
// never surfaced (the provenance-pair scope).
func TestDeclaredChecksIsRepositoryScoped(t *testing.T) {
	seedContextRepo(t, func(repo string) {
		writeRepoFile(t, repo, "docs/standards/std-a.json",
			checksJSON(t, "std", "a", "standards", []any{checkObj("make test", "src/**")}))
	})
	r := openRuntime(t)
	got, err := New(r).DeclaredChecks("eka-view-fixture", "some-other-repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("sibling-repo checks = %v, want empty (repository-scoped)", got)
	}
}

// TestDeclaredChecksNoDeclarationsIsEmpty: the base fixture declares no `checks`
// anywhere — an empty slice, no error.
func TestDeclaredChecksNoDeclarationsIsEmpty(t *testing.T) {
	seedContextRepo(t, nil)
	r := openRuntime(t)
	got, err := New(r).DeclaredChecks("eka-view-fixture", "eka-view-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("checks = %v, want empty", got)
	}
}

// TestDeclaredChecksMalformedIsError: a malformed declaration is an error, never
// silently ignored.
func TestDeclaredChecksMalformedIsError(t *testing.T) {
	seedMalformedChecksRepo(t)
	r := openRuntime(t)
	if _, err := New(r).DeclaredChecks("eka-view-fixture", "eka-view-fixture"); err == nil {
		t.Fatal("malformed `checks` must be an error")
	}
}

// TestDeclaredChecksOfAbsentBindsNothing: no declaration (or a non-object
// payload) contributes nothing and is not an error.
func TestDeclaredChecksOfAbsentBindsNothing(t *testing.T) {
	for name, payload := range map[string]string{
		"no checks key":      `{"purpose":"p"}`,
		"empty object":       `{}`,
		"non-object payload": `"just a string"`,
	} {
		t.Run(name, func(t *testing.T) {
			u := &exchange.Unit{ContentPayload: []byte(payload)}
			checks, err := declaredChecksOf(u)
			if err != nil {
				t.Fatalf("absent declaration must not error: %v", err)
			}
			if len(checks) != 0 {
				t.Fatalf("checks = %v, want empty", checks)
			}
		})
	}
}

// seedMalformedChecksRepo seeds one repository whose stored unit declares a
// malformed `checks` (a non-array value). The docs conformance gate rejects
// such a declaration (conformance/checks.go), so the unit reaches the store
// through a source-only snapshot — the one seed path that does not re-run the
// gate.
func seedMalformedChecksRepo(t *testing.T) {
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
		ContentPayload:        []byte(`{"purpose":"p","content":"c","checks":"make test"}`),
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
