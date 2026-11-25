package exchange

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envelope_test.go guards the transport half of the ADR-035 capture
// envelope: provenance (already covered by provenance_test.go),
// sourcePromptHash, confidence, sourceCommitSha, captureMeta{classifier,
// dedupeKey, title} and the freshness dates.
//
// The failure mode without the projection and the renderers is write-only
// metadata: an artifact declares a claim, the export silently drops it, and
// nothing reports a finding — the next import re-writes an artifact that
// looks human-authored. Fixing only the renderers would leave Markdown-native
// repositories in exactly that state, because their claim is read by the
// Markdown adapter, so both adapters are exercised end to end here.

// fixtureEnvelope is a repository with one artifact per adapter, each
// declaring the full envelope (see its README).
const fixtureEnvelope = "testdata/envelope"

// envelopeField is one declared envelope value to look for.
type envelopeField struct{ name, value string }

// TestImportRoundTripCarriesEnvelopeJSON: the JSON-native artifact's
// declared envelope reaches the package, the written authoring document,
// and the re-export unchanged.
func TestImportRoundTripCarriesEnvelopeJSON(t *testing.T) {
	pkgA := exportPackage(t, fixtureEnvelope)
	identity := Identity{Namespace: "eka-envelope-fixture", Type: "sto", ID: "capture-envelope", InstanceVersion: 1}

	entries, _ := readZIP(t, pkgA)
	unitJSON, ok := entries[UnitDirName(identity)+"/unit.json"]
	if !ok {
		t.Fatalf("package has no unit entry for %s", identity.CanonicalForm())
	}
	for _, f := range []envelopeField{
		{"provenance", `"provenance":"reconciled"`},
		{"sourcePromptHash", `"sourcePromptHash":"1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f899"`},
		{"confidence", `"confidence":0.75`},
		{"captureMeta.title", `"title":"fix login bug"`},
		{"validUntil", `"validUntil":"2027-06-30"`},
		{"lastConfirmed", `"lastConfirmed":"2026-11-18"`},
	} {
		if !strings.Contains(string(unitJSON), f.value) {
			t.Errorf("exported unit dropped %s (want %s):\n%s", f.name, f.value, truncate(unitJSON, 800))
		}
	}

	repoB := initTestRepo(t)
	mustImport(t, pkgA, ImportOptions{Root: repoB})

	imported := filepath.Join(repoB, "docs", "operating", "work-items", "stories", "sto-capture-envelope.json")
	data, err := os.ReadFile(imported)
	if err != nil {
		t.Fatalf("imported artifact missing: %v", err)
	}
	for _, f := range []envelopeField{
		{"provenance", `"provenance": "reconciled"`},
		{"confidence", `"confidence": 0.75`},
		{"captureMeta.title", `"title": "fix login bug"`},
		{"validUntil", `"validUntil": "2027-06-30"`},
	} {
		if !strings.Contains(string(data), f.value) {
			t.Errorf("imported authoring document dropped %s (want %s):\n%s", f.name, f.value, truncate(data, 800))
		}
	}

	assertPackagesIdentical(t, pkgA, exportPackage(t, repoB))
}

// TestImportRoundTripCarriesEnvelopeMarkdown: the same contract through the
// legacy Markdown adapter, and the explicit-zero half of it — the fixture's
// adr declares `confidence: 0.0`, which must arrive as a declared zero
// (hasConfidence in the unit, `"confidence": 0` in the written document) and
// not as an absent field.
func TestImportRoundTripCarriesEnvelopeMarkdown(t *testing.T) {
	pkgA := exportPackage(t, fixtureEnvelope)
	identity := Identity{Namespace: "eka-envelope-fixture", Type: "adr", ID: "002-envelope", InstanceVersion: 1}

	entries, _ := readZIP(t, pkgA)
	unitJSON := entries[UnitDirName(identity)+"/unit.json"]
	if !strings.Contains(string(unitJSON), `"provenance":"inferred"`) {
		t.Errorf("exported adr dropped provenance:\n%s", truncate(unitJSON, 800))
	}
	if !strings.Contains(string(unitJSON), `"title":"decide the adr 035 envelope"`) {
		t.Errorf("exported adr dropped captureMeta.title:\n%s", truncate(unitJSON, 800))
	}
	if !strings.Contains(string(unitJSON), `"hasConfidence":true`) {
		t.Errorf("exported adr dropped the declared zero confidence:\n%s", truncate(unitJSON, 800))
	}

	repoB := initTestRepo(t)
	mustImport(t, pkgA, ImportOptions{Root: repoB})

	imported := filepath.Join(repoB, "docs", "decisions", "adr-002-envelope.json")
	data, err := os.ReadFile(imported)
	if err != nil {
		t.Fatalf("imported artifact missing: %v", err)
	}
	for _, f := range []envelopeField{
		{"provenance", `"provenance": "inferred"`},
		{"confidence", `"confidence": 0`},
		{"captureMeta.dedupeKey", `"dedupeKey": "dcc691003c1ed7b6149b70d2aba3692ae1d3faf827bab6a650d2c2f965275371"`},
		{"sourceCommitSha", `"sourceCommitSha": "9f2c1ab"`},
		{"lastConfirmed", `"lastConfirmed": "2026-11-18"`},
	} {
		if !strings.Contains(string(data), f.value) {
			t.Errorf("imported authoring document dropped %s (want %s):\n%s", f.name, f.value, truncate(data, 800))
		}
	}

	assertPackagesIdentical(t, pkgA, exportPackage(t, repoB))
}

// TestImportRoundTripInventsNoEnvelope: the absence half. An artifact that
// declared nothing must not gain any envelope field in the cycle — writing a
// default would re-spell absence as a claim and change the bytes of every
// envelope-free artifact.
func TestImportRoundTripInventsNoEnvelope(t *testing.T) {
	pkgA := exportPackage(t, fixtureValid)
	repoB := initTestRepo(t)
	mustImport(t, pkgA, ImportOptions{Root: repoB})

	imported := filepath.Join(repoB, "docs", "operating", "work-items", "stories", "sto-login-email.json")
	data, err := os.ReadFile(imported)
	if err != nil {
		t.Fatalf("imported artifact missing: %v", err)
	}
	for _, field := range []string{"provenance", "sourcePromptHash", "confidence", "sourceCommitSha", "captureMeta", "validUntil", "lastConfirmed"} {
		if strings.Contains(string(data), field) {
			t.Errorf("imported artifact invented the %s field:\n%s", field, truncate(data, 800))
		}
	}

	assertPackagesIdentical(t, pkgA, exportPackage(t, repoB))
}

// TestCaptureMetaTitleHashesToDedupeKey pins the producer agreement the spec
// requires: the recorded title is the normalized text the dedupeKey was
// computed from, so sha256(title) must equal dedupeKey. The spec calls a
// mismatch a producer defect rather than an import failure, so this is an
// assertion, not a refusal — and it is asserted on both fixtures, because a
// renderer that normalized the title on the way out would break it silently.
func TestCaptureMetaTitleHashesToDedupeKey(t *testing.T) {
	entries, _ := readZIP(t, exportPackage(t, fixtureEnvelope))
	for _, id := range []Identity{
		{Namespace: "eka-envelope-fixture", Type: "adr", ID: "002-envelope", InstanceVersion: 1},
		{Namespace: "eka-envelope-fixture", Type: "sto", ID: "capture-envelope", InstanceVersion: 1},
	} {
		var u Unit
		mustUnmarshal(t, entries, UnitDirName(id)+"/unit.json", &u)
		sum := sha256.Sum256([]byte(u.CaptureMeta.Title))
		if got := hex.EncodeToString(sum[:]); got != u.CaptureMeta.DedupeKey {
			t.Errorf("%s: sha256(captureMeta.title) = %s, dedupeKey = %s; the stored title must be the key's preimage",
				id.CanonicalForm(), got, u.CaptureMeta.DedupeKey)
		}
	}
}
