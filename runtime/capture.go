package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/maleolabs/eka-core/conformance"
)

// CaptureService is the universal gateway for inferred direct-task capture
// (ADR-035 v3 + spec provenance-capture:1). It is the single deterministic
// entry point for eka-cli and eka-mcp — sejajar codegraph: a stateless
// service living in runtime.Authoring (package-level CaptureService var).
//
// Contract:
//   - No LLM required; classifier is keyword-heuristic v1.
//   - No auto-publish; it only decides whether a prompt should become a
//     draft with provenance=inferred and builds the metadata.
//   - Respect R0-R13: captured drafts are validated like any other draft.
//   - Provenance enum: human (default) | inferred | reconciled.
//
// The service is stateless and deterministic; confidence and dedupe are
// pure functions of the input and the threshold/window from eka.yaml
// (capture.threshold default 0.6, capture.dedupeWindow default 24h).
type CaptureService struct{}

// Capture is the package-level gateway (runtime.Capture / Authoring.CaptureService alias).
var Capture CaptureService

// CaptureDecision is the outcome of evaluating one prompt/title pair.
type CaptureDecision struct {
	ShouldCapture    bool
	Confidence       float64
	Provenance       string
	SourcePromptHash string
	DedupeKey        string
	Classifier       string
	Reason           string
}

// Provenance constants re-exported from conformance for runtime callers.
const (
	ProvenanceHuman      = conformance.ProvenanceHuman
	ProvenanceInferred   = conformance.ProvenanceInferred
	ProvenanceReconciled = conformance.ProvenanceReconciled
)

// DefaultThreshold is the default capture.threshold when eka.yaml omits it.
const DefaultThreshold = 0.6

// DefaultDedupeWindow is the default capture.dedupeWindow.
const DefaultDedupeWindow = 24 * time.Hour

// verbs is the deterministic verb set of classifier v1 (lowercase).
var verbs = []string{"fix", "add", "change", "decide", "update", "remove"}

var verbRegexp = regexp.MustCompile(`(?i)\b(fix|add|change|decide|update|remove)\b`)
var spaceCollapse = regexp.MustCompile(`\s+`)
var nonAlnum = regexp.MustCompile(`[^a-z0-9 ]`)

// ContainsVerb reports whether s contains one of the classifier verbs (case-insensitive, word-boundary).
func (CaptureService) ContainsVerb(s string) bool {
	return verbRegexp.MatchString(s)
}

// WordCount counts words in s (strings.Fields semantics).
func (CaptureService) WordCount(s string) int {
	return len(strings.Fields(s))
}

// Classify returns the deterministic confidence for s:
//
//	verb + >=10 words => 0.8
//	>=10 words only    => 0.5
//	otherwise          => 0.2
func (c CaptureService) Classify(s string) (confidence float64, hasVerb bool, words int) {
	words = c.WordCount(s)
	hasVerb = c.ContainsVerb(s)
	switch {
	case hasVerb && words >= 10:
		return 0.8, hasVerb, words
	case words >= 10:
		return 0.5, hasVerb, words
	default:
		return 0.2, hasVerb, words
	}
}

// ShouldCapture reports whether s is significant enough to auto-draft:
// verb present + len>=10 + confidence>=threshold.
func (c CaptureService) ShouldCapture(s string, threshold float64) bool {
	conf, hasVerb, words := c.Classify(s)
	return hasVerb && words >= 10 && conf >= threshold
}

// SourcePromptHash returns the hex sha256 of the prompt (provenance trace).
func (CaptureService) SourcePromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

// NormalizeTitle lowercases, removes punctuation, collapses whitespace, trims.
func (CaptureService) NormalizeTitle(title string) string {
	s := strings.ToLower(title)
	s = nonAlnum.ReplaceAllString(s, " ")
	s = spaceCollapse.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// DedupeKey returns the 64-char hex sha256 of the normalized title.
func (c CaptureService) DedupeKey(title string) string {
	norm := c.NormalizeTitle(title)
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// IsDuplicate reports whether last and now are within window (window is the dedupeWindow).
func (CaptureService) IsDuplicate(last, now time.Time, window time.Duration) bool {
	if window <= 0 {
		window = DefaultDedupeWindow
	}
	return now.Sub(last) <= window
}

// Evaluate is the single gateway entry point: given a prompt and an optional
// title, it returns a CaptureDecision with confidence, provenance, hashes and
// the should-capture verdict against threshold. Classifier is always
// "keyword-heuristic v1" (spec §3).
func (c CaptureService) Evaluate(prompt, title string, threshold float64) CaptureDecision {
	if threshold == 0 {
		threshold = DefaultThreshold
	}
	conf, hasVerb, words := c.Classify(prompt)
	hash := c.SourcePromptHash(prompt)
	dedupe := ""
	if title != "" {
		dedupe = c.DedupeKey(title)
	}
	should := hasVerb && words >= 10 && conf >= threshold
	reason := ""
	switch {
	case !hasVerb:
		reason = "no trigger verb (fix/add/change/decide/update/remove)"
	case words < 10:
		reason = "too short (<10 words)"
	case conf < threshold:
		reason = "confidence below threshold"
	default:
		reason = "verb + length + confidence"
	}
	prov := ProvenanceHuman
	if should {
		prov = ProvenanceInferred
	}
	return CaptureDecision{
		ShouldCapture:    should,
		Confidence:       conf,
		Provenance:       prov,
		SourcePromptHash: hash,
		DedupeKey:        dedupe,
		Classifier:       "keyword-heuristic v1",
		Reason:           reason,
	}
}

// Similarity returns the normalized title similarity in [0,1] for two raw
// titles. It normalizes both via NormalizeTitle, reduces each token to the set
// of its plausible plural-insensitive stems (stems), then computes token-set
// Jaccard where a token matches when its stem sets intersect — which is why
// "fix login bug" and "fix login bugs" score 1.0 without any character-level
// help. Titles of two tokens or fewer additionally try a character-level
// Levenshtein ratio and keep the larger score; that fallback is what absorbs a
// real typo in a short title, and its length gate is what keeps long titles
// sharing a prefix apart ("fix login bug in auth" vs "fix login bug in billing"
// stays at 4/6 token overlap, no fallback, no dedupe). The tokens are unweighted
// on purpose: there is no corpus of captured titles to derive IDF or any other
// weight from, so a weight would be an invented parameter dressed up as a
// metric. Deterministic, no external dependency. Threshold for dedupe is 0.8
// (sto:capture-gateway-core acceptance: similarity >=80%).
func (c CaptureService) Similarity(a, b string) float64 {
	na := c.NormalizeTitle(a)
	nb := c.NormalizeTitle(b)
	if na == nb {
		return 1.0
	}
	if na == "" || nb == "" {
		return 0
	}
	// Token Jaccard over stem sets.
	ta := strings.Fields(na)
	tb := strings.Fields(nb)
	ga := stemGroups(ta)
	gb := stemGroups(tb)
	inter := overlap(ga, gb)
	union := len(ga) + len(gb) - inter
	if union == 0 {
		return 0
	}
	jaccard := float64(inter) / float64(union)
	// For titles of two tokens or fewer, also consider the Levenshtein ratio:
	// one mistyped letter is a large fraction of a short title, while token
	// comparison sees two whole different tokens.
	if len(ta) <= 2 || len(tb) <= 2 {
		lev := levenshteinRatio(na, nb)
		if lev > jaccard {
			return lev
		}
	}
	return jaccard
}

// stems returns the plausible plural-insensitive stems of a lowercased token,
// usually exactly one. Only plural suffixes are stripped; verb suffixes are left
// alone deliberately, because over-stemming costs more than the singular/plural
// miss it would fix (a rule turning "planning" and "planned" into "plann" merges
// them with each other and with "planner", i.e. it invents similarity the titles
// do not have). Each token passes through one rule, once — no iteration, so no
// rule can leave a token in a form no writer would produce.
//
// Three ambiguity guards, each of which exists because the naive rule re-created
// the duplicate-draft bug one word class over:
//
//   - "-ches"/"-shes" is genuinely ambiguous in English: "watches" is watch+es,
//     "caches" is cache+s. Both readings are returned and either may match.
//     Picking one made "cache" and "caches" different tokens.
//   - a trailing "s" after "u" or "i" is usually Latin singular, not a plural
//     ("status", "virus", "bonus", "analysis"), so it is not stripped. Stripping
//     it produced "statu": a form that matches nothing, which is how "status"
//     and "statuses" stopped agreeing.
//   - "-yses" maps to "-ysis" so "analyses" reaches "analysis" through the same
//     single pass.
//
// "-ses" itself keeps one reading, so "cases" lands on "cas" and never agrees
// with "case". Splitting that one open means deciding between case+s and
// cas+es on every "-ses" token — "bases", "buses", "phases", "releases" — which
// needs a dictionary, not a heuristic; the miss is left rather than guessed at.
func stems(t string) []string {
	switch {
	case len(t) > 5 && strings.HasSuffix(t, "yses"):
		return []string{t[:len(t)-4] + "ysis"}
	case len(t) > 4 && strings.HasSuffix(t, "ies"):
		return []string{t[:len(t)-3] + "y"}
	case len(t) > 4 && (strings.HasSuffix(t, "ches") || strings.HasSuffix(t, "shes")):
		return []string{t[:len(t)-2], t[:len(t)-1]}
	case len(t) > 4 && (strings.HasSuffix(t, "ses") || strings.HasSuffix(t, "xes") ||
		strings.HasSuffix(t, "zes")):
		return []string{t[:len(t)-2]}
	case len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") &&
		!strings.HasSuffix(t, "us") && !strings.HasSuffix(t, "is"):
		return []string{t[:len(t)-1]}
	}
	return []string{t}
}

// stemGroups maps a title's tokens to their stem sets, dropping repeated
// readings so a title cannot inflate its own overlap by repeating a word.
func stemGroups(tokens []string) [][]string {
	groups := make([][]string, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		s := stems(t)
		key := strings.Join(s, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		groups = append(groups, s)
	}
	return groups
}

// overlap counts matched tokens between two stem-group lists: each token matches
// at most once, and the result is the smaller of the two one-sided match counts
// so a two-reading token cannot count twice or inflate the score. When every
// stem set is a single form — every token that is not a "-ches"/"-shes" plural —
// both counts agree and this is exactly the set intersection, so the score keeps
// its meaning.
func overlap(a, b [][]string) int {
	count := func(x, y [][]string) (n int) {
		for _, gx := range x {
			for _, gy := range y {
				if intersects(gx, gy) {
					n++
					break
				}
			}
		}
		return n
	}
	return min(count(a, b), count(b, a))
}

// intersects reports whether two stem sets share a reading.
func intersects(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// IsSimilar reports whether two titles are similar enough to dedupe
// (>=80% per sto acceptance). Deterministic.
func (c CaptureService) IsSimilar(a, b string) bool {
	return c.Similarity(a, b) >= 0.8
}

// levenshteinRatio returns 1 - distance/maxLen for two normalized strings.
func levenshteinRatio(a, b string) float64 {
	if a == b {
		return 1
	}
	la, lb := len(a), len(b)
	if la == 0 || lb == 0 {
		return 0
	}
	// DP with two rows.
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = min(del, min(ins, sub))
		}
		prev, cur = cur, prev
	}
	dist := prev[lb]
	maxLen := la
	if lb > maxLen {
		maxLen = lb
	}
	return 1 - float64(dist)/float64(maxLen)
}

// DedupeCheck inspects existing drafts of a project for a dedupeKey collision
// within window. It scans the drafts directory via Drafts and compares the
// stored captureMeta.dedupeKey (from the draft JSON's captureMeta.dedupeKey
// field via ScanFile). If a match is found within window, it returns the
// matched draft and true — the caller should create a cmt:note discusses target
// instead of a new CKO. Window defaults to 24h.
//
// Layer-1 dedupe is exact hash equality of normalized title (spec §3
// dedupeKey = hash(normalized-title)). Layer-1.5 similarity >=80% is handled
// via DedupeCheckByTitle which compares normalized titles with IsSimilar.
func (c CaptureService) DedupeCheck(rt *Runtime, project, dedupeKey string, window time.Duration, now time.Time) (*Draft, bool) {
	if dedupeKey == "" {
		return nil, false
	}
	if window <= 0 {
		window = DefaultDedupeWindow
	}
	drafts, err := Authoring.Drafts(rt, project)
	if err != nil {
		return nil, false
	}
	for _, d := range drafts {
		a, err := conformance.ScanFile(d.Path)
		if err != nil || a == nil {
			continue
		}
		if a.CaptureMeta.DedupeKey != dedupeKey {
			continue
		}
		// Use file mtime as creation time proxy (draft file has no timestamp in content).
		// Both Draft.Updated and file modtime are RFC3339; compare against now.
		if d.Updated != "" {
			if t, err := time.Parse(time.RFC3339, d.Updated); err == nil {
				if c.IsDuplicate(t, now, window) {
					cp := d
					return &cp, true
				}
				continue
			}
		}
		// Fallback: treat as duplicate if we cannot parse time (conservative).
		cp := d
		return &cp, true
	}
	return nil, false
}

// DedupeCheckByTitle inspects existing drafts for a title collision within
// window, using both exact dedupeKey equality and similarity >=80% of
// normalized titles (sto acceptance: similarity >=80% -> cmt:note). It is the
// similarity-aware dedupe path; DedupeCheck remains the exact-hash path for
// callers that only have the hash. Title is the raw title string (will be
// normalized inside).
func (c CaptureService) DedupeCheckByTitle(rt *Runtime, project, title string, window time.Duration, now time.Time) (*Draft, bool) {
	if title == "" {
		return nil, false
	}
	if window <= 0 {
		window = DefaultDedupeWindow
	}
	normIncoming := c.NormalizeTitle(title)
	hashIncoming := c.DedupeKey(title)
	drafts, err := Authoring.Drafts(rt, project)
	if err != nil {
		return nil, false
	}
	for _, d := range drafts {
		a, err := conformance.ScanFile(d.Path)
		if err != nil || a == nil {
			continue
		}
		// Exact hash path.
		if a.CaptureMeta.DedupeKey != "" && a.CaptureMeta.DedupeKey == hashIncoming {
			if d.Updated != "" {
				if t, err := time.Parse(time.RFC3339, d.Updated); err == nil {
					if c.IsDuplicate(t, now, window) {
						cp := d
						return &cp, true
					}
					continue
				}
			}
			cp := d
			return &cp, true
		}
		// Similarity path: compare normalized incoming title vs normalized
		// existing title derived from the draft's ID (slug) or, when
		// available, from the stored dedupeKey's preimage approximated via
		// the draft ID's normalized form. Using ID as proxy is deterministic
		// and matches the gateway's slug-from-title convention.
		normExisting := c.NormalizeTitle(d.ID)
		// If the draft carries a dedupeKey, we can also try to reconstruct
		// via scanning the draft's content for a title-like field, but ID
		// is the stable fallback.
		if c.IsSimilar(normIncoming, normExisting) {
			if d.Updated != "" {
				if t, err := time.Parse(time.RFC3339, d.Updated); err == nil {
					if c.IsDuplicate(t, now, window) {
						cp := d
						return &cp, true
					}
					continue
				}
			}
			cp := d
			return &cp, true
		}
		// Also compare against the draft's stored normalized title when the
		// draft's ID is generic (e.g. "orig" in tests) but its dedupeKey
		// was derived from a different title. In that case the ID proxy
		// fails, so we fall back to comparing the incoming normalized title
		// against the existing draft's dedupeKey hash equality only (already
		// handled). No further heuristic.
	}
	return nil, false
}
