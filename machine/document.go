// Package machine implements the machine interface of the EKA Runtime:
// it serializes Canonical Knowledge Objects (CKO) to the deterministic
// canonical JSON consumed by `eka get` and future machine consumers
// (MCP, Atrium, VS Code, AI agents, scripts).
//
// The machine interface is the machine-readable counterpart of the
// projection engine (view/): it never renders for readability, never
// parses Markdown, never touches storage, and never reuses projection
// renderers — pure CKO in, canonical JSON out.
//
// Determinism contract: fixed struct field order (declaration order),
// a stable schema string ("eka-cko-v2", camelCase per spec-standard-v2
// §5), and sorted inputs by canonical form (collections).
package machine

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// Schema is the canonical JSON schema identifier of the machine
// interface: "eka-cko-v2" (spec-standard-v2 §5 — camelCase naming,
// WYSIWYG with the v2.0 authoring and serialization conventions).
// Stable across minor releases.
const Schema = "eka-cko-v2"

// Document is the canonical machine projection of one Canonical
// Knowledge Object (one exchange.Unit). Field order is the fixed
// serialization order of the schema.
type Document struct {
	Schema            string   `json:"schema"`
	Identity          Identity `json:"identity"`
	CanonicalForm     string   `json:"canonicalForm"`
	EngineeringDomain string   `json:"engineeringDomain"`
	Stratum           int      `json:"stratum"`
	// Number is the line's issue number (RFC: per-group incremental
	// numbers — "#<n>" addresses the line). Additive: 0 omits it, so
	// the default document is byte-identical to the pre-number schema.
	Number         int                         `json:"number,omitempty"`
	Revision       int                         `json:"revision,omitempty"`
	Author         *conformance.AuthorIdentity `json:"author,omitempty"`
	Created        string                      `json:"created,omitempty"`
	Updated        string                      `json:"updated,omitempty"`
	StateVector    StateVector                 `json:"stateVector"`
	Phase          string                      `json:"phase,omitempty"`
	Classification *Classification             `json:"classification,omitempty"`
	Relationships  []Relationship              `json:"relationships,omitempty"`
	ChangeLog      []ChangeLogEntry            `json:"changeLog,omitempty"`
	Content        *Content                    `json:"content,omitempty"`
	ObjectHash     string                      `json:"objectHash"`
	// Retrieval options (ADR-015 additive contract): appended at the
	// END of the schema — absent until a retrieval flag asks for them,
	// so the default document is byte-identical to the pre-option
	// schema. NewDocument never sets them.
	Upstream   []*Document     `json:"upstream,omitempty"`
	Downstream []*Document     `json:"downstream,omitempty"`
	Timeline   []TimelineEntry `json:"timeline,omitempty"`
}

// Identity is the complete identity tuple of the CKO, in the fixed
// declared order (identical to the RSF unit.json naming).
type Identity struct {
	Namespace       string `json:"namespace"`
	Type            string `json:"type"`
	ID              string `json:"id"`
	InstanceVersion int    `json:"instanceVersion"`
}

// StateVector carries the six owned state domains with the canonical
// RSF unit.json naming. Values are never empty strings in a conformant
// repository, so omitempty loses nothing. NoteState (ADR-019 D4) is
// carried by cmt- note units only — additive, so documents of every
// other unit are byte-unchanged.
type StateVector struct {
	ContentState   string `json:"contentState,omitempty"`
	ExecutionState string `json:"executionState,omitempty"`
	PlanningState  string `json:"planningState,omitempty"`
	ContainerState string `json:"containerState,omitempty"`
	ExistenceState string `json:"existenceState,omitempty"`
	NoteState      string `json:"noteState,omitempty"`
}

// Relationship is one recorded relationship by Identity (stored order
// preserved).
type Relationship struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

// ChangeLogEntry is one recorded transition in occurrence order.
type ChangeLogEntry struct {
	Date   string                     `json:"date"`
	Domain string                     `json:"domain"`
	From   string                     `json:"from"`
	To     string                     `json:"to"`
	By     conformance.AuthorIdentity `json:"by"`
}

// TimelineEntry is one instance of an artifact line in the machine
// timeline projection: the canonical form of the instance, its
// instance-version, revision, the immutable object hash the instance
// points at, and its change log in occurrence order. The field names
// follow the document naming (canonicalForm, objectHash, changeLog) —
// the timeline array is part of the Document schema.
type TimelineEntry struct {
	CanonicalForm   string           `json:"canonicalForm"`
	InstanceVersion int              `json:"instanceVersion"`
	Revision        int              `json:"revision,omitempty"`
	ObjectHash      string           `json:"objectHash"`
	ChangeLog       []ChangeLogEntry `json:"changeLog,omitempty"`
}

// Classification carries the primary Knowledge Dimension, at most one
// secondary, and the Engineering Domain — omitted entirely (nil) when
// the CKO carries none.
type Classification struct {
	Dimension           string   `json:"dimension,omitempty"`
	DimensionsSecondary []string `json:"dimensionsSecondary,omitempty"`
	Domain              string   `json:"domain,omitempty"`
}

// Content is the representation-tagged knowledge payload: the opaque
// representation payload of the CKO, never parsed or re-structured.
// Text may be large — that is the knowledge content; it is never
// truncated.
//
// The payload shape follows the representation (spec-standard-v2 §5):
// eka/structured-json/1 content travels as `fields` — the canonical
// content object verbatim (byte-exact: the canonical payload bytes,
// re-indented with the document); legacy eka/structured-text/1 content
// travels as `text`. Exactly one of the two is present.
type Content struct {
	Representation string          `json:"representation"`
	Text           string          `json:"text,omitempty"`
	Fields         json.RawMessage `json:"fields,omitempty"`
}

// NewDocument maps one Canonical Knowledge Object (exchange.Unit) to
// its machine Document:
//
//   - identity tuple and canonical form pass through;
//   - engineeringDomain is Classification.Domain when non-empty, else
//     derived from the artifact type token via conformance.DomainForToken
//     (the single shared source of truth) — an unknown token is a
//     deterministic error;
//   - stratum is the authority stratum of the engineering domain
//     (conformance.Stratum);
//   - state vector, classification, relationships (stored order),
//     change log (occurrence order) pass through;
//   - content is {representation, text} for legacy structured-text
//     payloads and {representation, fields} for structured-json payloads
//     — fields carries the canonical content object verbatim (the
//     payload bytes; a hand-built unit whose structured-json payload is
//     not valid JSON degrades deterministically to the text shape);
//   - object_hash is the CKO digest ("" for hand-built units, kept
//     as-is).
func NewDocument(u *exchange.Unit) (*Document, error) {
	if u == nil {
		return nil, fmt.Errorf("machine: cannot build a document from a nil unit")
	}
	domain := u.Classification.Domain
	if domain == "" {
		d, ok := conformance.DomainForToken(u.Identity.Type)
		if !ok {
			return nil, fmt.Errorf("machine: unknown artifact type %q", u.Identity.Type)
		}
		domain = string(d)
	}
	content := &Content{Representation: u.Content.Representation}
	if u.Content.Representation == exchange.StructuredJSON && json.Valid(u.ContentPayload) {
		content.Fields = u.ContentPayload
	} else {
		content.Text = string(u.ContentPayload)
	}
	doc := &Document{
		Schema:            Schema,
		Identity:          Identity{Namespace: u.Identity.Namespace, Type: u.Identity.Type, ID: u.Identity.ID, InstanceVersion: u.Identity.InstanceVersion},
		CanonicalForm:     u.CanonicalIdentityForm,
		EngineeringDomain: domain,
		Stratum:           conformance.Stratum(conformance.Domain(domain)),
		Revision:          u.Revision,
		Author:            authorPtr(u.Author),
		Created:           u.Created,
		Updated:           u.Updated,
		StateVector: StateVector{
			ContentState:   u.StateVector.ContentState,
			ExecutionState: u.StateVector.ExecutionState,
			PlanningState:  u.StateVector.PlanningState,
			ContainerState: u.StateVector.ContainerState,
			ExistenceState: u.StateVector.ExistenceState,
			NoteState:      u.StateVector.NoteState,
		},
		Phase: u.Phase,
		// The content pointer: NewDocument always sets it, so the
		// default document carries content exactly as before — the
		// pointer (plus omitempty) exists so retrieval options can
		// strip it (StripContent, --no-content) without changing the
		// default schema.
		Content:    content,
		ObjectHash: u.Digest,
	}
	if u.Classification.Dimension != "" || len(u.Classification.DimensionsSecondary) > 0 || u.Classification.Domain != "" {
		c := Classification{
			Dimension:           u.Classification.Dimension,
			DimensionsSecondary: u.Classification.DimensionsSecondary,
			Domain:              u.Classification.Domain,
		}
		doc.Classification = &c
	}
	for _, r := range u.Relationships {
		doc.Relationships = append(doc.Relationships, Relationship{Type: r.Type, Target: r.Target})
	}
	for _, e := range u.ChangeLog {
		doc.ChangeLog = append(doc.ChangeLog, ChangeLogEntry{Date: e.Date, Domain: e.Domain, From: e.From, To: e.To, By: e.By})
	}
	return doc, nil
}

// Marshal serializes the Document deterministically: two-space
// indentation (fixed struct field order = declaration order) plus a
// single trailing newline. The document is emitted compactly first and
// re-indented by json.Indent, so the structured content object
// (fields) is re-indented with the rest of the document while keeping
// its canonical key order (spec-standard-v2 §3.3).
func (d *Document) Marshal() ([]byte, error) {
	return marshal(d, false)
}

// MarshalCompact serializes the Document as a single JSON line plus a
// single trailing newline — the compact form of the same deterministic
// document (same field order, same values; scripts and MCP prefer it
// for payload economy).
func (d *Document) MarshalCompact() ([]byte, error) {
	return marshal(d, true)
}

// marshal is the shared deterministic serializer of the machine
// interface: compact = json.Marshal (one line), pretty = the
// two-space-indented form; both end in a single trailing newline.
// The wire content is identical (same field order, same values) — only
// the whitespace differs, so both forms parse to the same document.
func marshal(v any, compact bool) ([]byte, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if !compact {
		var indented bytes.Buffer
		if err := json.Indent(&indented, out, "", "  "); err != nil {
			return nil, err
		}
		out = indented.Bytes()
	}
	return append(out, '\n'), nil
}

// StripContent removes the content payload from the document: the
// "content" key is absent from the serialized form (the pointer plus
// omitempty). Building the retrieval document — used by --no-content.
// Mutating by design: the document is built per retrieval, never
// shared.
func (d *Document) StripContent() {
	d.Content = nil
}

// AddRelated attaches the resolved relationship traversal of the
// retrieval to the document: upstream (the units the target's
// relationships point at) and downstream (the units that reference the
// target), each as Document arrays appended after object_hash. Nil
// slices stay absent from the JSON (omitempty) — pass nil for the
// traversals the retrieval did not request. Building the retrieval
// document — used by --upstream/--downstream.
func (d *Document) AddRelated(upstream, downstream []*Document) {
	d.Upstream = upstream
	d.Downstream = downstream
}

// AddTimeline attaches the instance-line history of the retrieval to
// the document as the "timeline" array (ascending instance-version,
// the line's history order). An empty line stays absent (nil), never
// an empty array. Building the retrieval document — used by
// --timeline.
func (d *Document) AddTimeline(entries []TimelineEntry) {
	if len(entries) == 0 {
		d.Timeline = nil
		return
	}
	d.Timeline = entries
}

// MarshalUnit is the convenience entry point: NewDocument + Marshal in
// one call. It is the function the machine consumers (eka get, MCP,
// Atrium, ...) call per CKO.
func MarshalUnit(u *exchange.Unit) ([]byte, error) {
	doc, err := NewDocument(u)
	if err != nil {
		return nil, err
	}
	return doc.Marshal()
}

// authorPtr returns the pointer form of an identity (nil for an empty
// identity — the omitempty contract of the machine document).
func authorPtr(a conformance.AuthorIdentity) *conformance.AuthorIdentity {
	if a.Name == "" {
		return nil
	}
	return &a
}
