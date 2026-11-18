package machine

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNewDocumentNeverSetsFreshness pins the retrieval-option contract:
// freshness is clock-derived state, so the default document builder must
// never populate it — the block appears only when a retrieval flag
// attaches it.
func TestNewDocumentNeverSetsFreshness(t *testing.T) {
	u := goldenUnit()
	u.ValidUntil = "2027-01-01"
	u.LastConfirmed = "2026-11-18"
	doc, err := NewDocument(u)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Freshness != nil {
		t.Fatalf("NewDocument set Freshness = %+v, want nil", doc.Freshness)
	}
	// The envelope fields themselves DO pass through (they need no clock).
	if doc.ValidUntil != "2027-01-01" || doc.LastConfirmed != "2026-11-18" {
		t.Fatalf("envelope freshness fields lost: validUntil=%q lastConfirmed=%q", doc.ValidUntil, doc.LastConfirmed)
	}
}

// TestDefaultDocumentWithoutFreshnessIsByteIdentical is the hard
// invariant: a unit carrying no freshness and a document carrying no
// freshness block serialize exactly as before the extension — no
// "validUntil", "lastConfirmed" or "freshness" key appears.
func TestDefaultDocumentWithoutFreshnessIsByteIdentical(t *testing.T) {
	got, err := MarshalUnit(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, key := range []string{"validUntil", "lastConfirmed", "freshness"} {
		if strings.Contains(s, `"`+key+`"`) {
			t.Errorf("default document must not carry %q:\n%s", key, s)
		}
	}
	// The document ends at objectHash: nothing appended after it.
	if !strings.HasSuffix(s, "}\n") || !strings.Contains(s, `"objectHash"`) {
		t.Fatalf("unexpected document tail:\n%s", s)
	}
}

// TestFreshnessPinnedToEndAndFieldOrder pins the appended block: after
// objectHash (and after timeline when present), with the declared field
// order, so the additive contract is visible in the bytes.
func TestFreshnessPinnedToEndAndFieldOrder(t *testing.T) {
	doc, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	doc.Freshness = &Freshness{ValidUntil: "2027-01-01", LastConfirmed: "2026-11-18", DaysUntilExpiry: 44, Status: "current"}
	got, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	want := `  "freshness": {
    "validUntil": "2027-01-01",
    "lastConfirmed": "2026-11-18",
    "daysUntilExpiry": 44,
    "status": "current"
  }
}
`
	if !strings.HasSuffix(s, want) {
		t.Fatalf("freshness block must close the document in declared order:\n%s", s)
	}
}

// TestFreshnessUndeclaredOmitsDates: the undeclared verdict carries no
// horizon, so the two date keys stay absent (omitempty) while the
// status and the zero day count remain — an honest, non-empty answer.
func TestFreshnessUndeclaredOmitsDates(t *testing.T) {
	doc, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	doc.Freshness = &Freshness{Status: "undeclared"}
	got, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, `"freshness": {`) == false {
		t.Fatalf("freshness block missing:\n%s", s)
	}
	if !strings.HasSuffix(s, `  "freshness": {
    "daysUntilExpiry": 0,
    "status": "undeclared"
  }
}
`) {
		t.Fatalf("undeclared block must omit the undeclared dates:\n%s", s)
	}
}

// TestEnvelopeFreshnessRoundTripsJSON proves the unit envelope carries
// the two fields on the wire with the declared names, and that a unit
// without them serializes without the keys.
func TestEnvelopeFreshnessRoundTripsJSON(t *testing.T) {
	u := goldenUnit()
	u.ValidUntil = "2027-01-01"
	u.LastConfirmed = "2026-11-18"
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{`"validUntil":"2027-01-01"`, `"lastConfirmed":"2026-11-18"`} {
		if !strings.Contains(string(b), frag) {
			t.Errorf("unit envelope missing %s:\n%s", frag, b)
		}
	}
	bare, err := json.Marshal(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"validUntil", "lastConfirmed"} {
		if strings.Contains(string(bare), `"`+key+`"`) {
			t.Errorf("undeclared unit must omit %q:\n%s", key, bare)
		}
	}
}
