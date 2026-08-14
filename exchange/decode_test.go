package exchange

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
)

// TestLoadPackageWithEntriesRoundtrip: the entries map returned
// alongside the model covers the raw unit.json and content entry of
// every unit, byte-identical to the entries the package carries.
func TestLoadPackageWithEntriesRoundtrip(t *testing.T) {
	out := filepath.Join(t.TempDir(), "layout")
	mustExport(t, fixtureValid, nil, out)

	pkg, entries, err := LoadPackageWithEntries(out)
	if err != nil {
		t.Fatalf("LoadPackageWithEntries: %v", err)
	}
	if len(pkg.Units) == 0 {
		t.Fatal("package carries no units")
	}
	for _, u := range pkg.Units {
		dir := UnitDirName(u.Identity)
		unitJSON, ok := entries[dir+"/unit.json"]
		if !ok {
			t.Errorf("entries missing %s/unit.json", dir)
			continue
		}
		content, ok := entries[dir+"/content"]
		if !ok {
			t.Errorf("entries missing %s/content", dir)
			continue
		}
		// The raw entries must be byte-identical to the model payloads.
		if !bytes.Equal(content, u.ContentPayload) {
			t.Errorf("entry content of %s differs from the model payload", u.CanonicalIdentityForm)
		}
		// The raw unit.json must strict-decode to the same unit.
		decoded, err := DecodeUnit(unitJSON, content)
		if err != nil {
			t.Errorf("DecodeUnit(%s): %v", u.CanonicalIdentityForm, err)
			continue
		}
		if decoded.CanonicalIdentityForm != u.CanonicalIdentityForm ||
			decoded.Revision != u.Revision ||
			decoded.StateVector.ContentState != u.StateVector.ContentState {
			t.Errorf("decoded unit %s does not match the model", u.CanonicalIdentityForm)
		}
	}
}

// TestMarshalUnitDeterminism: identical units marshal to identical
// bytes, and the bytes match the package's raw unit.json entry (the
// serializer contract — MarshalUnit is the same canonical projection).
func TestMarshalUnitDeterminism(t *testing.T) {
	out := filepath.Join(t.TempDir(), "layout")
	mustExport(t, fixtureValid, nil, out)
	pkg, entries, err := LoadPackageWithEntries(out)
	if err != nil {
		t.Fatalf("LoadPackageWithEntries: %v", err)
	}

	u := pkg.Units[0]
	first, err := MarshalUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("MarshalUnit must be deterministic")
	}
	// The unit's Digest is SHA-256(unit.json || content): MarshalUnit
	// must reproduce the exact bytes the digest covers.
	raw, ok := entries[UnitDirName(u.Identity)+"/unit.json"]
	if !ok {
		t.Fatalf("no raw entry for %s", u.CanonicalIdentityForm)
	}
	if !bytes.Equal(first, raw) {
		t.Error("MarshalUnit bytes differ from the package's raw unit.json entry")
	}
}

// TestDecodeUnitRoundtrip: decode(marshal(u)) reproduces every
// serialized field of u (identity, revision, metadata, state vector,
// change log, relationships, classification, phase, content ref) and
// attaches the content payload. Digest and UnitDir are never
// serialized and are not carried by the decode.
func TestDecodeUnitRoundtrip(t *testing.T) {
	u := &Unit{
		Identity: Identity{Namespace: "acme", Type: "adr", ID: "001", InstanceVersion: 2},
		// CanonicalIdentityForm is serialized and must round-trip; the
		// field is not recomputed on decode (the payload carries it).
		CanonicalIdentityForm: "acme/adr:001:2",
		Revision:              3,
		Author:                conformance.User("Eng"),
		Created:               "2026-08-01",
		Updated:               "2026-08-02",
		StateVector: StateVector{
			ContentState:   "stable",
			ExecutionState: "done",
			ExistenceState: "active",
		},
		ChangeLog: []ChangeLogEntry{
			{Date: "2026-08-01", Domain: "content-state", From: "draft", To: "stable", By: conformance.User("Eng")},
		},
		Relationships: []Relationship{
			{Type: "depends-on", Target: "acme/sto:login:1"},
		},
		Classification: Classification{
			Dimension:           "decisions",
			DimensionsSecondary: []string{"architecture"},
			Domain:              "Architecture",
		},
		Phase:   "wave-1",
		Content: ContentRef{Representation: ContentRepresentation, File: "content"},
	}
	unitJSON, err := MarshalUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("# Body\n")
	got, err := DecodeUnit(unitJSON, content)
	if err != nil {
		t.Fatalf("DecodeUnit: %v", err)
	}
	if got.Identity != u.Identity ||
		got.CanonicalIdentityForm != u.CanonicalIdentityForm ||
		got.Revision != u.Revision ||
		got.Author != u.Author || got.Created != u.Created || got.Updated != u.Updated ||
		got.StateVector != u.StateVector ||
		got.Phase != u.Phase ||
		got.Content != u.Content {
		t.Errorf("DecodeUnit roundtrip mismatch:\ngot  %+v\nwant %+v", got, u)
	}
	if len(got.ChangeLog) != 1 || got.ChangeLog[0] != u.ChangeLog[0] {
		t.Errorf("change log = %+v, want %+v", got.ChangeLog, u.ChangeLog)
	}
	if len(got.Relationships) != 1 || got.Relationships[0] != u.Relationships[0] {
		t.Errorf("relationships = %+v, want %+v", got.Relationships, u.Relationships)
	}
	if got.Classification.Dimension != u.Classification.Dimension ||
		len(got.Classification.DimensionsSecondary) != 1 ||
		got.Classification.DimensionsSecondary[0] != "architecture" ||
		got.Classification.Domain != u.Classification.Domain {
		t.Errorf("classification = %+v, want %+v", got.Classification, u.Classification)
	}
	if !bytes.Equal(got.ContentPayload, content) {
		t.Error("content payload not attached")
	}
}

// TestDecodeUnitRejectsUnknownField: the decode applies the package
// loader's reject-by-default policy (RSF §9.5): a unit.json carrying an
// unknown field must be refused, not silently dropped.
func TestDecodeUnitRejectsUnknownField(t *testing.T) {
	u := &Unit{
		Identity:              Identity{Namespace: "acme", Type: "sto", ID: "x", InstanceVersion: 1},
		CanonicalIdentityForm: "acme/sto:x:1",
		Revision:              1,
		Content:               ContentRef{Representation: ContentRepresentation, File: "content"},
	}
	unitJSON, err := MarshalUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	// Inject an unknown field into the unit.json bytes.
	tampered := bytes.Replace(unitJSON, []byte(`"revision":1`), []byte(`"revision":1,"bogus_field":true`), 1)
	if bytes.Equal(tampered, unitJSON) {
		t.Fatal("test setup: tampered bytes unchanged")
	}
	if _, err := DecodeUnit(tampered, []byte("body")); err == nil {
		t.Error("DecodeUnit must reject an unknown field")
	}
}
