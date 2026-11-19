package exchange

import (
	"testing"

	"github.com/maleolabs/eka-core/conformance"
)

// TestToArtifactCarriesFreshnessAndProvenance is the unit -> artifact
// direction of the envelope guard: ToArtifact projects the freshness fields
// (validUntil, lastConfirmed) and the ADR-035 capture fields the unit
// carries (provenance, sourcePromptHash, confidence, sourceCommitSha,
// captureMeta).
//
// Both directions are guarded: builder.go toUnit projects the same fields
// back (provenance_test.go owns the export -> import -> export contract,
// envelope_test.go the rest of the envelope), so neither direction can drop
// a claim and leave it write-only.
func TestToArtifactCarriesFreshnessAndProvenance(t *testing.T) {
	u := &Unit{
		Identity:              Identity{Namespace: "eka", Type: "adr", ID: "round-trip", InstanceVersion: 1},
		CanonicalIdentityForm: "eka/adr:round-trip:1",
		Revision:              1,
		Author:                conformance.User("Marij"),
		Created:               "2026-11-18",
		Updated:               "2026-11-18",
		StateVector:           StateVector{ContentState: "accepted"},
		Provenance:            conformance.ProvenanceInferred,
		SourcePromptHash:      "abc123",
		SourceCommitSha:       "deadbeef",
		Confidence:            0.75,
		CaptureMeta:           CaptureMeta{Classifier: "c", DedupeKey: "k"},
		ValidUntil:            "2027-06-30",
		LastConfirmed:         "2026-11-18",
		ChangeLog:             []ChangeLogEntry{},
		Relationships:         []Relationship{},
	}

	a := u.ToArtifact()

	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"validUntil", a.ValidUntil, "2027-06-30"},
		{"lastConfirmed", a.LastConfirmed, "2026-11-18"},
		{"provenance", a.Provenance, conformance.ProvenanceInferred},
		{"sourcePromptHash", a.SourcePromptHash, "abc123"},
		{"sourceCommitSha", a.SourceCommitSha, "deadbeef"},
		{"captureMeta.classifier", a.CaptureMeta.Classifier, "c"},
		{"captureMeta.dedupeKey", a.CaptureMeta.DedupeKey, "k"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q after unit -> artifact, want %q", c.name, c.got, c.want)
		}
	}
	if !a.HasConfidence || a.Confidence != 0.75 {
		t.Errorf("confidence = %v (has=%v), want 0.75", a.Confidence, a.HasConfidence)
	}
}

// TestUnitEnvelopeCarriesFreshness pins the builder direction for the new
// fields: an artifact's declared horizon reaches the unit (the same
// projection attach path the JSON adapter feeds).
func TestUnitEnvelopeCarriesFreshness(t *testing.T) {
	la := &loadedArtifact{Artifact: conformance.Artifact{
		Namespace:       "eka",
		Type:            "adr",
		ID:              "fresh",
		InstanceVersion: 1,
		Revision:        1,
		Author:          "Marij",
		AuthorKind:      conformance.KindUser,
		Created:         "2026-11-18",
		Updated:         "2026-11-18",
		ValidUntil:      "2027-06-30",
		LastConfirmed:   "2026-11-18",
		States:          map[string]string{conformance.DomainContentState: "accepted"},
		Relations:       map[string][]string{},
	}}
	la.content = []byte(`{}`)

	u := toUnit(la)
	if u.ValidUntil != "2027-06-30" || u.LastConfirmed != "2026-11-18" {
		t.Fatalf("toUnit dropped the horizon: validUntil=%q lastConfirmed=%q", u.ValidUntil, u.LastConfirmed)
	}
}

// TestToArtifactDefaultsProvenanceHuman pins the absence contract: a unit
// without a declared provenance still projects the human default, so the
// projection never turns an absent field into an empty one.
func TestToArtifactDefaultsProvenanceHuman(t *testing.T) {
	u := &Unit{Identity: Identity{Namespace: "eka", Type: "adr", ID: "x", InstanceVersion: 1}}
	if got := u.ToArtifact().Provenance; got != conformance.ProvenanceHuman {
		t.Fatalf("provenance = %q, want the human default", got)
	}
}
