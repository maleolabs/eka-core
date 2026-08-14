package conformance

import "testing"

func TestParseFilename(t *testing.T) {
	cases := []struct {
		name       string
		token      string
		idPart     string
		version    int
		hasVersion bool
	}{
		{"adr-001-identity-serialization", "adr", "001-identity-serialization", 0, false},
		{"plan-rilis-1-v1", "plan", "rilis-1", 1, true},
		{"plan-x-v2", "plan", "x", 2, true},
		{"tkt-sto-login-email", "tkt", "sto-login-email", 0, false},
		{"sto-login", "sto", "login", 0, false},
		{"rel-notes-v2", "rel", "notes", 2, true},
		{"single", "single", "", 0, false},
		{"x-v1-y-v2", "x", "v1-y", 2, true},
		{"scp-a-v01", "scp", "a", 1, true},
	}
	for _, c := range cases {
		p, err := parseFilename(c.name)
		if err != nil {
			t.Errorf("parseFilename(%q) unexpected error: %v", c.name, err)
			continue
		}
		if p.Token != c.token || p.IDPart != c.idPart || p.Version != c.version || p.HasVersion != c.hasVersion {
			t.Errorf("parseFilename(%q) = %+v, want token=%q idPart=%q version=%d hasVersion=%v",
				c.name, p, c.token, c.idPart, c.version, c.hasVersion)
		}
	}
}

func TestParseFilenameEmpty(t *testing.T) {
	if _, err := parseFilename(""); err == nil {
		t.Error("parseFilename(\"\") should error")
	}
}
