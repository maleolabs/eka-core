package store

import (
	"testing"
)

// This file tests the issue-number allocation and resolution (RFC:
// per-group incremental numbers — work items, tickets and notes count
// independently per project).

// numberedRef builds a Ref of the given type token on the given line.
func numberedRef(form, project, repo, typeToken, id string, version int) Ref {
	return Ref{
		Form:            form,
		ProjectID:       project,
		SourceRepo:      repo,
		Namespace:       "acme",
		Type:            typeToken,
		ID:              id,
		InstanceVersion: version,
		Revision:        1,
		UpdatedAt:       "2026-08-07T00:00:00Z",
	}
}

func TestNumberAllocationPerGroup(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Work items share one counter; tickets and notes own theirs.
	lines := []struct {
		form, typ, id string
	}{
		{"acme/sto:a:1", "sto", "a"},
		{"acme/sto:b:1", "sto", "b"},
		{"acme/tkt:t:1", "tkt", "t"},
		{"acme/cmt:n:1", "cmt", "n"},
		{"acme/bug:c:1", "bug", "c"},
	}
	for _, l := range lines {
		if _, _, err := s.PutUnit([]byte(`{}`), []byte(l.form), numberedRef(l.form, "proj1", "repo1", l.typ, l.id, 1)); err != nil {
			t.Fatalf("PutUnit %s: %v", l.form, err)
		}
	}
	want := map[string]int{
		"acme/sto:a:1": 1,
		"acme/sto:b:1": 2,
		"acme/tkt:t:1": 1,
		"acme/cmt:n:1": 1,
		"acme/bug:c:1": 3, // bugs share the work-item counter
	}
	for form, n := range want {
		got, err := s.NumberForLine("proj1", "acme", refType(form), refID(form))
		if err != nil {
			t.Fatal(err)
		}
		if got != n {
			t.Errorf("NumberForLine(%s) = %d, want %d", form, got, n)
		}
	}
}

func refType(form string) string { return form[5:8] }
func refID(form string) string   { return form[9 : len(form)-2] }

func TestNumberStableOnUpdate(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := numberedRef("acme/sto:x:1", "proj1", "repo1", "sto", "x", 1)
	if _, _, err := s.PutUnit([]byte(`{"a":1}`), []byte("c1"), r); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutUnit([]byte(`{"a":2}`), []byte("c2"), r); err != nil {
		t.Fatal(err)
	}
	n, err := s.NumberForLine("proj1", "acme", "sto", "x")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("number after update = %d, want 1 (stable)", n)
	}
}

func TestLineByNumberAndGroups(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Two projects: numbers count independently per project (line
	// identity stays global per namespace — distinct lines per
	// project).
	if _, _, err := s.PutUnit([]byte(`{}`), []byte("c1"), numberedRef("acme/sto:a:1", "proj1", "repo1", "sto", "a", 1)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutUnit([]byte(`{}`), []byte("c2"), numberedRef("acme/tkt:t:1", "proj1", "repo1", "tkt", "t", 1)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutUnit([]byte(`{}`), []byte("c3"), numberedRef("acme/sto:b:1", "proj2", "repo2", "sto", "b", 1)); err != nil {
		t.Fatal(err)
	}
	// #1 in proj1 matches work-item AND ticket (independent counters).
	lines, err := s.LineByNumber("proj1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("LineByNumber(proj1, 1) = %d lines, want 2", len(lines))
	}
	// The group-narrowed lookup is unambiguous.
	line, found, err := s.LineByNumberGroup("proj1", "ticket", 1)
	if err != nil || !found || line.Type != "tkt" {
		t.Fatalf("LineByNumberGroup(ticket, 1) = %+v, %v, %v", line, found, err)
	}
	// proj2 counts independently.
	lines, err = s.LineByNumber("proj2", 1)
	if err != nil || len(lines) != 1 {
		t.Fatalf("LineByNumber(proj2, 1) = %d lines, want 1", len(lines))
	}
	// NumbersByProject maps line forms.
	nums, err := s.NumbersByProject("proj1")
	if err != nil {
		t.Fatal(err)
	}
	if nums["acme/sto:a"] != 1 || nums["acme/tkt:t"] != 1 {
		t.Errorf("NumbersByProject(proj1) = %v", nums)
	}
}
