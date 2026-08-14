package machine

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// goldenUnit is the hand-built CKO the golden tests marshal: one
// fully-populated adr unit (states, classification, relationships,
// change log, metadata, digest).
func goldenUnit() *exchange.Unit {
	return &exchange.Unit{
		Identity: exchange.Identity{
			Namespace:       "feather",
			Type:            "adr",
			ID:              "001-serialization",
			InstanceVersion: 1,
		},
		CanonicalIdentityForm: "feather/adr:001-serialization:1",
		Revision:              2,
		Author:                conformance.User("Engineering"),
		Created:               "2026-08-05",
		Updated:               "2026-08-06",
		StateVector: exchange.StateVector{
			ContentState:   "accepted",
			ExistenceState: "active",
		},
		ChangeLog: []exchange.ChangeLogEntry{
			{Date: "2026-08-05", Domain: "content-state", From: "proposed", To: "accepted", By: conformance.User("Engineering")},
		},
		Relationships: []exchange.Relationship{
			{Type: "depends-on", Target: "feather/sto:publish-post:1"},
		},
		Classification: exchange.Classification{
			Dimension: "decisions",
			Domain:    "Architecture",
		},
		Content: exchange.ContentRef{Representation: "eka/structured-text/1", File: "units/feather/adr-001-serialization-v1/content.md"},
		ContentPayload: []byte("# ADR-001 — Login serialization\n\n" +
			"## Context\n\nContext body.\n"),
		Digest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

// goldenJSONUnit is the hand-built structured-json CKO of the golden
// tests: the v2.0 content shape (spec-standard-v2 §3.3) — the canonical
// payload with required section keys first.
func goldenJSONUnit() *exchange.Unit {
	u := goldenUnit()
	u.Content = exchange.ContentRef{Representation: "eka/structured-json/1", File: "content"}
	u.ContentPayload = []byte(`{"context":"Context body.","decision":"Decision body.","consequences":"Consequences body.","alternativesConsidered":"Alternatives body."}`)
	return u
}

// TestGoldenFieldOrder pins the exact serialized bytes of a fully
// populated document: fixed field order (struct declaration order),
// stable schema string, two-space indent, single trailing newline.
func TestGoldenFieldOrder(t *testing.T) {
	want := `{
  "schema": "eka-cko-v2",
  "identity": {
    "namespace": "feather",
    "type": "adr",
    "id": "001-serialization",
    "instanceVersion": 1
  },
  "canonicalForm": "feather/adr:001-serialization:1",
  "engineeringDomain": "Architecture",
  "stratum": 2,
  "revision": 2,
  "author": "Engineering",
  "created": "2026-08-05",
  "updated": "2026-08-06",
  "stateVector": {
    "contentState": "accepted",
    "existenceState": "active"
  },
  "classification": {
    "dimension": "decisions",
    "domain": "Architecture"
  },
  "relationships": [
    {
      "type": "depends-on",
      "target": "feather/sto:publish-post:1"
    }
  ],
  "changeLog": [
    {
      "date": "2026-08-05",
      "domain": "content-state",
      "from": "proposed",
      "to": "accepted",
      "by": "Engineering"
    }
  ],
  "content": {
    "representation": "eka/structured-text/1",
    "text": "# ADR-001 — Login serialization\n\n## Context\n\nContext body.\n"
  },
  "objectHash": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
`
	got, err := MarshalUnit(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("serialized document differs:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestGoldenStructuredJSONContent: a structured-json CKO serializes its
// content as {representation, fields} — the canonical content object
// verbatim (key order preserved, re-indented with the document).
func TestGoldenStructuredJSONContent(t *testing.T) {
	want := `{
  "schema": "eka-cko-v2",
  "identity": {
    "namespace": "feather",
    "type": "adr",
    "id": "001-serialization",
    "instanceVersion": 1
  },
  "canonicalForm": "feather/adr:001-serialization:1",
  "engineeringDomain": "Architecture",
  "stratum": 2,
  "revision": 2,
  "author": "Engineering",
  "created": "2026-08-05",
  "updated": "2026-08-06",
  "stateVector": {
    "contentState": "accepted",
    "existenceState": "active"
  },
  "classification": {
    "dimension": "decisions",
    "domain": "Architecture"
  },
  "relationships": [
    {
      "type": "depends-on",
      "target": "feather/sto:publish-post:1"
    }
  ],
  "changeLog": [
    {
      "date": "2026-08-05",
      "domain": "content-state",
      "from": "proposed",
      "to": "accepted",
      "by": "Engineering"
    }
  ],
  "content": {
    "representation": "eka/structured-json/1",
    "fields": {
      "context": "Context body.",
      "decision": "Decision body.",
      "consequences": "Consequences body.",
      "alternativesConsidered": "Alternatives body."
    }
  },
  "objectHash": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
`
	got, err := MarshalUnit(goldenJSONUnit())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("serialized document differs:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestOmitemptySemantics: a unit without states, classification,
// relationships, change log or metadata serializes with those keys
// absent. The state_vector block is a value struct (never omitted by
// encoding/json) and serializes as an empty object — the RSF unit.json
// empty-vector behavior of §5.1.1.
func TestOmitemptySemantics(t *testing.T) {
	u := &exchange.Unit{
		Identity: exchange.Identity{
			Namespace:       "acme",
			Type:            "run",
			ID:              "backup",
			InstanceVersion: 1,
		},
		CanonicalIdentityForm: "acme/run:backup:1",
		Content:               exchange.ContentRef{Representation: "eka/structured-text/1"},
		ContentPayload:        []byte("# Runbook\n"),
	}
	doc, err := NewDocument(u)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Classification != nil {
		t.Errorf("classification must be nil for a unit without classification, got %+v", doc.Classification)
	}
	if len(doc.Relationships) != 0 || doc.Relationships != nil {
		t.Errorf("relationships must stay nil, got %v", doc.Relationships)
	}
	if len(doc.ChangeLog) != 0 || doc.ChangeLog != nil {
		t.Errorf("change_log must stay nil, got %v", doc.ChangeLog)
	}
	got, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, absent := range []string{
		`"revision"`, `"author"`, `"created"`, `"updated"`, `"phase"`,
		`"classification"`, `"relationships"`, `"changeLog"`,
	} {
		if strings.Contains(s, absent) {
			t.Errorf("serialized document must not carry %s:\n%s", absent, s)
		}
	}
	if !strings.Contains(s, `"stateVector": {}`) {
		t.Errorf("stateVector must serialize as an empty object (RSF §5.1.1 empty-vector behavior):\n%s", s)
	}
	// The derived engineering domain: Classification.Domain is empty,
	// so it comes from the type token ("run" -> Operations, stratum 5).
	if !strings.Contains(s, `"engineeringDomain": "Operations"`) || !strings.Contains(s, `"stratum": 5`) {
		t.Errorf("domain must be derived from the type token:\n%s", s)
	}
	// object_hash stays as-is for hand-built units ("" kept as-is).
	if !strings.Contains(s, `"objectHash": ""`) {
		t.Errorf("objectHash must be kept as-is (empty for hand-built units):\n%s", s)
	}
}

// TestContentEscaping: content with quotes, newlines and non-ASCII
// characters must serialize as valid JSON and round-trip losslessly.
func TestContentEscaping(t *testing.T) {
	body := "He said \"quoted\", tab\there,\nand unicode: café — 日本語 ✓\n"
	u := &exchange.Unit{
		Identity: exchange.Identity{
			Namespace: "acme", Type: "spec", ID: "x", InstanceVersion: 1,
		},
		CanonicalIdentityForm: "acme/spec:x:1",
		Content:               exchange.ContentRef{Representation: "eka/structured-text/1"},
		ContentPayload:        []byte(body),
	}
	out, err := MarshalUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) {
		t.Fatalf("output must be valid JSON:\n%s", out)
	}
	var doc Document
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("round-trip unmarshal failed: %v", err)
	}
	if doc.Content.Text != body {
		t.Errorf("round-trip text = %q, want %q", doc.Content.Text, body)
	}
}

// TestUnknownTypeToken: a unit whose type token has no home domain and
// no declared Classification.Domain is a deterministic error.
func TestUnknownTypeToken(t *testing.T) {
	u := &exchange.Unit{
		Identity: exchange.Identity{
			Namespace: "acme", Type: "wat", ID: "x", InstanceVersion: 1,
		},
		CanonicalIdentityForm: "acme/wat:x:1",
	}
	_, err := NewDocument(u)
	if err == nil {
		t.Fatal("NewDocument must fail for an unknown artifact type")
	}
	if got := err.Error(); got != `machine: unknown artifact type "wat"` {
		t.Errorf("error = %q, want deterministic message", got)
	}
}

// TestCollectionEmpty: an empty unit list yields a collection with
// count 0 and an empty unit list (never null), with the stable
// collection shape.
func TestCollectionEmpty(t *testing.T) {
	c, err := NewCollection("Execution", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Count != 0 || len(c.Units) != 0 {
		t.Errorf("count = %d, units = %d, want 0 and 0", c.Count, len(c.Units))
	}
	got, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schema": "eka-cko-v2",
  "collection": "domain",
  "domain": "Execution",
  "count": 0,
  "units": []
}
`
	if string(got) != want {
		t.Errorf("empty collection differs:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestCollectionSortedByCanonicalForm: the collection sorts its units
// by canonical form regardless of the input order (determinism
// contract). Documents carry no ordering metadata beyond the pinned
// field order, so the assertion reads the serialized units array.
func TestCollectionSortedByCanonicalForm(t *testing.T) {
	mk := func(ns, typ, id string, v int) *exchange.Unit {
		return &exchange.Unit{
			Identity:              exchange.Identity{Namespace: ns, Type: typ, ID: id, InstanceVersion: v},
			CanonicalIdentityForm: ns + "/" + typ + ":" + id + ":" + itoa(v),
			Content:               exchange.ContentRef{Representation: "eka/structured-text/1"},
			ContentPayload:        []byte("# " + id + "\n"),
		}
	}
	units := []*exchange.Unit{
		mk("zeta", "sto", "late", 1),
		mk("alpha", "adr", "first", 1),
		mk("alpha", "adr", "first", 2),
		mk("alpha", "sto", "zz", 1),
	}
	c, err := NewCollection("Execution", units)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"alpha/adr:first:1",
		"alpha/adr:first:2",
		"alpha/sto:zz:1",
		"zeta/sto:late:1",
	}
	for i, w := range want {
		if c.Units[i].CanonicalForm != w {
			t.Errorf("units[%d] = %q, want %q", i, c.Units[i].CanonicalForm, w)
		}
	}
}

// TestDeterminism: two marshals of the same input are byte-identical.
func TestDeterminism(t *testing.T) {
	a, err := MarshalUnit(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	b, err := MarshalUnit(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("two marshals of the same unit must be byte-identical")
	}
	c, err := NewCollection("Architecture", []*exchange.Unit{goldenUnit(), goldenUnit()})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	cb, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(ca) != string(cb) {
		t.Error("two collection marshals must be byte-identical")
	}
}

// TestRetrievalOptionsAbsentByDefault: the additive schema contract —
// a plain NewDocument carries no upstream/downstream/timeline keys at
// all (ADR-015: new fields appended, absent until requested), and the
// default output stays the pre-option schema.
func TestRetrievalOptionsAbsentByDefault(t *testing.T) {
	got, err := MarshalUnit(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, absent := range []string{`"upstream"`, `"downstream"`, `"timeline"`} {
		if strings.Contains(s, absent) {
			t.Errorf("default document must not carry %s (additive contract):\n%s", absent, s)
		}
	}
	// The golden contract: the default document is byte-identical to
	// the pre-option schema (TestGoldenFieldOrder pins it).
	doc, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	if doc.Upstream != nil || doc.Downstream != nil || doc.Timeline != nil {
		t.Errorf("NewDocument must never set the retrieval option fields, got %+v", doc)
	}
	if doc.Content == nil {
		t.Fatal("NewDocument must always set Content (default contract)")
	}
}

// TestMarshalCompact: the compact form is a single JSON line plus a
// single trailing newline, parses to the same document as the pretty
// form, and is deterministic (two compacts are byte-identical).
func TestMarshalCompact(t *testing.T) {
	doc, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	got, err := doc.MarshalCompact()
	if err != nil {
		t.Fatal(err)
	}
	pretty, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// Single line: exactly one trailing newline, no other newline.
	if strings.HasSuffix(string(got), "\n\n") || !strings.HasSuffix(string(got), "\n") {
		t.Errorf("compact must end in exactly one trailing newline, got %q", got)
	}
	if strings.Contains(strings.TrimSuffix(string(got), "\n"), "\n") {
		t.Errorf("compact must be a single line, got %q", got)
	}
	if !json.Valid(got) {
		t.Fatalf("compact must be valid JSON:\n%s", got)
	}
	// Parses equal to the pretty form (same document, same values).
	var a, b Document
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pretty, &b); err != nil {
		t.Fatal(err)
	}
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	if string(ga) != string(gb) {
		t.Errorf("compact and pretty must parse to the same document:\ncompact: %s\npretty: %s", got, pretty)
	}
	// Determinism: two compacts are byte-identical.
	again, err := doc.MarshalCompact()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(again) {
		t.Error("two compact marshals must be byte-identical")
	}
	// Collection compact form follows the same contract.
	col, err := NewCollection("Execution", []*exchange.Unit{goldenUnit()})
	if err != nil {
		t.Fatal(err)
	}
	cgot, err := col.MarshalCompact()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.TrimSuffix(string(cgot), "\n"), "\n") || !strings.HasSuffix(string(cgot), "\n") {
		t.Errorf("collection compact must be a single line plus trailing newline, got %q", cgot)
	}
	if !json.Valid(cgot) {
		t.Fatalf("collection compact must be valid JSON:\n%s", cgot)
	}
}

// TestStripContent: StripContent removes the content field from the
// serialized form — the key is absent entirely, the rest unchanged.
func TestStripContent(t *testing.T) {
	doc, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	doc.StripContent()
	if doc.Content != nil {
		t.Error("StripContent must nil the content pointer")
	}
	got, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, `"content"`) {
		t.Errorf("stripped document must not carry the content key:\n%s", s)
	}
	if !strings.Contains(s, `"objectHash"`) {
		t.Errorf("stripped document must keep the rest of the schema:\n%s", s)
	}
	// A stripped traversal document behaves the same.
	rel, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	rel.StripContent()
	rgot, err := rel.MarshalCompact()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rgot), `"content"`) {
		t.Errorf("stripped compact document must not carry the content key:\n%s", rgot)
	}
}

// TestAddRelatedAppendedAfterObjectHash: the traversal fields are
// appended AFTER object_hash (the additive schema contract — pinned by
// key order), upstream and downstream both carried, and the embedded
// documents are full machine documents.
func TestAddRelatedAppendedAfterObjectHash(t *testing.T) {
	doc, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	up, err := NewDocument(&exchange.Unit{
		Identity:              exchange.Identity{Namespace: "feather", Type: "sto", ID: "publish-post", InstanceVersion: 1},
		CanonicalIdentityForm: "feather/sto:publish-post:1",
		Content:               exchange.ContentRef{Representation: "eka/structured-text/1"},
		ContentPayload:        []byte("# STO — Publish post\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	down, err := NewDocument(&exchange.Unit{
		Identity:              exchange.Identity{Namespace: "feather", Type: "tkt", ID: "sto-publish-post", InstanceVersion: 1},
		CanonicalIdentityForm: "feather/tkt:sto-publish-post:1",
		Content:               exchange.ContentRef{Representation: "eka/structured-text/1"},
		ContentPayload:        []byte("# Ticket\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	doc.AddRelated([]*Document{up}, []*Document{down})
	got, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	// Key order: object_hash before upstream before downstream (the
	// additive contract — appended after the pre-option schema end).
	objHash := strings.Index(s, `"objectHash"`)
	upstream := strings.Index(s, `"upstream"`)
	downstream := strings.Index(s, `"downstream"`)
	if objHash < 0 || upstream < 0 || downstream < 0 {
		t.Fatalf("document must carry object_hash, upstream and downstream:\n%s", s)
	}
	if !(objHash < upstream && upstream < downstream) {
		t.Errorf("key order must be object_hash < upstream < downstream (additive append), got indices %d/%d/%d:\n%s", objHash, upstream, downstream, s)
	}
	// The embedded traversal documents are full machine documents
	// (schema, identity, canonical_form, content).
	for _, key := range []string{`"schema": "eka-cko-v2"`, `"canonicalForm": "feather/sto:publish-post:1"`, `"canonicalForm": "feather/tkt:sto-publish-post:1"`} {
		if !strings.Contains(s, key) {
			t.Errorf("traversal documents must be full machine documents (missing %s):\n%s", key, s)
		}
	}
	// Round-trip: the traversal arrays survive the wire.
	var back Document
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Upstream) != 1 || len(back.Downstream) != 1 {
		t.Errorf("round-trip upstream/downstream = %d/%d, want 1/1", len(back.Upstream), len(back.Downstream))
	}
	if back.Upstream[0].CanonicalForm != "feather/sto:publish-post:1" || back.Downstream[0].CanonicalForm != "feather/tkt:sto-publish-post:1" {
		t.Errorf("round-trip forms = %q / %q", back.Upstream[0].CanonicalForm, back.Downstream[0].CanonicalForm)
	}
}

// TestAddRelatedNilStaysAbsent: unrequested traversals stay absent —
// AddRelated(nil, nil) leaves the JSON free of both keys.
func TestAddRelatedNilStaysAbsent(t *testing.T) {
	doc, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	doc.AddRelated(nil, nil)
	got, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, absent := range []string{`"upstream"`, `"downstream"`} {
		if strings.Contains(s, absent) {
			t.Errorf("document must not carry %s when the traversal is nil:\n%s", absent, s)
		}
	}
	// One-sided traversal: only the requested field appears.
	up, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	doc.AddRelated([]*Document{up}, nil)
	got, err = doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s = string(got)
	if !strings.Contains(s, `"upstream"`) {
		t.Errorf("document must carry upstream when requested:\n%s", s)
	}
	if strings.Contains(s, `"downstream"`) {
		t.Errorf("document must not carry downstream when not requested:\n%s", s)
	}
}

// TestAddTimelineAndTimelineEntry: AddTimeline appends the timeline
// array after objectHash with the pinned entry field order
// (canonicalForm, instanceVersion, revision, objectHash, changeLog);
// an empty line stays absent (nil, never an empty array).
func TestAddTimelineAndTimelineEntry(t *testing.T) {
	doc, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	doc.AddTimeline([]TimelineEntry{
		{
			CanonicalForm:   "feather/plan:roadmap-v1:1",
			InstanceVersion: 1,
			Revision:        1,
			ObjectHash:      "aaaa",
			ChangeLog: []ChangeLogEntry{
				{Date: "2026-08-05", Domain: "content-state", From: "-", To: "draft", By: conformance.User("Engineering")},
			},
		},
		{
			CanonicalForm:   "feather/plan:roadmap-v1:2",
			InstanceVersion: 2,
			Revision:        2,
			ObjectHash:      "bbbb",
		},
	})
	got, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	objHash := strings.Index(s, `"objectHash"`)
	timeline := strings.Index(s, `"timeline"`)
	if objHash < 0 || timeline < 0 || objHash > timeline {
		t.Fatalf("timeline must be appended after object_hash (indices %d/%d):\n%s", objHash, timeline, s)
	}
	// The pinned entry field order inside the array — anchored on the
	// timeline section (the document's own change_log carries the same
	// fragment strings, e.g. domain/date values).
	timelineSection := s[timeline:]
	entryOrder := []string{
		`"canonicalForm": "feather/plan:roadmap-v1:1"`,
		`"instanceVersion": 1`,
		`"revision": 1`,
		`"objectHash": "aaaa"`,
		`"changeLog": [`,
		`"date": "2026-08-05"`,
		`"domain": "content-state"`,
		`"from": "-"`,
		`"to": "draft"`,
		`"by": "Engineering"`,
	}
	prev := -1
	for _, frag := range entryOrder {
		at := strings.Index(timelineSection, frag)
		if at < 0 {
			t.Errorf("timeline entry missing %s:\n%s", frag, timelineSection)
			continue
		}
		if at < prev {
			t.Errorf("timeline entry field order broken at %s:\n%s", frag, timelineSection)
		}
		prev = at
	}
	// The second entry: revision 2 without a change log (omitted).
	if !strings.Contains(s, `"canonicalForm": "feather/plan:roadmap-v1:2"`) {
		t.Errorf("timeline must carry both instances:\n%s", s)
	}
	// Instance order preserved (the runtime sorts the line ascending;
	// the machine projection keeps the given order).
	first := strings.Index(s, `"feather/plan:roadmap-v1:1"`)
	second := strings.Index(s, `"feather/plan:roadmap-v1:2"`)
	if first < 0 || second < 0 || first > second {
		t.Errorf("timeline instances must stay in ascending instance order:\n%s", s)
	}
	// Empty line: AddTimeline(nil) and AddTimeline([]) leave the
	// timeline key absent.
	empty, err := NewDocument(goldenUnit())
	if err != nil {
		t.Fatal(err)
	}
	empty.AddTimeline(nil)
	empty.AddTimeline([]TimelineEntry{})
	eout, err := empty.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(eout), `"timeline"`) {
		t.Errorf("an empty timeline must stay absent (nil), got:\n%s", eout)
	}
}

func itoa(v int) string {
	return strconv.Itoa(v)
}
