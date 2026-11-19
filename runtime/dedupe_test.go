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

// TestSimilarityShortTitleTyposDedupe: the character-level fallback's remaining
// job. For titles of two tokens or fewer, one mistyped letter is a large
// fraction of the title while token Jaccard only sees two different tokens, so
// the typo path has to stay open. Plurals are no longer this fallback's
// responsibility — stemming handles those — so what is pinned here is a genuine
// misspelling, not a grammar variant.
func TestSimilarityShortTitleEditsDedupe(t *testing.T) {
	c := CaptureService{}
	for _, p := range [][2]string{
		{"fix login", "fix logn"},
		{"dedupe", "dedup"},
	} {
		if got := c.Similarity(p[0], p[1]); !c.IsSimilar(p[0], p[1]) {
			t.Errorf("Similarity(%q,%q) = %v, want >= 0.8: a typo in a short title is the same task", p[0], p[1], got)
		}
	}
}

// TestSimilarityPluralsDedupe: a singular/plural edit is the same task, so it
// must dedupe at every title length. The three- and four-token cases are the
// ones that matter: they are past the Levenshtein gate, so only the token
// metric can save them. When it cannot, capture files a second draft for work
// that is already tracked and the duplicate is invisible.
func TestSimilarityPluralsDedupe(t *testing.T) {
	c := CaptureService{}
	pairs := [][2]string{
		{"login", "logins"},
		{"add cache", "add caches"},
		// cache+caches at three tokens has no fallback to lean on, and "cache"
		// takes a plain "s": a stemmer that assumed every "-ches" is "-ch"+"es"
		// answers "cach" and leaves these two titles apart. Same defect as the
		// three-token "bug"/"bugs" case this metric was introduced for.
		{"fix login cache", "fix login caches"},
		// The other reading of the same suffix, where the "es" really is there.
		{"watch the queue", "watches the queue"},
		{"fix login bug", "fix login bugs"},
		{"fix login bug redirect", "fix login bugs redirect"},
	}
	for _, p := range pairs {
		if got := c.Similarity(p[0], p[1]); !c.IsSimilar(p[0], p[1]) {
			t.Errorf("Similarity(%q,%q) = %v, want >= 0.8: singular/plural is one task", p[0], p[1], got)
		}
	}
}

// TestSimilarityDifferingLongTitlesDoNotDedupe: the over-merge risk that the
// fallback's length gate protects. These two titles share four of five tokens
// and differ only in the qualifier. A metric that forgave that difference would
// fold "fix login bug in auth" into "fix login bug in billing": two different
// tasks, one draft, and the second one silently gone. If this ever dedupes,
// stop and check how the gate or the threshold moved.
func TestSimilarityDifferingLongTitlesDoNotDedupe(t *testing.T) {
	c := CaptureService{}
	a, b := "fix login bug in auth", "fix login bug in billing"
	if got := c.Similarity(a, b); c.IsSimilar(a, b) {
		t.Errorf("Similarity(%q,%q) = %v, want < 0.8: these are different tasks", a, b, got)
	}
}

// TestSimilarityStemmingKeepsDistinctWordsApart: class/classes and
// status/statuses are plural pairs, so they must collapse to one token — but
// the form they collapse to must not swallow unrelated words. "classes" ->
// "class" is right; "classes" -> "clas" would still dedupe the pair while also
// colliding with anything that starts the same way, which is exactly how
// over-stemming starts merging tasks that only share a prefix.
func TestSimilarityStemmingKeepsDistinctWordsApart(t *testing.T) {
	c := CaptureService{}
	for _, p := range [][2]string{
		{"class", "classes"},
		{"status", "statuses"},
		{"analysis", "analyses"},
	} {
		if got := c.Similarity(p[0], p[1]); !c.IsSimilar(p[0], p[1]) {
			t.Errorf("Similarity(%q,%q) = %v, want >= 0.8: the plural pair must resolve to one token", p[0], p[1], got)
		}
	}
	// Agreeing is not enough — the shared stem has to be a word. "statuses" and
	// "status" both landing on "statu" would dedupe this pair and hand the
	// metric a form nobody writes, which is a standing collision risk against
	// anything that happens to normalise the same way. Pinned by value so a
	// future rule cannot reintroduce it silently.
	for _, want := range []struct{ token, stem string }{
		{"status", "status"},
		{"statuses", "status"},
		{"class", "class"},
		{"classes", "class"},
		{"analysis", "analysis"},
		{"analyses", "analysis"},
	} {
		if got := stems(want.token); len(got) != 1 || got[0] != want.stem {
			t.Errorf("stems(%q) = %v, want exactly [%q]", want.token, got, want.stem)
		}
	}
	// "caches" is the token that forced the two-reading design: cache+"s" and
	// cach+"es" are both English, so neither reading may be dropped on a guess.
	if got := stems("caches"); len(got) != 2 {
		t.Errorf("stems(%q) = %v, want both readings", "caches", got)
	}
	for _, p := range [][2]string{
		{"Fix class hierarchy", "Fix glass hierarchy"},
		{"Check status endpoint", "Check stats endpoint"},
	} {
		if got := c.Similarity(p[0], p[1]); c.IsSimilar(p[0], p[1]) {
			t.Errorf("Similarity(%q,%q) = %v, want < 0.8: shared structure is not a shared task", p[0], p[1], got)
		}
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
