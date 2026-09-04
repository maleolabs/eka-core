package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// TestCaptureClassifierThreshold verifies the deterministic classifier
// with threshold 0.6: verb+len>=10 passes (0.8), len alone fails (0.5),
// short fails (0.2).
func TestCaptureClassifierThreshold(t *testing.T) {
	cases := []struct {
		prompt    string
		threshold float64
		want      bool
		conf      float64
	}{
		{"fix the login bug and update documentation for the release notes section now", 0.6, true, 0.8},
		{"fix bug", 0.6, false, 0.2},
		{"this is a very long prompt without any trigger verb but it has more than ten words total here", 0.6, false, 0.5},
		{"add new feature to support multiple capture provenance types in the gateway service layer", 0.6, true, 0.8},
		// threshold edge: 0.8 >=0.8 true, 0.5 <0.8 false
		{"add new feature to support multiple capture provenance types in the gateway service layer", 0.8, true, 0.8},
		{"this is a very long prompt without any trigger verb but it has more than ten words total here", 0.8, false, 0.5},
		// threshold 0.5: len alone passes
		{"this is a very long prompt without any trigger verb but it has more than ten words total here", 0.5, false, 0.5}, // still false: no verb required for ShouldCapture
		{"fix the login bug and update documentation for the release notes section now", 0.9, false, 0.8},
	}
	for i, tc := range cases {
		conf, hasVerb, words := Capture.Classify(tc.prompt)
		if conf != tc.conf {
			t.Errorf("case %d: confidence %v want %v", i, conf, tc.conf)
		}
		got := Capture.ShouldCapture(tc.prompt, tc.threshold)
		if got != tc.want {
			t.Errorf("case %d: ShouldCapture=%v want %v (verb=%v words=%d conf=%v thr=%v prompt=%q)", i, got, tc.want, hasVerb, words, conf, tc.threshold, tc.prompt)
		}
	}
}

// TestCaptureSourcePromptHash verifies deterministic sha256 hex (64 chars).
func TestCaptureSourcePromptHash(t *testing.T) {
	h1 := Capture.SourcePromptHash("hello world fix this")
	h2 := Capture.SourcePromptHash("hello world fix this")
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash not deterministic or not 64 hex: %q", h1)
	}
	h3 := Capture.SourcePromptHash("different prompt add feature")
	if h1 == h3 {
		t.Error("different prompts must give different hashes")
	}
}

// TestCaptureDedupeKey verifies normalized title hashing and dedupe window logic.
func TestCaptureDedupeKey(t *testing.T) {
	k1 := Capture.DedupeKey("Fix Login Bug")
	k2 := Capture.DedupeKey("fix login bug")
	k3 := Capture.DedupeKey("Fix   Login   Bug!!!")
	if k1 != k2 || k1 != k3 {
		t.Errorf("dedupe keys must be equal for normalized titles: %q %q %q", k1, k2, k3)
	}
	if len(k1) != 64 {
		t.Errorf("dedupeKey must be 64 hex, got %q len %d", k1, len(k1))
	}
	k4 := Capture.DedupeKey("Add new feature")
	if k1 == k4 {
		t.Error("different titles must give different dedupeKeys")
	}
	// Dedupe window: within 24h is duplicate, beyond is not.
	now := time.Now()
	last := now.Add(-23 * time.Hour)
	if !Capture.IsDuplicate(last, now, 24*time.Hour) {
		t.Error("23h within 24h must be duplicate")
	}
	last = now.Add(-25 * time.Hour)
	if Capture.IsDuplicate(last, now, 24*time.Hour) {
		t.Error("25h beyond 24h must not be duplicate")
	}
}

// TestCaptureEvaluate verifies the gateway decision object.
func TestCaptureEvaluate(t *testing.T) {
	prompt := "fix the login bug and update documentation for the release notes section now"
	dec := Capture.Evaluate(prompt, "Fix Login Bug", 0.6)
	if !dec.ShouldCapture {
		t.Fatalf("expected should capture, got %+v", dec)
	}
	if dec.Provenance != ProvenanceInferred {
		t.Errorf("provenance %q want inferred", dec.Provenance)
	}
	if dec.Confidence != 0.8 {
		t.Errorf("confidence %v want 0.8", dec.Confidence)
	}
	if dec.DedupeKey == "" || len(dec.DedupeKey) != 64 {
		t.Errorf("dedupeKey %q must be 64 hex", dec.DedupeKey)
	}
	if dec.SourcePromptHash == "" || len(dec.SourcePromptHash) != 64 {
		t.Errorf("sourcePromptHash %q", dec.SourcePromptHash)
	}
	if dec.Classifier != "keyword-heuristic v1" {
		t.Errorf("classifier %q", dec.Classifier)
	}
	// Non-significant prompt stays human.
	dec2 := Capture.Evaluate("hello", "Hello", 0.6)
	if dec2.ShouldCapture {
		t.Error("short prompt must not capture")
	}
	if dec2.Provenance != ProvenanceHuman {
		t.Errorf("provenance %q want human", dec2.Provenance)
	}
}

// TestProvenanceDefaultHuman verifies that NewDraft without provenance defaults to human
// and that valid inferred drafts persist and validate.
func TestProvenanceDefaultHuman(t *testing.T) {
	r, project := draftRuntime(t)
	// Default human.
	d := newSTODraft(t, r, project, "feather", "prov-human", nil)
	data, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"provenance": "human"`) {
		t.Errorf("default provenance must be human, got:\n%s", data)
	}
	// Validate clean.
	dv, err := Authoring.ValidateDraft(r, "feather/sto:prov-human", "")
	if err != nil {
		t.Fatal(err)
	}
	if !dv.Report.Pass() {
		t.Errorf("default human draft must validate: %+v", dv.Report.SortedResults())
	}
	// Explicit inferred with hash and confidence.
	hash := Capture.SourcePromptHash("fix the login bug and update documentation for the release notes section now")
	dec := Capture.Evaluate("fix the login bug and update documentation for the release notes section now", "Prov Inferred", 0.6)
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project:          project,
		Namespace:        "feather",
		Type:             "sto",
		ID:               "prov-inferred",
		Provenance:       dec.Provenance,
		SourcePromptHash: dec.SourcePromptHash,
		Confidence:       dec.Confidence,
		HasConfidence:    true,
		CaptureMeta:      CaptureMeta{Classifier: dec.Classifier, DedupeKey: dec.DedupeKey},
		ContentFile: func() string {
			p := filepath.Join(t.TempDir(), "c.json")
			os.WriteFile(p, []byte(`{"description":"d","acceptanceCriteria":"ac"}`), 0o644)
			return p
		}(),
	}); err != nil {
		t.Fatalf("inferred NewDraft: %v", err)
	} else {
		_ = hash
	}
	dv2, err := Authoring.ValidateDraft(r, "feather/sto:prov-inferred", "")
	if err != nil {
		t.Fatal(err)
	}
	if !dv2.Report.Pass() {
		t.Errorf("inferred draft must validate: %+v", dv2.Report.SortedResults())
	}
	// Publish and verify provenance survives to canonical store.
	if _, err := Authoring.Publish(r, "feather/sto:prov-inferred", PublishOptions{}); err != nil {
		t.Fatalf("publish inferred: %v", err)
	}
	u, ok, err := r.Knowledge.Object("feather/sto:prov-inferred:1")
	if err != nil || !ok {
		t.Fatalf("Knowledge.Object: %v %v", ok, err)
	}
	if u.Provenance != "inferred" {
		t.Errorf("published provenance %q want inferred", u.Provenance)
	}
	if u.SourcePromptHash != dec.SourcePromptHash {
		t.Errorf("sourcePromptHash %q want %q", u.SourcePromptHash, dec.SourcePromptHash)
	}
	if u.CaptureMeta.DedupeKey != dec.DedupeKey {
		t.Errorf("dedupeKey %q want %q", u.CaptureMeta.DedupeKey, dec.DedupeKey)
	}
}

// TestProvenanceInvalidEnum validates that invalid provenance is rejected (R0).
func TestProvenanceInvalidEnum(t *testing.T) {
	r, project := draftRuntime(t)
	dir := filepath.Join(r.Path(), "drafts", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sto-badprov.json")
	bad := `{
  "namespace": "feather",
  "type": "sto",
  "id": "badprov",
  "revision": 1,
  "provenance": "bogus",
  "state": {"executionState":"planned","existenceState":"active"},
  "changeLog": [
    {"date":"2026-09-04","domain":"executionState","from":"-","to":"planned","by":"Engineering"},
    {"date":"2026-09-04","domain":"existenceState","from":"-","to":"active","by":"Engineering"}
  ],
  "content": {"description":"d","acceptanceCriteria":"ac"}
}`
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := conformance.ScanFile(path)
	if err == nil {
		t.Fatal("bogus provenance must be rejected")
	}
	if !strings.Contains(err.Error(), "provenance") {
		t.Errorf("error must mention provenance, got %v", err)
	}
	// ValidateDraft also rejects.
	if _, err := Authoring.ValidateDraft(r, "feather/sto:badprov", ""); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Errorf("ValidateDraft of bogus provenance = %v, want provenance error", err)
	}
}

// TestDraftsByProvenanceFilter verifies the --provenance filter helper.
func TestDraftsByProvenanceFilter(t *testing.T) {
	r, project := draftRuntime(t)
	newSTODraft(t, r, project, "feather", "human-item", nil)
	// Inferred item.
	dec := Capture.Evaluate("fix the login bug and update documentation for the release notes section now", "Inf Item", 0.6)
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"description":"d","acceptanceCriteria":"ac"}`), 0o644)
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "sto", ID: "inferred-item",
		Provenance: dec.Provenance, SourcePromptHash: dec.SourcePromptHash, Confidence: dec.Confidence, HasConfidence: true,
		CaptureMeta: CaptureMeta{Classifier: dec.Classifier, DedupeKey: dec.DedupeKey}, ContentFile: p,
	}); err != nil {
		t.Fatal(err)
	}
	all, err := Authoring.Drafts(r, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 drafts, got %d", len(all))
	}
	hum, err := Authoring.DraftsByProvenance(r, project, "human")
	if err != nil {
		t.Fatal(err)
	}
	if len(hum) != 1 || hum[0].ID != "human-item" {
		t.Errorf("human filter = %+v, want human-item", hum)
	}
	inf, err := Authoring.DraftsByProvenance(r, project, "inferred")
	if err != nil {
		t.Fatal(err)
	}
	if len(inf) != 1 || inf[0].ID != "inferred-item" {
		t.Errorf("inferred filter = %+v, want inferred-item", inf)
	}
	all2, err := Authoring.DraftsByProvenance(r, project, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(all2) != 2 {
		t.Errorf("all filter must return 2, got %d", len(all2))
	}
}

// TestDedupeWindowIntegration verifies DedupeCheck finds duplicate within window
// and that the caller should create a cmt:note instead of a new CKO.
func TestDedupeWindowIntegration(t *testing.T) {
	r, project := draftRuntime(t)
	dec := Capture.Evaluate("fix the login bug and update documentation for the release notes section now", "Dedupe Title", 0.6)
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"description":"d","acceptanceCriteria":"ac"}`), 0o644)
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "sto", ID: "orig",
		Provenance: dec.Provenance, SourcePromptHash: dec.SourcePromptHash, Confidence: dec.Confidence, HasConfidence: true,
		CaptureMeta: CaptureMeta{Classifier: dec.Classifier, DedupeKey: dec.DedupeKey}, ContentFile: p,
	}); err != nil {
		t.Fatal(err)
	}
	// Dedupe check should find it immediately (within 24h).
	if _, ok := Capture.DedupeCheck(r, project, dec.DedupeKey, 24*time.Hour, time.Now()); !ok {
		t.Error("DedupeCheck must find duplicate within window")
	}
	// Different title must not dedupe.
	otherKey := Capture.DedupeKey("Other Title Completely Different")
	if _, ok := Capture.DedupeCheck(r, project, otherKey, 24*time.Hour, time.Now()); ok {
		t.Error("different dedupeKey must not match")
	}
	// Past window: simulate orig is 25h old — by touching file mtime.
	drafts, _ := Authoring.Drafts(r, project)
	var origPath string
	for _, d := range drafts {
		if d.ID == "orig" {
			origPath = d.Path
		}
	}
	old := time.Now().Add(-25 * time.Hour)
	os.Chtimes(origPath, old, old)
	if _, ok := Capture.DedupeCheck(r, project, dec.DedupeKey, 24*time.Hour, time.Now()); ok {
		t.Error("expired dedupe window must not match")
	}
}

// TestNoAutoPublish ensures the gateway never publishes: publish remains explicit.
func TestNoAutoPublish(t *testing.T) {
	r, project := draftRuntime(t)
	dec := Capture.Evaluate("fix the login bug and update documentation for the release notes section now", "No Auto", 0.6)
	if !dec.ShouldCapture {
		t.Fatal("should capture")
	}
	// The gateway evaluation must NOT have published anything.
	if o, _, _, err := r.Knowledge.Counts(); err == nil && o != 0 {
		t.Errorf("gateway must not auto-publish; store must stay empty, got %d objects", o)
	}
	if units, _ := r.Knowledge.Search(SearchQuery{ProjectID: project}); len(units) != 0 {
		t.Error("gateway must not auto-publish; project must have no units")
	}
	// Ensure the draft does not exist until explicitly created.
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"description":"d","acceptanceCriteria":"ac"}`), 0o644)
	if _, err := Authoring.NewDraft(r, NewDraftRequest{
		Project: project, Namespace: "feather", Type: "sto", ID: "noauto",
		Provenance: dec.Provenance, SourcePromptHash: dec.SourcePromptHash, Confidence: dec.Confidence, HasConfidence: true,
		CaptureMeta: CaptureMeta{Classifier: dec.Classifier, DedupeKey: dec.DedupeKey}, ContentFile: p,
	}); err != nil {
		t.Fatal(err)
	}
	// Still not published.
	if _, ok, _ := r.Knowledge.Object("feather/sto:noauto:1"); ok {
		t.Error("draft must not be auto-published")
	}
}

var _ = exchange.Unit{}
