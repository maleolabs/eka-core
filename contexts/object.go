// Package contexts implements the Context Engine of the EKA Runtime:
// it constructs the deterministic Engineering Context Object ("eka
// context") around one knowledge subject.
//
// The Context Engine is a Runtime consumer (ADR-014): it consumes
// Engineering Knowledge ONLY through the runtime package services
// (Resolver, Relations, Timeline, Knowledge) — it never parses
// Markdown, never touches the canonical store, and never implements
// graph traversal primitives beyond composing runtime service calls.
// The package imports only the runtime, exchange and conformance
// layers plus the standard library; the machine, view, store,
// workspace, sync and compile packages are deliberately out of scope.
//
// Determinism contract (mirror of the machine interface): fixed
// struct field order (declaration order), a stable schema string
// ("eka-context-v1", camelCase keys per spec-standard-v2 §5),
// sections and strata sorted by canonical form, deduplication by line
// form with first-role-wins, and no timestamps or host-dependent
// values. The engine is purely deterministic — no LLM, no randomness,
// no ambient state: the same subject, depth and options always
// produce the same Context Object.
//
// The context is the engineering lens around ONE unit: the focus
// (full unit detail), the higher-authority constraints (strata above
// the focus), the one-hop neighborhood classified into sections
// (upstream, downstream, dependencies, decisions, planning, review),
// the strata landscape of the collected units, and the focus's
// instance-line history. Consumers fetch full CKOs via `eka get`;
// sections carry compact Entry references, never full documents.
package contexts

import (
	"encoding/json"

	"github.com/maleolabs/eka-core/conformance"
)

// Schema is the canonical JSON schema identifier of the context
// interface: "eka-context-v1" (camelCase per spec-standard-v2 §5,
// WYSIWYG with the machine interface of `eka get`). Stable across
// minor releases.
const Schema = "eka-context-v1"

// Object is the Context Object: the deterministic machine projection
// of the engineering context around one subject. Field order is the
// fixed serialization order of the schema.
type Object struct {
	Schema string `json:"schema"`
	// Kind is the object kind discriminator: "context".
	Kind    string  `json:"kind"`
	Focus   Focus   `json:"focus"`
	Depth   string  `json:"depth"` // "local" | "dependency" | "engineering"
	Summary Summary `json:"summary"`
	// Strata carries only non-empty strata, stratum ascending (1..5).
	Strata   []Stratum `json:"strata"`
	Sections Sections  `json:"sections"`
}

// Focus is the full detail of the focus unit: the machine document
// shape (mirror of machine.Document minus schema/author/created/
// updated) plus the qualified line form.
type Focus struct {
	Identity Identity `json:"identity"`
	// CanonicalForm is the exact-instance form "<ns>/<type>:<id>:<v>".
	CanonicalForm string `json:"canonicalForm"`
	// LineForm is the qualified line form "<ns>/<type>:<id>".
	LineForm string `json:"lineForm"`
	// Number is the line's issue number (RFC: per-group incremental
	// numbers — "#<n>" addresses the line). Additive: 0 omits it
	// (projectID "" or an unnumbered line).
	Number            int             `json:"number,omitempty"`
	EngineeringDomain string          `json:"engineeringDomain"`
	Stratum           int             `json:"stratum"`
	Revision          int             `json:"revision,omitempty"`
	StateVector       StateVector     `json:"stateVector"`
	Phase             string          `json:"phase,omitempty"`
	Classification    *Classification `json:"classification,omitempty"`
	Provenance        string          `json:"provenance,omitempty"`
	Confidence        *float64        `json:"confidence,omitempty"`
	SourcePromptHash  string          `json:"sourcePromptHash,omitempty"`
	SourceCommitSha   string          `json:"sourceCommitSha,omitempty"`
	CaptureMeta       *CaptureMeta    `json:"captureMeta,omitempty"`
	Relationships     []Relationship  `json:"relationships,omitempty"`
	// Content is nil (absent from the JSON) when the build ran with
	// Options.NoContent.
	Content    *Content `json:"content,omitempty"`
	ObjectHash string   `json:"objectHash"`
}

// Identity is the complete identity tuple of the focus unit, in the
// fixed declared order (identical to the machine/RSF naming).
type Identity struct {
	Namespace       string `json:"namespace"`
	Type            string `json:"type"`
	ID              string `json:"id"`
	InstanceVersion int    `json:"instanceVersion"`
}

// StateVector carries the six owned state domains with the canonical
// unit.json naming (identical to the machine document). Values are
// never empty strings in a conformant repository, so omitempty loses
// nothing.
type StateVector struct {
	ContentState   string `json:"contentState,omitempty"`
	ExecutionState string `json:"executionState,omitempty"`
	PlanningState  string `json:"planningState,omitempty"`
	ContainerState string `json:"containerState,omitempty"`
	ExistenceState string `json:"existenceState,omitempty"`
	NoteState      string `json:"noteState,omitempty"`
}

// Classification carries the primary Knowledge Dimension, at most one
// secondary, and the Engineering Domain — omitted entirely (nil) when
// the focus unit carries none.
type Classification struct {
	Dimension           string   `json:"dimension,omitempty"`
	DimensionsSecondary []string `json:"dimensionsSecondary,omitempty"`
	Domain              string   `json:"domain,omitempty"`
}

// Relationship is one recorded relationship of the focus unit by
// Identity (stored order preserved).
type Relationship struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

// CaptureMeta holds classifier/dedupe metadata (ADR-035 v3).
type CaptureMeta struct {
	Classifier string `json:"classifier,omitempty"`
	DedupeKey  string `json:"dedupeKey,omitempty"`
}

// Content is the representation-tagged knowledge payload of the focus
// unit, never parsed or re-structured (the machine content shape:
// eka/structured-json/1 travels as `fields`, legacy
// eka/structured-text/1 as `text`; exactly one of the two is
// present).
type Content struct {
	Representation string          `json:"representation"`
	Text           string          `json:"text,omitempty"`
	Fields         json.RawMessage `json:"fields,omitempty"`
}

// Entry is a compact unit reference used in the sections and strata of
// the Context Object. It is NOT the full document: consumers fetch
// full CKOs via `eka get`. The canonical form addresses the exact
// instance; the line form addresses the line.
type Entry struct {
	// CanonicalForm is the exact-instance form "<ns>/<type>:<id>:<v>".
	CanonicalForm string `json:"canonicalForm"`
	// LineForm is the qualified line form "<ns>/<type>:<id>".
	LineForm string `json:"lineForm"`
	// Number is the line's issue number (0 omits it).
	Number int    `json:"number,omitempty"`
	Type   string `json:"type"`
	ID     string `json:"id"`
	Domain string `json:"domain"`
	// Stratum is the authority stratum of the unit's domain.
	Stratum int `json:"stratum"`
	// State is the primary state value of the unit (the primaryState
	// priority: execution-state, container-state, note-state,
	// planning-state, content-state).
	State string `json:"state,omitempty"`
	// Role is the reason the unit is in this section: the relationship
	// type that pulled it in, or "constraint" for higher-authority
	// units reached only through the bounded closure.
	Role       string `json:"role,omitempty"`
	ObjectHash string `json:"objectHash,omitempty"`
}

// Stratum is one non-empty stratum group of the collected units:
// every unit of the same authority stratum, sorted by canonical form.
// Strata are ordered ascending (1 = highest authority .. 5).
type Stratum struct {
	Stratum int     `json:"stratum"`
	Domain  string  `json:"domain"`
	Units   []Entry `json:"units"`
}

// Sections are the classified relationship neighborhoods of the
// Context Object. Every list is sorted by canonical form, deduplicated
// by line form; empty sections are absent from the JSON.
type Sections struct {
	// Upstream: the units the focus's relationships point at.
	Upstream []Entry `json:"upstream,omitempty"`
	// Downstream: the units that reference the focus.
	Downstream []Entry `json:"downstream,omitempty"`
	// Dependencies: the depends-on / derives-from outgoing targets.
	Dependencies []Entry `json:"dependencies,omitempty"`
	// Constraints: the collected units with a stratum strictly higher
	// than the focus's (a strictly smaller stratum number — higher
	// authority).
	Constraints []Entry `json:"constraints,omitempty"`
	// Decisions: adr- / dec- units among the collected set.
	Decisions []Entry `json:"decisions,omitempty"`
	// Planning: scp- / epc- / plan- / trc- units among the collected
	// set.
	Planning []Entry `json:"planning,omitempty"`
	// Review: rvw- / cmt- units among the collected set.
	Review []Entry `json:"review,omitempty"`
	// History: the focus instance-line history (the Runtime Timeline),
	// ascending instance-version.
	History []HistoryEntry `json:"history,omitempty"`
}

// HistoryEntry is one instance of the focus artifact line in the
// context history projection: the canonical form of the instance, its
// instance-version, revision, the immutable object hash the instance
// points at, and its change log in occurrence order (the machine
// timeline shape).
type HistoryEntry struct {
	CanonicalForm   string           `json:"canonicalForm"`
	InstanceVersion int              `json:"instanceVersion"`
	Revision        int              `json:"revision,omitempty"`
	ObjectHash      string           `json:"objectHash"`
	ChangeLog       []ChangeLogEntry `json:"changeLog,omitempty"`
}

// ChangeLogEntry is one recorded transition in occurrence order
// (the machine change-log shape).
type ChangeLogEntry struct {
	Date   string                     `json:"date"`
	Domain string                     `json:"domain"`
	From   string                     `json:"from"`
	To     string                     `json:"to"`
	By     conformance.AuthorIdentity `json:"by"`
}

// Summary is the deterministic count summary of the Context Object.
type Summary struct {
	// Focus is always 1 (the object has exactly one focus).
	Focus int `json:"focus"`
	// Units is the total distinct units across all strata (the focus
	// itself at local depth, the collected set at dependency and
	// engineering depth).
	Units int `json:"units"`
	// Sections is the count of non-empty sections (upstream /
	// downstream / dependencies / constraints / decisions / planning /
	// review — history excluded).
	Sections int `json:"sections"`
	// History is len(History) — the instance count of the focus line.
	History int `json:"history"`
}

// Marshal serializes the Context Object deterministically: two-space
// indentation (fixed struct field order = declaration order) plus a
// single trailing newline. The object is emitted compactly first and
// re-indented by json.Indent, so the structured content object
// (fields) is re-indented with the rest of the document while keeping
// its canonical key order (spec-standard-v2 §3.3).
func (o *Object) Marshal() ([]byte, error) {
	return marshal(o, false)
}

// MarshalCompact serializes the Context Object as a single JSON line
// plus a single trailing newline — the compact form of the same
// deterministic object (same field order, same values; scripts and
// MCP prefer it for payload economy).
func (o *Object) MarshalCompact() ([]byte, error) {
	return marshal(o, true)
}
