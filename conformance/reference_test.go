package conformance

import "testing"

// parseReference unit tests: reference grammar and resolution semantics.

func TestParseReference(t *testing.T) {
	cases := []struct {
		raw         string
		defNS       string
		defType     string
		ns, typ, id string
		version     int
		hasVersion  bool
		wantErr     bool
	}{
		{"sto:login", "eka", "adr", "eka", "sto", "login", 0, false, false},
		{"sto:login:2", "eka", "adr", "eka", "sto", "login", 2, true, false},
		{"ns2/adr:001-x", "eka", "adr", "ns2", "adr", "001-x", 0, false, false},
		{"ns2/adr:001-x:3", "eka", "adr", "ns2", "adr", "001-x", 3, true, false},
		{"001-bare", "eka", "adr", "eka", "adr", "001-bare", 0, false, false},
		{"plan:r-1:9", "eka", "sto", "eka", "plan", "r-1", 9, true, false},
		// Malformed references.
		{"", "eka", "adr", "", "", "", 0, false, true},
		{"sto:", "eka", "adr", "", "", "", 0, false, true},
		{"sto:a:b", "eka", "adr", "", "", "", 0, false, true},   // id contains a colon
		{"sto:a:2x", "eka", "adr", "", "", "", 0, false, true},  // version suffix not digits
		{"mystery:x", "eka", "adr", "", "", "", 0, false, true}, // unknown type token
		{"/adr:x", "eka", "adr", "", "", "", 0, false, true},    // empty namespace
		{"ns/", "eka", "adr", "", "", "", 0, false, true},       // empty rest
		{"login", "eka", "", "", "", "", 0, false, true},        // bare id with defType empty
	}
	for _, c := range cases {
		ref, err := parseReference(c.raw, c.defNS, c.defType)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseReference(%q) expected error, got %+v", c.raw, ref)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseReference(%q) unexpected error: %v", c.raw, err)
			continue
		}
		if ref.Namespace != c.ns || ref.Type != c.typ || ref.ID != c.id ||
			ref.Version != c.version || ref.HasVersion != c.hasVersion {
			t.Errorf("parseReference(%q) = %+v, want ns=%q type=%q id=%q ver=%d hasVer=%v",
				c.raw, ref, c.ns, c.typ, c.id, c.version, c.hasVersion)
		}
	}
}

func TestParseReferenceCrossNamespace(t *testing.T) {
	// Bare ids resolve to the referrer's namespace and type.
	ref, err := parseReference("001-identity-serialization", "eka-ref-impl", "adr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Namespace != "eka-ref-impl" || ref.Type != "adr" || ref.ID != "001-identity-serialization" || ref.HasVersion {
		t.Errorf("bare id reference = %+v", ref)
	}
}
