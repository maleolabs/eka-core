package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maleolabs/eka-core/v2/conformance"
)

// dedupe_title_test.go guards the recorded-title contract (spec
// provenance-capture:1): a capture records the normalized title text its
// dedupeKey was computed from, and similarity dedupe compares against that
// text.
//
// The failure mode without it is a dedupe that only ever works when the
// draft identifier happens to spell the title: the key is a one-way hash, so
// the title was unrecoverable, and a draft captured as "sto:orig-slug" was
// invisible to every later capture of the title it actually came from. Two
// drafts for one task, silently.

// captureDraft scaffolds a sto- draft carrying capture metadata the way the
// gateway does: the dedupe key from the raw title, plus the title itself.
func captureDraft(t *testing.T, r *Runtime, project, id, rawTitle string) *Draft {
	t.Helper()
	content := filepath.Join(t.TempDir(), "content.json")
	if err := os.WriteFile(content, []byte(`{"description":"d","acceptanceCriteria":"ac"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dec := Capture.Evaluate("fix the login bug and update documentation for the release notes section now", rawTitle, 0.6)
	d, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:          project,
		Namespace:        "feather",
		Type:             "sto",
		ID:               id,
		Provenance:       dec.Provenance,
		SourcePromptHash: dec.SourcePromptHash,
		Confidence:       dec.Confidence,
		HasConfidence:    true,
		CaptureMeta:      CaptureMeta{Classifier: dec.Classifier, DedupeKey: dec.DedupeKey, Title: rawTitle},
		ContentFile:      content,
	})
	if err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	return d
}

// TestDraftRecordsNormalizedTitle: the draft stores the title NORMALIZED, not
// as typed, and the stored text is exactly the preimage of the stored key
// (sha256 equality is the producer contract; the spec treats a mismatch as a
// producer defect rather than an import failure, so it is asserted here and
// nowhere refused).
func TestDraftRecordsNormalizedTitle(t *testing.T) {
	r, project := draftRuntime(t)
	d := captureDraft(t, r, project, "orig-slug", "Fix Login Bug!")

	a, err := conformance.ScanFile(d.Path)
	if err != nil || a == nil {
		t.Fatalf("ScanFile(%s) = %v, %v", d.Path, a, err)
	}
	if got, want := a.CaptureMeta.Title, "fix login bug"; got != want {
		t.Errorf("captureMeta.title = %q, want the normalized %q", got, want)
	}
	sum := sha256.Sum256([]byte(a.CaptureMeta.Title))
	if got := hex.EncodeToString(sum[:]); got != a.CaptureMeta.DedupeKey {
		t.Errorf("sha256(captureMeta.title) = %s, dedupeKey = %s; the stored title must be the key's preimage",
			got, a.CaptureMeta.DedupeKey)
	}
}

// TestDedupeMatchesRecordedTitleNotSlug: the draft's id ("orig-slug") has
// nothing to do with its title, so only the recorded title can match. The
// incoming title differs from the recorded one character-wise ("bugs"), which
// keeps the exact hash path out of play: the match can only come from the
// similarity comparison against the recorded title.
func TestDedupeMatchesRecordedTitleNotSlug(t *testing.T) {
	r, project := draftRuntime(t)
	captureDraft(t, r, project, "orig-slug", "Fix Login Bug")

	if _, ok := Capture.DedupeCheckByTitle(r, project, "Fix Login Bugs", 24*time.Hour, time.Now()); !ok {
		t.Fatal("a capture whose title differs from its draft id must dedupe on the recorded title")
	}
}

// TestDedupeFallsBackToSlugWithoutTitle: a draft captured before the title was
// recorded has no preimage (the key is a one-way hash), so the identifier
// remains the only available comparison. That fallback still works when the
// id spells the title — and still misses when it does not, which is the
// limitation the recorded title removes for new drafts. Pinned so the
// fallback is not mistaken for the fixed behaviour.
func TestDedupeFallsBackToSlugWithoutTitle(t *testing.T) {
	r, project := draftRuntime(t)
	newSTODraft(t, r, project, "feather", "fix-login-bug", nil)

	if _, ok := Capture.DedupeCheckByTitle(r, project, "Fix Login Bug", 24*time.Hour, time.Now()); !ok {
		t.Fatal("a title-less draft must still dedupe through its identifier")
	}

	// And the known ceiling: an id that does not resemble the title stays
	// unmatched without a recorded title.
	r2, project2 := draftRuntime(t)
	newSTODraft(t, r2, project2, "feather", "orig", nil)
	if _, ok := Capture.DedupeCheckByTitle(r2, project2, "Fix Login Bug", 24*time.Hour, time.Now()); ok {
		t.Fatal("a title-less draft with an unrelated id cannot be matched; that is the fallback's ceiling")
	}
}

// TestDedupeExactHashPathUnaffected: the layer-1 contract — key equality is a
// duplicate regardless of the recorded title and both entry points still see
// it.
func TestDedupeExactHashPathUnaffected(t *testing.T) {
	r, project := draftRuntime(t)
	captureDraft(t, r, project, "orig-slug", "Fix Login Bug")
	key := Capture.DedupeKey("Fix Login Bug")

	if _, ok := Capture.DedupeCheck(r, project, key, 24*time.Hour, time.Now()); !ok {
		t.Error("DedupeCheck must still match on the exact hash")
	}
	if _, ok := Capture.DedupeCheckByTitle(r, project, "fix login bug", 24*time.Hour, time.Now()); !ok {
		t.Error("DedupeCheckByTitle must still match on the exact hash")
	}
}

// TestNormalizeTitleIsIdempotent: the recorded title is stored normalized, and
// Similarity normalizes its arguments again, so the comparison relies on
// normalization being idempotent rather than on a branch that skips it. If
// this ever fails, the stored title and the key can disagree.
func TestNormalizeTitleIsIdempotent(t *testing.T) {
	for _, raw := range []string{"Fix Login Bug!", "  fix   LOGIN bug ", "", "a/b:c", "Ünïcode Title"} {
		once := Capture.NormalizeTitle(raw)
		if twice := Capture.NormalizeTitle(once); twice != once {
			t.Errorf("NormalizeTitle(%q) is not idempotent: %q then %q", raw, once, twice)
		}
		if got := Capture.DedupeKey(once); got != Capture.DedupeKey(raw) {
			t.Errorf("DedupeKey(%q) = %s, DedupeKey(%q) = %s; the key must hash the normalized form", once, got, raw, Capture.DedupeKey(raw))
		}
	}
}

// TestDraftTitleSurvivesPublishAndTransport: the recorded title reaches the
// exchange unit on publish (the transport half lives in
// exchange/envelope_test.go; this pins the runtime projection so the title is
// not dropped at the publish boundary).
func TestDraftTitleSurvivesPublishAndTransport(t *testing.T) {
	r, project := draftRuntime(t)
	captureDraft(t, r, project, "orig-slug", "Fix Login Bug")

	if _, err := Authoring.Publish(r, "feather/sto:orig-slug", PublishOptions{}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	u, ok, err := r.Knowledge.Object("feather/sto:orig-slug:1")
	if err != nil || !ok {
		t.Fatalf("Knowledge.Object: %v %v", ok, err)
	}
	if u.CaptureMeta.Title != "fix login bug" {
		t.Errorf("published captureMeta.title = %q, want the recorded normalized title", u.CaptureMeta.Title)
	}
}
