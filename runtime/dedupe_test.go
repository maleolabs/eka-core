package runtime

import (
	"testing"
	"time"
)

// TestSimilarityIdenticalAndEmpty: the two ends of the range that dedupe
// actually depends on. An identical title must be a certain duplicate, and an
// empty title must never be treated as similar to anything — otherwise every
// untitled capture would collapse into the first untitled draft.
func TestSimilarityIdenticalAndEmpty(t *testing.T) {
	c := CaptureService{}
	if got := c.Similarity("Fix login bug", "fix login bug"); got != 1.0 {
		t.Errorf("identical titles = %v, want 1.0", got)
	}
	if got := c.Similarity("Fix login bug", ""); got != 0 {
		t.Errorf("empty comparison = %v, want 0", got)
	}
	// Two empty titles are identical after normalization, so the score is 1.0.
	// That is only safe because DedupeCheckByTitle refuses an empty title
	// before reaching this function; a caller that skips that guard would
	// collapse every untitled capture together. Pinned so the dependency on
	// the upstream guard is visible rather than assumed.
	if got := c.Similarity("", ""); got != 1.0 {
		t.Errorf("both empty = %v, want 1.0 (identical, guarded upstream)", got)
	}
}

// TestSimilarityNormalization: punctuation and whitespace differences are not
// content differences, so they must not lower the score. This is the path that
// makes dedupe work on titles a human retyped.
func TestSimilarityNormalization(t *testing.T) {
	c := CaptureService{}
	pairs := [][2]string{
		{"Fix login bug", "fix  login   bug"},
		{"Fix login bug", "Fix login bug!"},
		{"Fix login bug", "  FIX LOGIN BUG  "},
	}
	for _, p := range pairs {
		if got := c.Similarity(p[0], p[1]); got != 1.0 {
			t.Errorf("Similarity(%q,%q) = %v, want 1.0 after normalization", p[0], p[1], got)
		}
	}
}

// TestSimilarityShortTitleEditsDedupe: the Levenshtein fallback does work, but
// only for titles of two tokens or fewer — that is the condition in
// Similarity, and short titles are where token Jaccard is unfairly harsh.
func TestSimilarityShortTitleEditsDedupe(t *testing.T) {
	c := CaptureService{}
	if !c.IsSimilar("add cache", "add caches") {
		t.Errorf("two-token singular/plural scored %v, want >= 0.8",
			c.Similarity("add cache", "add caches"))
	}
	if !c.IsSimilar("login", "logins") {
		t.Errorf("single-token singular/plural scored %v, want >= 0.8",
			c.Similarity("login", "logins"))
	}
}

// TestSimilarityLongTitleEditsDoNotDedupe pins a known gap, and is named so it
// cannot be mistaken for approval of it.
//
// Similarity's own doc comment cites "fix login bug" vs "fix login bugs" as the
// case the Levenshtein fallback rescues, but the fallback is gated on
// `len(ta) <= 2 || len(tb) <= 2`, and that pair is three tokens. The result is
// 0.5, well under the 0.8 threshold, so the pair the comment names does not
// dedupe at all: capture creates a second draft for the same task.
//
// This is recorded rather than fixed here. Widening the fallback changes how
// aggressively capture merges titles, which is a product decision about the
// capture gateway, not a coverage concern — and silently merging more drafts is
// its own failure mode. The finding is filed against the wave; whoever decides
// the dedupe policy should change both the condition and this test.
func TestSimilarityLongTitleEditsDoNotDedupe(t *testing.T) {
	c := CaptureService{}
	got := c.Similarity("Fix login bug", "Fix login bugs")
	if got != 0.5 {
		t.Fatalf("Similarity = %v, want 0.5 — if this changed, the dedupe policy was "+
			"decided deliberately; update this test and close the finding", got)
	}
	if c.IsSimilar("Fix login bug", "Fix login bugs") {
		t.Fatal("three-token singular/plural now dedupes; the fallback condition changed " +
			"without the dedupe policy decision this test exists to force")
	}
}

// TestSimilarityUnrelatedStaysBelowThreshold: the other direction matters more.
// Two unrelated tasks merging into one draft loses work silently, so unrelated
// titles must stay clearly apart. A shared word is not similarity.
func TestSimilarityUnrelatedStaysBelowThreshold(t *testing.T) {
	c := CaptureService{}
	pairs := [][2]string{
		{"Fix login bug", "Add export command"},
		{"Bump eka-core version", "Document the schema command"},
		{"Fix login bug", "Fix database migration"},
	}
	for _, p := range pairs {
		if c.IsSimilar(p[0], p[1]) {
			t.Errorf("Similarity(%q,%q) = %v exceeded the dedupe threshold; unrelated tasks must not merge",
				p[0], p[1], c.Similarity(p[0], p[1]))
		}
	}
}

// TestSimilarityIsSymmetricAndBounded: dedupe compares a new title against
// existing drafts, so the result must not depend on argument order and must
// stay inside [0,1] whatever the input.
func TestSimilarityIsSymmetricAndBounded(t *testing.T) {
	c := CaptureService{}
	pairs := [][2]string{
		{"Fix login bug", "Fix login bugs"},
		{"add cache", "cache invalidation"},
		{"a", "b"},
		{"Fix login bug", "Add export command"},
	}
	for _, p := range pairs {
		fwd, rev := c.Similarity(p[0], p[1]), c.Similarity(p[1], p[0])
		if fwd != rev {
			t.Errorf("Similarity(%q,%q)=%v but reverse=%v; must be symmetric", p[0], p[1], fwd, rev)
		}
		if fwd < 0 || fwd > 1 {
			t.Errorf("Similarity(%q,%q)=%v outside [0,1]", p[0], p[1], fwd)
		}
	}
}

// TestLevenshteinRatio: the fallback's own contract. Identity, one edit in a
// short string, complete mismatch, and the empty edge that must return 0 rather
// than divide by zero.
func TestLevenshteinRatio(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"abc", "abc", 1},
		{"", "", 1},
		{"abc", "", 0},
		{"", "abc", 0},
		{"a", "b", 0},
		// One edit in a three-character string. Compared with a tolerance
		// because 1-1/3 and 2/3 are not the same float64.
		{"abc", "abd", 2.0 / 3.0},
	}
	for _, c := range cases {
		got := levenshteinRatio(c.a, c.b)
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("levenshteinRatio(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestContextBudget pins the truncation guard: Priority is the ordering the
// context builder relies on, and Truncated is the flag that tells a caller
// constraints were dropped — the one loss the budget must never report as
// clean.
func TestContextBudget(t *testing.T) {
	var b ContextBudget
	want := []string{"constraints", "dependencies", "decisions", "planning", "downstream", "history"}
	got := b.Priority()
	if len(got) != len(want) {
		t.Fatalf("Priority() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Priority()[%d] = %q, want %q (order is the contract)", i, got[i], want[i])
		}
	}
	if b.EstimateBytes(400) != 100 {
		t.Errorf("EstimateBytes(400) = %d, want 100", b.EstimateBytes(400))
	}
	if b.Truncated(0) {
		t.Error("Truncated(0) = true; nothing was dropped")
	}
	if !b.Truncated(1) {
		t.Error("Truncated(1) = false; a dropped constraint must be reported")
	}
}

// TestDeltaCacheIsNotModified: the cache decides "not modified" from an
// immutable objectHash or from a matching head version. Both are ways to skip
// resending content, so a false positive would hide a real change.
func TestDeltaCacheIsNotModified(t *testing.T) {
	c := &DeltaCache{
		ByHash: map[string][]byte{"hash-a": []byte("payload")},
		ByLine: map[string]int{"eka/adr:a:1": 3},
	}
	if !c.IsNotModified("eka/adr:b:1", "hash-a", 1) {
		t.Error("known objectHash must report not-modified")
	}
	if !c.IsNotModified("eka/adr:a:1", "unknown", 3) {
		t.Error("matching head version must report not-modified")
	}
	if c.IsNotModified("eka/adr:a:1", "unknown", 4) {
		t.Error("stale head version must not report not-modified")
	}
	if c.IsNotModified("eka/adr:c:1", "unknown", 1) {
		t.Error("unknown artifact must not report not-modified")
	}
}

// TestIsDuplicateWindow: the time window is what stops a title from deduping
// against a draft created months ago, so the boundaries are pinned here.
func TestIsDuplicateWindow(t *testing.T) {
	c := CaptureService{}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	if !c.IsDuplicate(now.Add(-time.Hour), now, time.Hour) {
		t.Error("exactly on the window boundary must count as duplicate")
	}
	if c.IsDuplicate(now.Add(-2*time.Hour), now, time.Hour) {
		t.Error("outside the window must not count as duplicate")
	}
	// A future timestamp yields a negative elapsed time, which is <= the
	// window, so it counts as a duplicate. Pinned: this is the conservative
	// direction (dedupe rather than duplicate) and only reachable through
	// clock skew on a draft's recorded update time.
	if !c.IsDuplicate(now.Add(time.Minute), now, time.Hour) {
		t.Error("a future timestamp is treated as within the window; pinned behaviour")
	}
}
