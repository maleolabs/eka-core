package runtime

import (
	"errors"
	"strings"
	"testing"
)

// TestResolveDraftFileAmbiguous: the same draft identity in two
// projects is refused as ambiguous — the caller must pass --project.
func TestResolveDraftFileAmbiguous(t *testing.T) {
	r, _ := draftRuntime(t)
	newSTODraft(t, r, "proj-a", "feather", "dup", nil)
	newSTODraft(t, r, "proj-b", "feather", "dup", nil)

	_, err := resolveDraftFile(r.ws, r, "", "sto", "dup")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("resolveDraftFile with two matches = %v, want an ambiguous error", err)
	}
	if !strings.Contains(err.Error(), "proj-a") || !strings.Contains(err.Error(), "proj-b") {
		t.Errorf("the ambiguous error must name both projects: %v", err)
	}

	// With the explicit hint the ambiguity disappears.
	df, err := resolveDraftFile(r.ws, r, "proj-b", "sto", "dup")
	if err != nil {
		t.Fatalf("resolveDraftFile with hint: %v", err)
	}
	if df.Project != "proj-b" {
		t.Errorf("Project = %q, want proj-b", df.Project)
	}
}

// TestResolveDraftCrossProject: ResolveDraft (the editor path) uses
// the same cross-project fallback — a draft visible in `eka draft
// list` resolves from any directory, with the note naming the project.
func TestResolveDraftCrossProject(t *testing.T) {
	r, _ := draftRuntime(t)
	newSTODraft(t, r, "proj-a", "feather", "edit-me", nil)

	df, err := Authoring.ResolveDraft(r, "feather/sto:edit-me", "")
	if err != nil {
		t.Fatalf("ResolveDraft: %v", err)
	}
	if df.Project != "proj-a" {
		t.Errorf("Project = %q, want proj-a (cross-project fallback)", df.Project)
	}
	if !strings.Contains(df.Note, "resolved from project proj-a") {
		t.Errorf("Note must name the fallback project, got %q", df.Note)
	}

	// A missing draft is *DraftNotFoundError naming the tried project.
	_, err = Authoring.ResolveDraft(r, "feather/sto:ghost", "")
	var dne *DraftNotFoundError
	if !errors.As(err, &dne) {
		t.Fatalf("ResolveDraft of a missing draft = %v, want *DraftNotFoundError", err)
	}
}
