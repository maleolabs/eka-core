package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file tests the reply-note contract (ADR-019 D8 revised): the
// reply role schema ({role, body}) and the replies-to relationship
// resolution of the R13 graph pass.

// validateOne validates a single authoring file and returns the
// blocking (error) findings.
func validateOne(t *testing.T, content string) []Result {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "docs", "operating", "notes", "cmt-x.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Validate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return report.Results
}

func TestReplyRoleSchema(t *testing.T) {
	base := `{
  "namespace": "feather", "type": "cmt", "id": "p",
  "instanceVersion": 1, "revision": 1,
  "author": "Jonas Berg",
  "created": "2026-08-10", "updated": "2026-08-10",
  "state": {"contentState": "draft", "existenceState": "active", "noteState": "open"},
  "changeLog": [
    {"date": "2026-08-10", "domain": "contentState", "from": "-", "to": "draft", "by": "Jonas Berg"},
    {"date": "2026-08-10", "domain": "existenceState", "from": "-", "to": "active", "by": "Jonas Berg"},
    {"date": "2026-08-10", "domain": "noteState", "from": "-", "to": "open", "by": "Jonas Berg"}
  ],
  "content": {"role": "reply", "body": "Looks good."}
}`
	findings := validateOne(t, base)
	if n := countResults(&Report{Results: findings}, Rule13, SeverityError); n != 0 {
		t.Errorf("valid reply note: %d R13 errors: %v", n, findings)
	}
	// Missing body is refused.
	noBody := strings.Replace(base, `"body": "Looks good."`, `"body": ""`, 1)
	if n := countResults(&Report{Results: validateOne(t, noBody)}, Rule13, SeverityError); n == 0 {
		t.Error("reply without a body must be refused")
	}
	// Unknown keys are refused (strict schema).
	extra := strings.Replace(base, `"body": "Looks good."`, `"body": "x", "verdict": "approve"`, 1)
	if n := countResults(&Report{Results: validateOne(t, extra)}, Rule13, SeverityError); n == 0 {
		t.Error("reply with an unknown key must be refused")
	}
}

func TestRepliesToResolution(t *testing.T) {
	// A reply whose parent does not resolve is an R13 error.
	orphan := `{
  "namespace": "feather", "type": "cmt", "id": "p-reply",
  "instanceVersion": 1, "revision": 1,
  "author": "agent-x",
  "created": "2026-08-10", "updated": "2026-08-10",
  "state": {"contentState": "draft", "existenceState": "active", "noteState": "open"},
  "changeLog": [
    {"date": "2026-08-10", "domain": "contentState", "from": "-", "to": "draft", "by": {"kind": "agent", "name": "agent-x"}},
    {"date": "2026-08-10", "domain": "existenceState", "from": "-", "to": "active", "by": {"kind": "agent", "name": "agent-x"}},
    {"date": "2026-08-10", "domain": "noteState", "from": "-", "to": "open", "by": {"kind": "agent", "name": "agent-x"}}
  ],
  "relationships": {"repliesTo": ["feather/cmt:ghost"]},
  "content": {"role": "reply", "body": "Hi"}
}`
	if n := countResults(&Report{Results: validateOne(t, orphan)}, Rule13, SeverityError); n == 0 {
		t.Error("a reply to an unresolvable parent must be refused")
	}
	// A reply to a non-note target is refused with the note-line hint.
	nonNote := strings.Replace(orphan, `"feather/cmt:ghost"`, `"feather/sto:one"`, 1)
	found := false
	for _, r := range validateOne(t, nonNote) {
		if r.Rule == Rule13 && strings.Contains(r.Message, "not a note (cmt-)") {
			found = true
		}
	}
	if !found {
		t.Error("a reply to a non-note target must be refused with the note-line hint")
	}
	// Multiple parents are refused (single-parent contract).
	multi := strings.Replace(orphan, `"feather/cmt:ghost"`, `"feather/cmt:a", "feather/cmt:b"`, 1)
	found = false
	for _, r := range validateOne(t, multi) {
		if r.Rule == Rule13 && strings.Contains(r.Message, "exactly one parent") {
			found = true
		}
	}
	if !found {
		t.Error("a reply with two parents must be refused (single-parent)")
	}
}
