package conformance

import (
	"strings"
	"testing"
)

// envelope_test.go guards the ADR-035 capture envelope at the adapter
// boundary. Both adapters must read the same five fields with the same
// validation: the envelope is format-neutral, so a value accepted in the
// JSON document and dropped in the Markdown frontmatter is a silent loss —
// export writes it, the next import of that repository forgets it, and no
// finding ever mentions it.

const envelopePromptHash = "8d5e2f1c4b7a9036e1f2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"
const envelopeDedupeKey = "dcc691003c1ed7b6149b70d2aba3692ae1d3faf827bab6a650d2c2f965275371"

// envelopeMarkdown renders a minimal adr whose frontmatter carries the
// given envelope block verbatim (indented one level).
func envelopeMarkdown(envelope string) []byte {
	return []byte("---\nnamespace: eka\ntype: adr\nid: e1\ninstance-version: 1\nrevision: 1\n" +
		"content-state: accepted\nexistence-state: active\ndimension: decisions\n" +
		envelope +
		"---\n# ADR 1 — x\n\n## Context\n\nx\n\n## Decision\n\nx\n\n## Consequences\n\nx\n")
}

// envelopeJSON renders the same artifact as a JSON authoring document, with
// the envelope keys spliced at the top level (camelCase, no indentation
// applied — analyzeJSON decodes, it does not compare bytes).
func envelopeJSON(envelope string) []byte {
	return []byte(`{"namespace":"eka","type":"adr","id":"e1","instanceVersion":1,"revision":1,` +
		`"contentState":"accepted",` + envelope +
		`"content":{"context":"x","decision":"x","consequences":"x"}}`)
}

// TestAnalyzeMarkdownReadsEnvelope: the Markdown adapter records every
// declared envelope field, including an explicit `confidence: 0` (a claim,
// distinct from absence via HasConfidence).
func TestAnalyzeMarkdownReadsEnvelope(t *testing.T) {
	md := envelopeMarkdown("provenance: inferred\n" +
		"sourcePromptHash: " + envelopePromptHash + "\n" +
		"confidence: 0\n" +
		"sourceCommitSha: 9f2c1ab\n" +
		"captureMeta:\n  classifier: keyword-heuristic v1\n  dedupeKey: " + envelopeDedupeKey + "\n  title: decide the adr 035 envelope\n" +
		"validUntil: 2027-06-30\nlastConfirmed: 2026-11-18\n")
	a, results := analyzeMarkdown("x.md", "/x.md", md, true)
	if a == nil {
		t.Fatalf("artifact did not classify: %+v", results)
	}
	for _, r := range results {
		if r.Severity == SeverityError {
			t.Fatalf("unexpected finding: %+v", r)
		}
	}
	if a.Provenance != ProvenanceInferred {
		t.Errorf("provenance = %q, want inferred", a.Provenance)
	}
	if a.SourcePromptHash != envelopePromptHash {
		t.Errorf("sourcePromptHash = %q", a.SourcePromptHash)
	}
	if !a.HasConfidence || a.Confidence != 0 {
		t.Errorf("confidence = %v (has=%v), want the declared zero", a.Confidence, a.HasConfidence)
	}
	if a.SourceCommitSha != "9f2c1ab" {
		t.Errorf("sourceCommitSha = %q", a.SourceCommitSha)
	}
	if a.CaptureMeta.Title != "decide the adr 035 envelope" || a.CaptureMeta.DedupeKey != envelopeDedupeKey ||
		a.CaptureMeta.Classifier != "keyword-heuristic v1" {
		t.Errorf("captureMeta = %+v", a.CaptureMeta)
	}
	if a.ValidUntil != "2027-06-30" || a.LastConfirmed != "2026-11-18" {
		t.Errorf("freshness = %q/%q", a.ValidUntil, a.LastConfirmed)
	}
}

// TestAnalyzeJSONAndMarkdownAgreeOnEnvelope: the same declaration through
// either adapter produces the same artifact model. This is the guard against
// one adapter drifting ahead of the other.
func TestAnalyzeJSONAndMarkdownAgreeOnEnvelope(t *testing.T) {
	fm := "provenance: reconciled\n" +
		"sourcePromptHash: " + envelopePromptHash + "\n" +
		"confidence: 0.75\n" +
		"sourceCommitSha: 9f2c1ab\n" +
		"captureMeta:\n  classifier: c\n  dedupeKey: " + envelopeDedupeKey + "\n  title: fix login bug\n"
	fromMD, _ := analyzeMarkdown("x.md", "/x.md", envelopeMarkdown(fm), true)
	fromJSON, _ := analyzeJSON("x.json", "/x.json", envelopeJSON(
		`"provenance":"reconciled",`+
			`"sourcePromptHash":"`+envelopePromptHash+`",`+
			`"confidence":0.75,`+
			`"sourceCommitSha":"9f2c1ab",`+
			`"captureMeta":{"classifier":"c","dedupeKey":"`+envelopeDedupeKey+`","title":"fix login bug"},`), true)
	if fromMD == nil || fromJSON == nil {
		t.Fatalf("classify failed: md=%v json=%v", fromMD, fromJSON)
	}
	if fromMD.Provenance != fromJSON.Provenance ||
		fromMD.SourcePromptHash != fromJSON.SourcePromptHash ||
		fromMD.Confidence != fromJSON.Confidence ||
		fromMD.HasConfidence != fromJSON.HasConfidence ||
		fromMD.SourceCommitSha != fromJSON.SourceCommitSha ||
		fromMD.CaptureMeta != fromJSON.CaptureMeta {
		t.Errorf("adapters disagree:\n markdown: %+v\n json:     %+v", fromMD, fromJSON)
	}
}

// TestAnalyzeEnvelopeRefusals: every malformed envelope value is still a
// structural finding in BOTH adapters, and the finding names the field. A
// shared pass is only shared if it actually runs for both — the failure this
// guards is the Markdown adapter silently accepting what the JSON adapter
// refuses, which drops the claim with no finding.
func TestAnalyzeEnvelopeRefusals(t *testing.T) {
	cases := []struct {
		name string
		md   string // Markdown frontmatter block
		json string // equivalent JSON top-level fragment
		want string // expected finding message
	}{
		{
			"bad provenance enum",
			"provenance: synthetic\n", `"provenance":"synthetic",`,
			`provenance "synthetic" must be one of human|inferred|reconciled`,
		},
		{
			"non-string provenance",
			"provenance:\n  - inferred\n", `"provenance":["inferred"],`,
			"`provenance` must be a string (human|inferred|reconciled)",
		},
		{
			"short sourcePromptHash",
			"sourcePromptHash: abc123\n", `"sourcePromptHash":"abc123",`,
			"`sourcePromptHash` must be a 64-char hex sha256",
		},
		{
			"out-of-range confidence",
			"confidence: 1.5\n", `"confidence":1.5,`,
			"confidence 1.5 must be 0.0-1.0",
		},
		{
			"non-numeric confidence",
			"confidence: high\n", `"confidence":"high",`,
			"`confidence` must be a number 0.0-1.0",
		},
		{
			"short sourceCommitSha",
			"sourceCommitSha: abc\n", `"sourceCommitSha":"abc",`,
			"`sourceCommitSha` must be a 7-40 char hex",
		},
		{
			"non-hex sourceCommitSha",
			"sourceCommitSha: zzzzzzz\n", `"sourceCommitSha":"zzzzzzz",`,
			"`sourceCommitSha` must be a 7-40 char hex",
		},
		{
			"captureMeta not an object",
			"captureMeta: bare\n", `"captureMeta":"bare",`,
			"`captureMeta` must be an object {classifier, dedupeKey, title}",
		},
		{
			"unknown captureMeta field",
			"captureMeta:\n  bogus: x\n", `"captureMeta":{"bogus":"x"},`,
			`unknown captureMeta field "bogus"`,
		},
		{
			"bad dedupeKey",
			"captureMeta:\n  dedupeKey: not-hex\n", `"captureMeta":{"dedupeKey":"not-hex"},`,
			"`captureMeta.dedupeKey` must be a 64-char hex hash",
		},
		{
			"non-string title",
			"captureMeta:\n  title:\n    - x\n", `"captureMeta":{"title":["x"]},`,
			"`captureMeta.title` must be a string",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, results := analyzeMarkdown("x.md", "/x.md", envelopeMarkdown(c.md), true); !hasFinding(results, c.want) {
				t.Errorf("markdown adapter produced no finding %q; findings: %+v", c.want, results)
			}
			if _, results := analyzeJSON("x.json", "/x.json", envelopeJSON(c.json), true); !hasFinding(results, c.want) {
				t.Errorf("json adapter produced no finding %q; findings: %+v", c.want, results)
			}
		})
	}
}

// hasFinding reports whether results carry a finding whose message contains
// want.
func hasFinding(results []Result, want string) bool {
	for _, r := range results {
		if strings.Contains(r.Message, want) {
			return true
		}
	}
	return false
}
