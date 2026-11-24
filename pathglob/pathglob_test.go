package pathglob

import "testing"

// TestMatch is the grammar table: "*", "?", "**" (zero segments included),
// whole-path anchoring, and no-match.
func TestMatch(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// Literal.
		{"src/main.go", "src/main.go", true},
		{"src/main.go", "src/other.go", false},

		// "*" within one segment.
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/main_test.go", true},
		{"src/*.go", "src/a/b.go", false}, // never crosses "/"
		{"*.go", "main.go", true},
		{"*.go", "src/main.go", false}, // anchored: no implicit prefix segment
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/c", false}, // "*" does not match zero segments

		// "?" exactly one character.
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"a?c", "abbc", false},
		{"a?c", "a/c", false}, // "?" never matches "/"
		{"?", "é", true},      // one rune, not one byte

		// "**" across segments, including zero.
		{"**", "anything/goes", true},
		{"**/x", "x", true},
		{"**/x", "a/b/x", true},
		{"**/x", "a/x", true},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**", "a", true},
		{"a/**", "a/b/c", true},
		{"src/**/*.go", "src/main.go", true},
		{"src/**/*.go", "src/a/b/main.go", true},
		{"src/**/*.go", "lib/main.go", false},

		// Anchoring: whole path, no prefix match.
		{"docs", "docs/readme.md", false},
		{"a/b", "a/b", true},
		{"a/b", "a/b/c", false},

		// Non-metacharacters are literal.
		{"a[0]", "a[0]", true},
		{"a{1,2}", "a{1,2}", true},
	}
	for _, c := range cases {
		p, err := Compile(c.pattern)
		if err != nil {
			t.Fatalf("Compile(%q): %v", c.pattern, err)
		}
		if got := p.Match(c.path); got != c.want {
			t.Errorf("pattern %q path %q = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// TestCompileRejectsMalformed verifies the two patterns that cannot be
// interpreted: an empty pattern and a "**" fragment inside a segment.
func TestCompileRejectsMalformed(t *testing.T) {
	for _, pattern := range []string{"", "a**b", "**b", "a**"} {
		if _, err := Compile(pattern); err == nil {
			t.Errorf("Compile(%q) must refuse a malformed pattern", pattern)
		}
	}
}

// TestInterpret covers the decoded-value shapes: an array of strings
// compiles; a non-array, a non-string element and an uninterpretable
// pattern are errors; an absent field is never passed (empty array spends no
// declaration but is not malformed).
func TestInterpret(t *testing.T) {
	if got, err := Interpret([]any{"src/**", "docs/*.md"}); err != nil || len(got) != 2 {
		t.Errorf("Interpret(valid) = %v, %v", got, err)
	}
	if got, err := Interpret([]string{"src/**"}); err != nil || len(got) != 1 {
		t.Errorf("Interpret([]string) = %v, %v", got, err)
	}
	if got, err := Interpret([]any{}); err != nil || len(got) != 0 {
		t.Errorf("Interpret(empty) = %v, %v; want empty, nil", got, err)
	}
	for _, bad := range []any{"src/**", 42, []any{"ok", 42}, []any{"a**"}, map[string]any{}} {
		if _, err := Interpret(bad); err == nil {
			t.Errorf("Interpret(%v) must report a malformed declaration", bad)
		}
	}
}

// TestInterpretChecks covers the decoded-value shapes of a `checks` field: an
// array of objects with a non-empty command and an optional scope compiles; a
// non-array value, a non-object element, a missing/non-string/empty command and
// a bad scope are errors.
func TestInterpretChecks(t *testing.T) {
	valid := []any{
		map[string]any{"command": "make test", "scope": []any{"src/**"}},
		map[string]any{"command": "make lint"},
	}
	got, err := InterpretChecks(valid)
	if err != nil || len(got) != 2 {
		t.Fatalf("InterpretChecks(valid) = %v, %v", got, err)
	}
	if got[0].Command != "make test" || len(got[0].Scope) != 1 || got[0].Scope[0].String() != "src/**" {
		t.Errorf("first check = %+v", got[0])
	}
	if len(got[1].Scope) != 0 {
		t.Errorf("absent scope must be empty, got %v", got[1].Scope)
	}
	for _, bad := range []any{
		"make test",
		[]any{"make test"},
		[]any{map[string]any{"scope": []any{"src/**"}}},
		[]any{map[string]any{"command": 42}},
		[]any{map[string]any{"command": ""}},
		[]any{map[string]any{"command": "make test", "scope": "src/**"}},
		[]any{map[string]any{"command": "make test", "scope": []any{"a**b"}}},
	} {
		if _, err := InterpretChecks(bad); err == nil {
			t.Errorf("InterpretChecks(%v) must report a malformed declaration", bad)
		}
	}
}
