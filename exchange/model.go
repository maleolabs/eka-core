// Package exchange implements the EKA export engine: the official
// projection of an EKA repository onto the EKA Reference Serialization
// Format (RSF) v2.0 (reference/eka-reference-serialization-format-v1.1.md
// as amended by reference/spec-standard-v2.md: camelCase keys, structured
// JSON content).
//
// The package is the application-layer engine behind the `eka export`
// command (cmd/export.go), sibling to bootstrap/ and conformance/: public,
// reusable, and independent of the CLI layer. Per the Naming and
// Terminology Specification §7.3, engine packages are domain nouns —
// exchange/ is the import/export engine of the Exchange Contract
// (standard/eka-exchange-specification-v1.0.md §4).
//
// Pipeline (RSF §13.1):
//
//  1. validate the source repository (conformance.Validate; Export gate)
//  2. resolve the Export Scope deterministically (builder.go)
//  3. select units (no closure computation in v1 — dependency integrity
//     via External Reference Declarations, Exchange §12.3/§12.4)
//  4. detect External References and declare them
//  5. assemble the RSF object model (model.go)
//  6. project to deterministic byte entries (serialize.go)
//  7. compute integrity digests (SHA-256, RSF §9.4)
//  8. emit atomically: single-file .ekapkg (ZIP) or directory layout
//     (write.go)
//
// Determinism contract (RSF §9, §13.3): all collections ordered by the
// canonical Identity key; Change Log in occurrence order; Relationships
// ordered by (type, target); ZIP entries sorted; JSON fields in fixed
// declared order; digests over canonical bytes; no timestamps inside the
// package; no absolute paths; no host-dependent values. Two exports from
// identical repository state produce byte-identical packages.
package exchange

import (
	"strconv"

	"github.com/maleolabs/eka-core/conformance"
)

// Specification version constants, echoed into the Package Header and
// Manifest (Exchange §9.1, RSF §4.3).
const (
	// SpecificationVersion is the version of the EKA Exchange
	// Specification (and EKA standard) the exported knowledge conforms
	// to: "1.0".
	SpecificationVersion = "1.0"
	// ExchangeFormatVersion is the version of the Exchange Contract the
	// package is written against: "1".
	ExchangeFormatVersion = "1"
	// SerializationVersion is the RSF v2.0 Serialization Version carried
	// in the Package Identity Label suffix and echoed in the
	// Header/Manifest (RSF §4.1, §11.2; spec-standard-v2 §4): "2".
	SerializationVersion = "2"
	// LegacySerializationVersion is the highest legacy RSF Serialization
	// Version still accepted at import ("1.1"); the pre-1.1 line ("1")
	// remains importable too (spec-standard-v2 §4: v1.1 packages are
	// decoded with a legacy key map at import time, then re-emitted as
	// v2.0). The legacy line is never exported.
	LegacySerializationVersion = "1.1"
	// LegacySerializationVersionV1 is the pre-1.1 Serialization Version
	// still accepted at import (same legacy key map as "1.1").
	LegacySerializationVersionV1 = "1"
	// Exporter is the exporter identity recorded in the Package Header
	// (RSF §4.3; provenance per Exchange §17.2).
	Exporter = "eka"
	// ContentRepresentation is the registered v1 Representation
	// Identifier of the legacy representation (RSF §6.2/6.3): EKA
	// Structured Text. Still accepted at import; never emitted by v2.0
	// exports.
	ContentRepresentation = "eka/structured-text/1"
	// StructuredJSON is the registered v2.0 Representation Identifier of
	// the canonical default representation (spec-standard-v2 §1, §3.3):
	// EKA Structured JSON — the canonical content object, encoded
	// compactly with fixed key order.
	StructuredJSON = "eka/structured-json/1"
	// PackageExtension is the recommended extension for single-file
	// containers (RSF §4.2; convention, not contract).
	PackageExtension = ".ekapkg"
)

// ScopeKind is the declared type of export selection (Exchange §5.3,
// RSF §4.1). The values are the scope tokens used in the Package Identity
// Label and in serialized scope fields.
type ScopeKind string

const (
	// ScopeRepository exports all instances of all lines. Seed set: empty.
	ScopeRepository ScopeKind = "repo"
	// ScopeLine exports all instances of one Artifact Line. Seed set: the
	// line reference.
	ScopeLine ScopeKind = "line"
	// ScopeInstance exports exactly one Artifact Instance. Seed set: the
	// instance identity.
	ScopeInstance ScopeKind = "instance"
	// ScopeCollection exports the union of the resolved lines/instances
	// named by multiple targets. Seed set: the resolved target set.
	ScopeCollection ScopeKind = "collection"
)

// Options configures one Export run.
type Options struct {
	// Targets are canonical reference forms ("<type>:<id>[:<instance-version>]"
	// or cross-namespace "<ns>/<type>:<id>[:<version>]"). Empty means
	// Repository scope. Exactly one target without an instance-version
	// means Line scope; with one means Instance scope; several means
	// Collection scope.
	Targets []string
	// Output is the destination: "" auto-names "<label>.ekapkg" in the
	// current directory; an existing directory or a path ending in a path
	// separator selects the directory layout; any other path is written
	// as a single-file ZIP package at that path.
	Output string
	// Scope, when non-empty, must equal the scope derived from Targets;
	// a mismatch is a usage error. Empty means "derive from Targets".
	Scope ScopeKind
}

// Result reports one successful Export run.
type Result struct {
	// Label is the Package Identity Label (RSF §4.1):
	// "rsf-<scope>-<namespace>-<serialization-version>".
	Label string
	// Output is the absolute path the package was written to.
	Output string
	// Directory reports whether the package was written as a directory
	// layout (true) or as a single-file ZIP container (false).
	Directory bool
	// Package is the assembled RSF object model.
	Package *Package
	// Validation is the conformance report of the validation gate
	// (always passing when Export succeeds).
	Validation *conformance.Report
	// Counts are convenience mirrors of Package.Manifest.Counts.
	Units              int
	Attachments        int
	ExternalReferences int
}

// Package is the RSF object model of one exchange package (RSF §4.4): the
// six logical elements realizing the Exchange object model (Exchange
// §4.4): Package Header, Manifest, Unit Entries, Attachments Collection,
// Declarations Block, Integrity Block.
type Package struct {
	Header       Header
	Manifest     Manifest
	Units        []*Unit
	Attachments  []*Attachment
	Declarations Declarations
	Integrity    Integrity
}

// Header is the Package Header (RSF §4.3): the contract facts block
// realizing the Contract Header (Exchange §10.1). Field order is the fixed
// declared serialization order (RSF §9.3).
type Header struct {
	SerializationVersion  string    `json:"serializationVersion"`
	ExchangeFormatVersion string    `json:"exchangeFormatVersion"`
	SpecificationVersion  string    `json:"specificationVersion"`
	Exporter              string    `json:"exporter"`
	PackageIdentityLabel  string    `json:"packageIdentityLabel"`
	ExportScope           ScopeKind `json:"exportScope"`
	Namespace             string    `json:"namespace"`
}

// Manifest is the ordered package-level unit list plus scope/count/version
// echoes (RSF §8): the realization of the Exchange Manifest (Exchange
// §10.2, §10.6). PackageDigest echoes the authoritative package digest of
// the Integrity Block (RSF §8.1 "integrity information"; see serialize.go
// deviation 5 for why the manifest is excluded from the digest input).
type Manifest struct {
	Scope                 ScopeKind          `json:"scope"`
	PackageIdentityLabel  string             `json:"packageIdentityLabel"`
	SerializationVersion  string             `json:"serializationVersion"`
	ExchangeFormatVersion string             `json:"exchangeFormatVersion"`
	SpecificationVersion  string             `json:"specificationVersion"`
	PackageDigest         string             `json:"packageDigest"`
	Units                 []ManifestUnit     `json:"units"`
	Counts                Counts             `json:"counts"`
	Closure               ClosureDeclaration `json:"closure"`
}

// ManifestUnit is one entry of the Manifest ordered unit list (RSF §8.1).
// UnitDigest equals the per-unit digest recorded in the Integrity Block
// (RSF §9.4).
type ManifestUnit struct {
	CanonicalIdentityForm string `json:"canonicalIdentityForm"`
	Type                  string `json:"type"`
	ID                    string `json:"id"`
	Namespace             string `json:"namespace"`
	InstanceVersion       int    `json:"instanceVersion"`
	Revision              int    `json:"revision"`
	ContentRepresentation string `json:"contentRepresentation"`
	ContentFile           string `json:"contentFile"`
	UnitDigest            string `json:"unitDigest"`
}

// Counts is the Manifest dependency summary (RSF §8.1).
type Counts struct {
	Units              int `json:"units"`
	Attachments        int `json:"attachments"`
	ExternalReferences int `json:"externalReferences"`
	Extensions         int `json:"extensions"`
}

// ClosureDeclaration records the Export Scope and seed set from which the
// package contents were selected (Exchange §12.2, RSF §8.1). Seeds are
// normalized canonical reference forms; Repository scope carries an empty
// seed set.
type ClosureDeclaration struct {
	Scope ScopeKind `json:"scope"`
	Seeds []string  `json:"seeds"`
}

// Unit is one Exchange Unit projected to the RSF (RSF §5.1): the complete
// unit composition of the Exchange object model (Exchange §4.4).
type Unit struct {
	// Identity is the complete identity tuple (Exchange §6.1); never
	// omitted, derived, or defaulted.
	Identity Identity `json:"identity"`
	// CanonicalIdentityForm is the RSF canonical spelling of Identity
	// (RSF §5.2): "<namespace>/<type>:<id>:<instance-version>".
	CanonicalIdentityForm string `json:"canonicalIdentityForm"`
	// Revision travels as unit metadata, never as Identity (Exchange
	// §6.4); never an ordering key.
	Revision int `json:"revision"`
	// Author/Created/Updated are the repository frontmatter metadata
	// fields, preserved losslessly ("" when absent on the source).
	// Author carries the identity kind (RFC: user | agent | worker;
	// the legacy plain-string form is a user).
	Author  conformance.AuthorIdentity `json:"author,omitempty"`
	Created string                     `json:"created,omitempty"`
	Updated string                     `json:"updated,omitempty"`
	// StateVector carries all owned state domains with exact values
	// (Exchange §8.1); empty-vector types (e.g. tkt-) carry an empty
	// State block (Exchange §8.5, RSF §5.1.1).
	StateVector StateVector `json:"stateVector"`
	// ChangeLog carries the full transition history in occurrence order
	// (Exchange §8.2).
	ChangeLog []ChangeLogEntry `json:"changeLog"`
	// Relationships are all relationships by Identity, recorded on the
	// referring unit (Exchange §7.1-7.2), ordered by (type, target).
	Relationships []Relationship `json:"relationships"`
	// Classification is the primary Knowledge Dimension plus at most one
	// secondary (Exchange §4.4, P15). Empty for work items/projections
	// that carry none.
	Classification Classification `json:"classification"`
	// Phase is the context attribute on planning/scope artifacts
	// (Exchange §8.4); outside the State Vector, never a State Domain.
	Phase string `json:"phase,omitempty"`
	// Content references the representation-tagged payload (RSF §6).
	Content ContentRef `json:"content"`

	// ContentPayload is the raw payload bytes written to the unit's
	// content file; never serialized into JSON.
	ContentPayload []byte `json:"-"`
	// UnitDir is the package-relative directory of the unit entry
	// ("units/<namespace>/<type>-<id>-v<nn>"); never serialized.
	UnitDir string `json:"-"`
	// Digest is the per-unit SHA-256 over unit.json || content (RSF
	// §9.4); filled in by serialize.go; never serialized into unit.json.
	Digest string `json:"-"`
}

// Domain derives the Engineering Domain of the unit at load time from the
// artifact type token (conformance.DomainForToken — the single shared
// source of truth). It backs the graph/integrity checks of the import
// pipeline; the second return value is false only for unknown type
// tokens (rejected elsewhere before this is consulted).
func (u *Unit) Domain() (conformance.Domain, bool) {
	return conformance.DomainForToken(u.Identity.Type)
}

// Identity is the complete identity tuple (Exchange §6.1): (Namespace,
// Type, ID, InstanceVersion). Field order is the fixed declared order.
type Identity struct {
	Namespace       string `json:"namespace"`
	Type            string `json:"type"`
	ID              string `json:"id"`
	InstanceVersion int    `json:"instanceVersion"`
}

// CanonicalForm renders the RSF Canonical Identity Form (RSF §5.2):
// "<namespace>/<type>:<id>:<instance-version>".
func (i Identity) CanonicalForm() string {
	return i.Namespace + "/" + i.Type + ":" + i.ID + ":" + strconv.Itoa(i.InstanceVersion)
}

// StateVector carries the five owned state domains in the canonical
// declared order (conformance stateFields order). Omitempty keeps the
// serialized block to exactly the owned fields present; an empty-vector
// type serializes as {} (RSF §5.1.1). Values are never empty strings in a
// conformant repository, so omitempty loses nothing.
type StateVector struct {
	ContentState   string `json:"contentState,omitempty"`
	ExecutionState string `json:"executionState,omitempty"`
	PlanningState  string `json:"planningState,omitempty"`
	ContainerState string `json:"containerState,omitempty"`
	ExistenceState string `json:"existenceState,omitempty"`
	// NoteState is the note-state domain owned by cmt- artifacts
	// (ADR-019 D4): {open, resolved, dismissed}. Additive — only note
	// units carry it, so the serialized schema of every other unit is
	// byte-unchanged.
	NoteState string `json:"noteState,omitempty"`
}

// ChangeLogEntry is one recorded transition (Exchange §8.2): domain, old
// value, new value, time (date), authority. Serialized in occurrence
// order; the RSF never fabricates, truncates, or reorders entries.
// By carries the authority identity kind (RFC: user | agent | worker;
// the legacy plain-string form is a user).
type ChangeLogEntry struct {
	Date   string                     `json:"date"`
	Domain string                     `json:"domain"`
	From   string                     `json:"from"`
	To     string                     `json:"to"`
	By     conformance.AuthorIdentity `json:"by"`
}

// Relationship is one recorded relationship by Identity (Exchange §7.1),
// ordered by (type, target). The source is the unit's own Identity
// (constant per unit, so (type, target) ordering realizes the canonical
// key ordering of Exchange §10.5).
type Relationship struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

// Classification carries the primary Knowledge Dimension plus at most one
// secondary (canonical Section 8, P15), and the Engineering Domain of the
// unit's artifact type (EKA v1.1 Wave 1).
type Classification struct {
	Dimension           string   `json:"dimension,omitempty"`
	DimensionsSecondary []string `json:"dimensionsSecondary,omitempty"`
	// Domain is the Engineering Domain of the unit's artifact type,
	// derived from the type token via conformance.DomainForToken (the
	// single shared source of truth, like ParseReference). Written on
	// export, validated on import when present, and optional: packages
	// without the field derive the domain from the type token.
	Domain string `json:"domain,omitempty"`
}

// ContentRef is the representation-tagged payload reference (RSF §6.1):
// payload + Representation Identifier.
type ContentRef struct {
	Representation string `json:"representation"`
	File           string `json:"file"`
}

// Attachment is one supporting resource carried verbatim (RSF §7).
type Attachment struct {
	// ID is the Attachment ID: the repository-relative path with forward
	// slashes, deterministic and unique within the package (RSF §7.2).
	ID string `json:"id"`
	// Digest is the per-attachment SHA-256 (RSF §7.5, §9.4); filled in by
	// serialize.go; never serialized into the attachment itself.
	Digest string `json:"digest"`
	// Data is the raw attachment bytes; never serialized into JSON.
	Data []byte `json:"-"`
}

// Declarations is the package-level Declarations Block (RSF §4.4, §8.1;
// Exchange §10.4): Closure Declaration, External Reference Declarations,
// Extension Declarations. Attached at package level only — no per-unit
// declarations exist.
type Declarations struct {
	Closure            ClosureDeclaration  `json:"closure"`
	ExternalReferences []ExternalReference `json:"externalReferences"`
	Extensions         []ExtensionDecl     `json:"extensions"`
}

// ExternalReference declares one Relationship target whose Identity is not
// carried by the package (Exchange §12.3). Declarations are mandatory for
// every out-of-package target; an undeclared one makes the package invalid
// (Exchange §10.6).
type ExternalReference struct {
	Source string `json:"source"`
	Type   string `json:"type"`
	Target string `json:"target"`
}

// ExtensionDecl is a placeholder record of the Extension Declarations set
// (Exchange §16.3). RSF v1 exports carry no extensions: the only registered
// Content representation is eka/structured-text/1 and all relationships are
// canonical, so the v1 extension set is always empty. Unknown-field
// rejection (RSF §9.5) is an importer concern, not an emitter concern.
type ExtensionDecl struct{}

// Integrity is the Integrity Block (RSF §9.4, Exchange §17.1): the
// package-level SHA-256 over every other entry's bytes (in sorted entry
// order, excluding integrity.json itself), plus per-unit and
// per-attachment digests.
type Integrity struct {
	PackageDigest string             `json:"packageDigest"`
	Units         []UnitDigest       `json:"units"`
	Attachments   []AttachmentDigest `json:"attachments"`
}

// UnitDigest is one per-unit digest entry (RSF §9.4).
type UnitDigest struct {
	CanonicalIdentityForm string `json:"canonicalIdentityForm"`
	Digest                string `json:"digest"`
}

// AttachmentDigest is one per-attachment digest entry (RSF §7.5, §9.4).
type AttachmentDigest struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}
