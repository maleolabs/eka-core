package exchange

import "testing"

// This file tests the Standard 1.5 applicability-declaration reader
// (AppliesTo): the shared extraction of a unit's optional `appliesTo` scope.

// TestAppliesToMalformedIsError: a present but malformed declaration is an
// error, so a unit with one can never be silently ignored.
func TestAppliesToMalformedIsError(t *testing.T) {
	for name, payload := range map[string]string{
		"non-string element": `{"appliesTo":["ok",42]}`,
		"non-array value":    `{"appliesTo":"src/**"}`,
		"uninterpretable":    `{"appliesTo":["a**b"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			u := &Unit{ContentPayload: []byte(payload)}
			if _, _, err := AppliesTo(u); err == nil {
				t.Fatalf("malformed declaration must be an error, got nil")
			}
		})
	}
}

// TestAppliesToAbsentBindsNothing: no declaration (or a non-object payload)
// binds nothing and is not an error.
func TestAppliesToAbsentBindsNothing(t *testing.T) {
	for name, payload := range map[string]string{
		"no appliesTo key":   `{"purpose":"p"}`,
		"empty object":       `{}`,
		"non-object payload": `"just a string"`,
	} {
		t.Run(name, func(t *testing.T) {
			u := &Unit{ContentPayload: []byte(payload)}
			patterns, declared, err := AppliesTo(u)
			if err != nil {
				t.Fatalf("absent declaration must not error: %v", err)
			}
			if declared || patterns != nil {
				t.Fatalf("declared=%v patterns=%v, want false/nil", declared, patterns)
			}
		})
	}
}

// TestAppliesToDeclared: a well-formed declaration yields its compiled
// patterns and declared=true.
func TestAppliesToDeclared(t *testing.T) {
	u := &Unit{ContentPayload: []byte(`{"appliesTo":["src/**","docs/*.md"]}`)}
	patterns, declared, err := AppliesTo(u)
	if err != nil {
		t.Fatal(err)
	}
	if !declared {
		t.Fatal("declared = false, want true")
	}
	if len(patterns) != 2 || patterns[0].String() != "src/**" || patterns[1].String() != "docs/*.md" {
		t.Fatalf("patterns = %v, want [src/** docs/*.md]", patterns)
	}
	if !patterns[0].Match("src/a/b.go") || patterns[0].Match("docs/readme.md") {
		t.Errorf("src/** must bind src/a/b.go only")
	}
}
