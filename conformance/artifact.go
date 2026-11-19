package conformance

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// This file implements the artifact-vs-convention-document classification
// and the frontmatter parsing that feeds every rule:
//
//	A .md file is an Artifact iff its YAML frontmatter contains BOTH
//	`type` and `id`. (reference-architecture.md §3, docs/README.md)
//	A .json file is an Artifact iff its top level contains BOTH
//	`type` and `id` (the v2.0 JSON-native authoring schema,
//	spec-standard-v2 §3.2).
//
// Files without both are Convention Documents (READMEs, protocol.md,
// validation.md, transfer.md, the canonical spec) and are skipped entirely.
// Files with exactly one of the two violate the artifact rule and are
// reported as malformed (R0 bucket, see report.go).
//
// Structural failures of the frontmatter block itself (unparseable YAML,
// unterminated block, non-mapping root, missing/invalid identity fields,
// invalid dates) are also reported in the R0 bucket because they are
// pre-conditions for the numbered rules rather than rule violations. The
// JSON adapter adds its own R0 findings: invalid JSON, unknown top-level
// fields, non-object content.

// Artifact is one parsed EKA artifact found under the scanned root.
type Artifact struct {
	// RelPath is the path relative to the scan root, used in results so
	// output is stable regardless of the machine.
	RelPath string
	// AbsPath is the absolute file path.
	AbsPath string

	// Identity (frontmatter is the source of truth).
	Namespace       string
	Type            string
	ID              string
	InstanceVersion int
	Revision        int
	// Author is the frontmatter `author` field, preserved as recorded
	// ("" when absent). It is unit metadata for exchange purposes and is
	// not checked by any rule (additive; no rule behavior depends on it).
	// AuthorKind is the identity kind (RFC: user | agent | worker; the
	// legacy plain-string form is a user — "" == user).
	Author     string
	AuthorKind string
	Created    string
	Updated    string
	// ValidUntil is the declared validity horizon (freshness ADR):
	// YYYY-MM-DD, "" when undeclared. LastConfirmed is the date a human
	// last asserted the content still holds — author-set, never written
	// by tooling, and NOT `updated` (which is the file modification
	// time). Both are advisory: the shape is structural, the value is
	// never compared against a clock by any rule.
	ValidUntil    string
	LastConfirmed string

	// States maps present state domain fields to their values. A domain
	// absent from this map is not present on the artifact.
	States map[string]string
	// Phase is the phase context attribute (scp-/plan- only).
	Phase       string
	HasPhase    bool
	HasPhaseKey bool

	// Classification (Rule 6).
	Dimension           string
	HasDimension        bool
	DimensionsSecondary []string

	// Domain is the raw frontmatter `domain` field value ("" when
	// absent). It is the declared Engineering Domain checked by Rule 11;
	// the derived home domain comes from DomainForToken (Rule 11:
	// absent = OK, derived).
	Domain string

	// Relations maps relationship field name -> raw reference strings.
	Relations map[string][]string

	// ChangeLog holds the parsed change-log entries in file order.
	ChangeLog []ChangeLogEntry

	// BodyLines are the content lines after the frontmatter block.
	BodyLines []string

	// ContentFields holds the structured content object of a JSON-native
	// artifact (spec-standard-v2 §3.2): the type's required section keys
	// (camelCase per SectionKey) plus allowed extra keys, values strings
	// or nested objects. Nil for Markdown artifacts — their content is
	// the BodyLines the section rules evaluate.
	ContentFields map[string]any

	// Provenance capture fields (ADR-035 v3 + spec provenance-capture:1).
	// Provenance is human|inferred|reconciled, default human for drafts
	// without the field. Persistence: stored as top-level draft fields and
	// mirrored into content for canonical store (non-breaking).
	Provenance       string
	SourcePromptHash string
	Confidence       float64
	HasConfidence    bool
	SourceCommitSha  string
	CaptureMeta      CaptureMeta
}

// CaptureMeta holds the classifier/dedupe metadata carried with inferred/
// reconciled drafts.
type CaptureMeta struct {
	Classifier string
	DedupeKey  string
	// Title is the NORMALIZED title text the DedupeKey was computed from
	// (spec provenance-capture:1): lowercase, non-alphanumerics folded to
	// spaces, whitespace collapsed and trimmed. Recording it is what lets
	// dedupe compare a new capture against the title that actually
	// produced the key — the key is a one-way hash, and the draft
	// identifier is a slug that need not resemble the title. A title that
	// does not hash to DedupeKey is a producer defect, not a structural
	// violation, so no rule refuses the mismatch.
	Title string
}

// Provenance constants (ADR-035 v3).
const (
	ProvenanceHuman      = "human"
	ProvenanceInferred   = "inferred"
	ProvenanceReconciled = "reconciled"
)

// provenanceValues is the provenance vocabulary in canonical order. The
// diagnostic that names the allowed values and the validator that accepts
// them share this list, so a new enum value cannot be accepted by one and
// reported as unknown by the other.
var provenanceValues = []string{ProvenanceHuman, ProvenanceInferred, ProvenanceReconciled}

// validProvenance reports whether p is a valid provenance enum value.
func ValidProvenance(p string) bool {
	return contains(provenanceValues, p)
}

// ChangeLogEntry is one parsed {date, domain, from, to, by} entry.
// ByKind is the authority identity kind (RFC: user | agent | worker;
// the legacy plain-string form is a user — "" == user).
type ChangeLogEntry struct {
	Date   string
	Domain string
	From   string
	To     string
	By     string
	ByKind string
}

// projectHeader is the exact projection header line required by Rule 8
// (validation.md Rule 8, ADR-003 §4). The em dash is U+2014.
const projectHeader = "> Generated \u2014 State Projection. Do NOT edit state here; refresh on read."

// ProjectionHeader is the exported form of projectHeader, for the
// authoring layer (draft templates scaffold it for projection types).
const ProjectionHeader = projectHeader

// analyzeFile classifies and parses one authoring file (.md or .json),
// dispatching to the format adapter by extension.
//
// It returns a nil artifact (and no results) for convention documents
// and for files without an artifact identity. Non-nil results are R0
// structural findings (malformed frontmatter, artifact-rule violations,
// missing or invalid identity fields).
func analyzeFile(relPath, absPath string, data []byte) (*Artifact, []Result) {
	return analyze(relPath, absPath, data, true)
}

// analyze is analyzeFile with one serialization-context switch:
// requireInstanceVersion reports whether a missing `instance-version`
// (frontmatter) / `instanceVersion` (JSON) field is an R0 structural
// violation.
//
// Repository authoring files (Validate, Scan) require the field; draft
// files (ScanFile, the draft-publish workflow) deliberately carry NO
// instance-version — it is assigned at publish time (the authoring spec
// §2.2) — so their classification tolerates the absence. Every other
// structural check is identical in both modes.
func analyze(relPath, absPath string, data []byte, requireInstanceVersion bool) (*Artifact, []Result) {
	if strings.HasSuffix(strings.ToLower(relPath), ".json") {
		return analyzeJSON(relPath, absPath, data, requireInstanceVersion)
	}
	return analyzeMarkdown(relPath, absPath, data, requireInstanceVersion)
}

// analyzeMarkdown is the legacy Markdown adapter: parses the YAML
// frontmatter block and classifies the kebab-keyed field map. Behavior
// is byte-identical to the v1.1 adapter (spec-standard-v2 §12 step 2:
// the Markdown path stays as-is).
func analyzeMarkdown(relPath, absPath string, data []byte, requireInstanceVersion bool) (*Artifact, []Result) {
	lines := strings.Split(string(data), "\n")
	if strings.TrimRight(lines[0], "\r") != "---" {
		// No frontmatter block: convention document, skipped entirely.
		return nil, nil
	}

	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return nil, []Result{{
			File:     relPath,
			Rule:     RuleStructural,
			Severity: SeverityError,
			Message:  "frontmatter block starts with --- but never closes",
		}}
	}

	var fm map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closeIdx], "\n")), &fm); err != nil {
		return nil, []Result{{
			File:     relPath,
			Rule:     RuleStructural,
			Severity: SeverityError,
			Message:  fmt.Sprintf("frontmatter is not valid YAML: %v", err),
		}}
	}
	if fm == nil {
		// An empty or comment-only block decodes to nil: treat it as a
		// document without type/id.
		fm = map[string]any{}
	}

	_, hasType := fm["type"]
	_, hasID := fm["id"]
	if hasType != hasID {
		return nil, []Result{{
			File:     relPath,
			Rule:     RuleStructural,
			Severity: SeverityError,
			Message:  "frontmatter contains exactly one of `type` and `id`; an artifact requires both (type XOR id is malformed)",
		}}
	}
	if !hasType {
		// Convention document: frontmatter without type AND without id.
		return nil, nil
	}

	a, results := classifyMap(relPath, absPath, fm, requireInstanceVersion)
	if a == nil {
		return nil, results
	}
	// The ADR-035 capture envelope is format-neutral: the Markdown
	// frontmatter carries the same keys as the JSON document, so it is
	// validated by the same pass. Reading it here is what makes the
	// envelope survive transport for Markdown-native repositories —
	// without it an export writes values the next import (through this
	// adapter) drops, and the claim is lost with no finding.
	results = append(results, classifyEnvelope(a, relPath, fm)...)
	a.BodyLines = lines[closeIdx+1:]
	return a, results
}

// allowedJSONTopLevel is the strict top-level field set of the v2.0 JSON
// authoring schema (spec-standard-v2 §3.2). Unknown top-level fields are
// rejected (R0) — strict schema, the same reject-by-default philosophy as
// the RSF.
//
// The envelope keys are appended from envelopeFields rather than restated
// here: an allowlisted field the envelope pass never reads (or a field it
// reads but the allowlist forgets) is a silent loss, and the table is the
// one place that knows which keys exist.
var allowedJSONTopLevel = func() map[string]bool {
	base := map[string]bool{
		"namespace":           true,
		"type":                true,
		"id":                  true,
		"instanceVersion":     true,
		"revision":            true,
		"author":              true,
		"created":             true,
		"updated":             true,
		"state":               true,
		"phase":               true,
		"dimension":           true,
		"dimensionsSecondary": true,
		"domain":              true,
		"relationships":       true,
		"changeLog":           true,
		"content":             true,
	}
	for _, f := range envelopeFields {
		base[f.key] = true
	}
	return base
}()

// analyzeJSON is the v2.0 JSON-native authoring adapter
// (spec-standard-v2 §3.2): parses the schema into the same Artifact
// model the Markdown adapter produces (state/relationship/change-log
// domain names converted to their kebab internal forms) plus
// ContentFields, the structured content object. Structural findings
// beyond the shared identity checks: invalid JSON, unknown top-level
// fields, non-object content, non-string/object content values,
// malformed state/relationships/changeLog blocks.
func analyzeJSON(relPath, absPath string, data []byte, requireInstanceVersion bool) (*Artifact, []Result) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, []Result{{
			File:     relPath,
			Rule:     RuleStructural,
			Severity: SeverityError,
			Message:  fmt.Sprintf("file is not valid JSON: %v", err),
		}}
	}
	if root == nil {
		root = map[string]any{}
	}

	_, hasType := root["type"]
	_, hasID := root["id"]
	if hasType != hasID {
		return nil, []Result{{
			File:     relPath,
			Rule:     RuleStructural,
			Severity: SeverityError,
			Message:  "file contains exactly one of `type` and `id`; an artifact requires both (type XOR id is malformed)",
		}}
	}
	if !hasType {
		// Convention document: JSON without type AND without id.
		return nil, nil
	}

	var results []Result
	for k := range root {
		if !allowedJSONTopLevel[k] {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: fmt.Sprintf("unknown top-level field %q (allowed: %s)", k, strings.Join(jsonTopLevelList(), ", ")),
			})
		}
	}

	// The structured content object: required top-level, must be an
	// object whose values are strings or nested objects (spec §3.2).
	var contentFields map[string]any
	contentRaw, hasContent := root["content"]
	if !hasContent {
		results = append(results, Result{
			File: relPath, Rule: RuleStructural, Severity: SeverityError,
			Message: "missing required top-level field `content`",
		})
	} else if m, ok := contentRaw.(map[string]any); !ok {
		results = append(results, Result{
			File: relPath, Rule: RuleStructural, Severity: SeverityError,
			Message: "`content` must be a JSON object whose keys are the type's required sections and allowed extra keys",
		})
	} else {
		for k, v := range m {
			if !isStringOrObject(v) {
				results = append(results, Result{
					File: relPath, Rule: RuleStructural, Severity: SeverityError,
					Message: fmt.Sprintf("content value %q must be a string or a nested object", k),
				})
			}
		}
		contentFields = m
	}

	// Normalize into the kebab-keyed frontmatter-shaped map the shared
	// classifier evaluates: identity/classification/metadata pass
	// through, state/relationships/changeLog domains convert to their
	// kebab internal names (the engine is v1.1-shaped; JSON is the
	// authoring spelling).
	fm := make(map[string]any, len(root))
	for _, k := range []string{"namespace", "type", "id", "revision", "author", "created", "updated", "phase", "dimension", "domain"} {
		if v, ok := root[k]; ok {
			fm[k] = v
		}
	}
	if v, ok := root["instanceVersion"]; ok {
		fm["instance-version"] = v
	}
	if v, ok := root["dimensionsSecondary"]; ok {
		fm["dimensions-secondary"] = v
	}

	// state: {contentState, ...} -> kebab domains; unknown domains are
	// structural findings.
	if v, ok := root["state"]; ok {
		sm, ok := v.(map[string]any)
		if !ok {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: "`state` must be an object of state domain -> value pairs",
			})
		} else {
			for k, val := range sm {
				kebab := stateKeyToKebab(k)
				if !isStateField(kebab) {
					results = append(results, Result{
						File: relPath, Rule: RuleStructural, Severity: SeverityError,
						Message: fmt.Sprintf("unknown state domain %q (allowed: %s)", k, strings.Join(stateFields, ", ")),
					})
					continue
				}
				fm[kebab] = val
			}
		}
	}

	// relationships: {dependsOn, ...} -> kebab fields; unknown
	// relationship types are structural findings (strict schema).
	if v, ok := root["relationships"]; ok {
		rm, ok := v.(map[string]any)
		if !ok {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: "`relationships` must be an object mapping relationship type -> array of references",
			})
		} else {
			for k, val := range rm {
				kebab := relationshipKeyToKebab(k)
				if !isRelationshipField(kebab) {
					results = append(results, Result{
						File: relPath, Rule: RuleStructural, Severity: SeverityError,
						Message: fmt.Sprintf("unknown relationship type %q (allowed: %s)", k, strings.Join(relationshipFields, ", ")),
					})
					continue
				}
				fm[kebab] = val
			}
		}
	}

	// changeLog: {date, domain, from, to, by} entries; domain uses the
	// camelCase state domain names (spec §3.2) and converts to kebab for
	// the engine.
	if v, ok := root["changeLog"]; ok {
		list, ok := v.([]any)
		if !ok {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: "`changeLog` must be a list of {date, domain, from, to, by} entries",
			})
		} else {
			converted := make([]any, 0, len(list))
			for _, item := range list {
				em, ok := item.(map[string]any)
				if !ok {
					converted = append(converted, item) // Rule 7 reports the malformed entry.
					continue
				}
				ne := make(map[string]any, len(em))
				for ek, ev := range em {
					ne[ek] = ev
				}
				if d, ok := ne["domain"].(string); ok {
					ne["domain"] = stateKeyToKebab(d)
				}
				converted = append(converted, ne)
			}
			fm["change-log"] = converted
		}
	}

	a, classifyResults := classifyMap(relPath, absPath, fm, requireInstanceVersion)
	results = append(results, classifyResults...)
	if a != nil {
		a.ContentFields = contentFields
		results = append(results, classifyEnvelope(a, relPath, root)...)
		if a.Provenance == "" {
			// The JSON adapter reports the human default explicitly
			// (ADR-035 v3: an undeclared provenance is human). The
			// Markdown adapter leaves it empty for the same claim; the
			// exchange envelope normalises both spellings to absence, so
			// the difference never reaches a package.
			a.Provenance = ProvenanceHuman
		}
	}
	return a, results
}

// --- Capture envelope (ADR-035 v3, spec provenance-capture:1). ---
//
// The envelope field set is declared ONCE, as data. Both adapters read the
// same camelCase keys (analyzeJSON from the document root, analyzeMarkdown
// from the frontmatter map), the JSON allowlist is derived from the table,
// and the exchange unit projection is checked against it by
// envelope_manifest_test.go. The failure this prevents is silent: a field
// added to the Artifact model and to the allowlist but not to the unit
// projection exports as "not declared", with no finding — the claim leaves
// the repository and nothing ever reports it.

// envelopeKind is the structural shape of one envelope value. Each kind has
// exactly one validation arm (validateEnvelopeValue), so the two adapters
// cannot disagree about a shape: they share the pass, not a copy of the
// rules.
type envelopeKind int

const (
	// envelopeProvenance: the provenance enum (human|inferred|reconciled;
	// human is the default by absence).
	envelopeProvenance envelopeKind = iota
	// envelopeHex64: exactly 64 hex characters (a sha256 digest).
	envelopeHex64
	// envelopeHex7to40: 7-40 hex characters (an abbreviated or full commit
	// sha).
	envelopeHex7to40
	// envelopeHex64OrEmpty: 64 hex characters or "" — an absent dedupeKey is
	// a legitimate capture state, unlike an absent prompt hash.
	envelopeHex64OrEmpty
	// envelopeConfidence: a number in [0.0, 1.0]; 0.0 is a claim, not
	// absence.
	envelopeConfidence
	// envelopeDate: a real calendar date in YYYY-MM-DD.
	envelopeDate
	// envelopeString: any string (the normalized capture title).
	envelopeString
	// envelopeCaptureMeta: the nested object whose fields are
	// captureMetaFields.
	envelopeCaptureMeta
)

// envelopeValue is one validated envelope value; exactly one arm is set, per
// the field's kind. set is false when nothing may be recorded: a refused
// shape records nothing (a zero value would re-spell invalid input as a
// declaration), while a nested block records the children that validated.
type envelopeValue struct {
	set  bool
	str  string
	num  float64
	meta CaptureMeta
}

// envelopeField is one field of the envelope: the metadata key (camelCase,
// read by both adapters), the kind that defines its shape, and where a
// validated value lands.
type envelopeField struct {
	key  string
	kind envelopeKind
	// flag is the boolean the exchange unit carries next to the value so a
	// declared zero stays distinct from absence (hasConfidence for
	// confidence; "" where none is needed — no other field can legally be
	// zero, since a zero-length hash or date is refused). It is data because
	// the unit projection is checked against this table.
	flag string
	// record stores a validated value on the artifact. The artifact is a
	// struct, so each field needs one line saying where its value goes; what
	// actually drifts — which keys exist, their shapes, their messages —
	// stays table-driven.
	record func(*Artifact, envelopeValue)
}

// envelopeFields is THE declaration of the capture envelope. The order is the
// diagnostic order: findings are appended in it.
var envelopeFields = []envelopeField{
	{key: "provenance", kind: envelopeProvenance,
		record: func(a *Artifact, v envelopeValue) { a.Provenance = v.str }},
	{key: "sourcePromptHash", kind: envelopeHex64,
		record: func(a *Artifact, v envelopeValue) { a.SourcePromptHash = v.str }},
	{key: "confidence", kind: envelopeConfidence, flag: "HasConfidence",
		record: func(a *Artifact, v envelopeValue) {
			a.Confidence = v.num
			a.HasConfidence = true
		}},
	{key: "sourceCommitSha", kind: envelopeHex7to40,
		record: func(a *Artifact, v envelopeValue) { a.SourceCommitSha = v.str }},
	{key: "captureMeta", kind: envelopeCaptureMeta,
		record: func(a *Artifact, v envelopeValue) { a.CaptureMeta = v.meta }},
	{key: "validUntil", kind: envelopeDate,
		record: func(a *Artifact, v envelopeValue) { a.ValidUntil = v.str }},
	{key: "lastConfirmed", kind: envelopeDate,
		record: func(a *Artifact, v envelopeValue) { a.LastConfirmed = v.str }},
}

// captureMetaField is one field of the nested captureMeta object.
type captureMetaField struct {
	key    string
	kind   envelopeKind
	record func(*CaptureMeta, envelopeValue)
}

// captureMetaFields is the allowed field set of the captureMeta object (spec
// provenance-capture:1): classifier, dedupeKey, and title — the normalized
// title text the dedupeKey was computed from. Any string is accepted as the
// title: the stored form is already normalized, and a title that disagrees
// with the key is a producer defect, not a structural violation — refusing it
// would reject a draft the runtime can still dedupe by key.
var captureMetaFields = []captureMetaField{
	{key: "classifier", kind: envelopeString,
		record: func(cm *CaptureMeta, v envelopeValue) { cm.Classifier = v.str }},
	{key: "dedupeKey", kind: envelopeHex64OrEmpty,
		record: func(cm *CaptureMeta, v envelopeValue) { cm.DedupeKey = v.str }},
	{key: "title", kind: envelopeString,
		record: func(cm *CaptureMeta, v envelopeValue) { cm.Title = v.str }},
}

// captureMetaKeys is the allowed child set, derived from the table so the
// shape diagnostic and the unknown-field refusal can never disagree about
// what is allowed.
var captureMetaKeys = func() []string {
	out := make([]string, 0, len(captureMetaFields))
	for _, f := range captureMetaFields {
		out = append(out, f.key)
	}
	return out
}()

// EnvelopeManifestEntry is one row of the envelope declaration as seen from
// outside this package: the metadata key plus the paired unit flag ("" when
// the field needs none).
type EnvelopeManifestEntry struct {
	Key  string
	Flag string
}

// EnvelopeManifest exposes the envelope field set to the anti-drift test,
// which must live where both models are importable. This package cannot
// import exchange (the dependency runs the other way: exchange projects units
// onto this package's Artifact), so the unit projection of the same set can
// only be checked from outside.
func EnvelopeManifest() []EnvelopeManifestEntry {
	out := make([]EnvelopeManifestEntry, 0, len(envelopeFields))
	for _, f := range envelopeFields {
		out = append(out, EnvelopeManifestEntry{Key: f.key, Flag: f.flag})
	}
	return out
}

// classifyEnvelope validates and records the ADR-035 capture envelope
// (provenance, sourcePromptHash, confidence, sourceCommitSha, captureMeta,
// validUntil, lastConfirmed) from a decoded metadata map, iterating
// envelopeFields.
//
// Both adapters call it with the map they decoded: the JSON root and the
// Markdown frontmatter. The envelope is format-neutral, so a claim must be
// validated identically whichever spelling it arrives in — a second copy of
// these checks is how one adapter starts accepting what the other refuses,
// which shows up as write-only metadata (exported, then dropped on
// re-import) rather than as a finding.
//
// Absence is honoured for every field: a missing key records nothing. The
// human provenance default is deliberately NOT applied here — the JSON
// adapter reports it, the Markdown adapter leaves the field empty, and the
// exchange envelope treats both as "no declaration".
func classifyEnvelope(a *Artifact, relPath string, meta map[string]any) []Result {
	var results []Result
	for _, f := range envelopeFields {
		raw, ok := meta[f.key]
		if !ok {
			continue
		}
		val, msgs := validateEnvelopeValue(f.kind, f.key, raw)
		for _, msg := range msgs {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: msg,
			})
		}
		if val.set {
			f.record(a, val)
		}
	}
	return results
}

// validateEnvelopeValue validates one envelope value against its kind and
// returns the value to record plus the findings, if any. The messages are
// part of the contract — they are what a repository author sees, and
// conformance/envelope_test.go pins them — so a kind's wording is written
// once, here.
func validateEnvelopeValue(kind envelopeKind, key string, v any) (envelopeValue, []string) {
	switch kind {
	case envelopeProvenance:
		s, ok := v.(string)
		if !ok {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be a string (%s)", key, strings.Join(provenanceValues, "|"))}
		}
		if !ValidProvenance(s) {
			return envelopeValue{}, []string{fmt.Sprintf("%s %q must be one of %s", key, s, strings.Join(provenanceValues, "|"))}
		}
		return envelopeValue{set: true, str: s}, nil
	case envelopeHex64:
		s, ok := v.(string)
		if !ok || s == "" {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be a non-empty hex string", key)}
		}
		if !isHexString(s, 64) {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be a 64-char hex sha256", key)}
		}
		return envelopeValue{set: true, str: s}, nil
	case envelopeHex7to40:
		s, ok := v.(string)
		if !ok || s == "" {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be a non-empty hex string", key)}
		}
		if !isHexString(s, -1) || len(s) < 7 || len(s) > 40 {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be a 7-40 char hex", key)}
		}
		return envelopeValue{set: true, str: s}, nil
	case envelopeHex64OrEmpty:
		s, ok := v.(string)
		if !ok {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be a string", key)}
		}
		if s != "" && !isHexString(s, 64) {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be a 64-char hex hash", key)}
		}
		return envelopeValue{set: true, str: s}, nil
	case envelopeConfidence:
		f, ok := asFloat64(v)
		if !ok {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be a number 0.0-1.0", key)}
		}
		if f < 0 || f > 1 {
			return envelopeValue{}, []string{fmt.Sprintf("%s %v must be 0.0-1.0", key, f)}
		}
		return envelopeValue{set: true, num: f}, nil
	case envelopeDate:
		s, ok := asDateString(v)
		if !ok || !validDate(s) {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be a date in YYYY-MM-DD format", key)}
		}
		return envelopeValue{set: true, str: s}, nil
	case envelopeString:
		s, ok := v.(string)
		if !ok {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be a string", key)}
		}
		return envelopeValue{set: true, str: s}, nil
	case envelopeCaptureMeta:
		m, ok := v.(map[string]any)
		if !ok {
			return envelopeValue{}, []string{fmt.Sprintf("`%s` must be an object {%s}", key, strings.Join(captureMetaKeys, ", "))}
		}
		var val envelopeValue
		val.set = true
		var msgs []string
		for _, child := range captureMetaFields {
			cv, ok := m[child.key]
			if !ok {
				continue
			}
			childVal, childMsgs := validateEnvelopeValue(child.kind, key+"."+child.key, cv)
			msgs = append(msgs, childMsgs...)
			if childVal.set {
				child.record(&val.meta, childVal)
			}
		}
		for k := range m {
			if !contains(captureMetaKeys, k) {
				msgs = append(msgs, fmt.Sprintf("unknown captureMeta field %q", k))
			}
		}
		return val, msgs
	}
	return envelopeValue{}, nil
}

// isStringOrObject reports whether v is a content value of the allowed
// shapes: strings, nested objects, and arrays (spec-standard-v2 §3.2
// documents strings/nested objects; the ADR-019 D7 note schemas add
// string arrays — changes/tests/notes/addresses — so arrays are accepted
// and their shapes are enforced per type by the note contract, R13).
func isStringOrObject(v any) bool {
	switch v.(type) {
	case string, map[string]any, []any:
		return true
	}
	return false
}

// isHexString reports whether s is hex of expected length (len<0 = any length).
func isHexString(s string, wantLen int) bool {
	if wantLen >= 0 && len(s) != wantLen {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// asFloat64 converts JSON numbers (float64, int, json.Number) to float64.
func asFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// jsonTopLevelList renders the allowed top-level field list, sorted
// (deterministic diagnostics).
func jsonTopLevelList() []string {
	out := make([]string, 0, len(allowedJSONTopLevel))
	for k := range allowedJSONTopLevel {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// classifyMap builds the Artifact model from a kebab-keyed frontmatter
// field map (the shared shape of both adapters) and runs the identity/
// metadata structural checks.
func classifyMap(relPath, absPath string, fm map[string]any, requireInstanceVersion bool) (*Artifact, []Result) {
	a := &Artifact{
		RelPath:   relPath,
		AbsPath:   absPath,
		States:    map[string]string{},
		Relations: map[string][]string{},
	}
	var results []Result

	// --- Identity fields. ---
	a.Type, _ = asString(fm["type"])
	if _, ok := typeTokens[a.Type]; !ok {
		results = append(results, Result{
			File: relPath, Rule: RuleStructural, Severity: SeverityError,
			Message: fmt.Sprintf("unknown artifact type %q; expected one of the %d EKA type tokens", a.Type, len(typeTokens)),
		})
	}
	a.ID, _ = asString(fm["id"])
	if a.ID == "" {
		results = append(results, Result{
			File: relPath, Rule: RuleStructural, Severity: SeverityError,
			Message: "artifact id must be a non-empty string",
		})
	}

	a.Namespace, _ = asString(fm["namespace"])
	if a.Namespace == "" {
		results = append(results, Result{
			File: relPath, Rule: RuleStructural, Severity: SeverityError,
			Message: "missing required identity field `namespace`",
		})
	}

	if v, ok := fm["instance-version"]; ok {
		if n, valid := asInt(v); valid {
			a.InstanceVersion = n
			if n < 1 {
				results = append(results, Result{
					File: relPath, Rule: RuleStructural, Severity: SeverityError,
					Message: "`instance-version` must be >= 1",
				})
			}
		} else {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: "`instance-version` must be an integer",
			})
		}
	} else if requireInstanceVersion {
		results = append(results, Result{
			File: relPath, Rule: RuleStructural, Severity: SeverityError,
			Message: "missing required identity field `instance-version`",
		})
	}

	if v, ok := fm["revision"]; ok {
		if n, valid := asInt(v); valid {
			a.Revision = n
		} else {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: "`revision` must be an integer",
			})
		}
	} else {
		results = append(results, Result{
			File: relPath, Rule: RuleStructural, Severity: SeverityError,
			Message: "missing required identity field `revision`",
		})
	}
	if a.Revision < 1 {
		results = append(results, Result{
			File: relPath, Rule: RuleStructural, Severity: SeverityError,
			Message: "`revision` must be >= 1",
		})
	}

	// Author: unit metadata preserved for exchange (RFC: identity
	// kinds). A plain string is a user; the structured
	// {"kind", "name"} object declares an agent or worker. Malformed
	// identities are structural findings (R0).
	if v, ok := fm["author"]; ok {
		switch t := v.(type) {
		case string:
			a.Author = t
			a.AuthorKind = KindUser
		case map[string]any:
			name, _ := asString(t["name"])
			kind, _ := asString(t["kind"])
			if kind == "" {
				kind = KindUser
			}
			if !IsAuthorKind(kind) || name == "" {
				results = append(results, Result{
					File: relPath, Rule: RuleStructural, Severity: SeverityError,
					Message: fmt.Sprintf("`author` object requires kind (%s) and a non-empty name", strings.Join(AuthorKinds, " | ")),
				})
			} else {
				a.Author = name
				a.AuthorKind = kind
			}
		default:
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: "`author` must be a string or an object {\"kind\", \"name\"}",
			})
		}
	}

	// created/updated are unit metadata (not envelope): the same date shape,
	// produced by the same kind validator, so the wording cannot diverge from
	// the freshness dates envelopeFields validates. The validity horizon
	// (validUntil/lastConfirmed) is an envelope field and is read by
	// classifyEnvelope with the rest of the envelope — a malformed value is R0
	// there too, so a typo never degrades silently into "undeclared", and the
	// values themselves are never checked against a clock.
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"created", &a.Created},
		{"updated", &a.Updated},
	} {
		v, ok := fm[f.key]
		if !ok {
			continue
		}
		val, msgs := validateEnvelopeValue(envelopeDate, f.key, v)
		for _, msg := range msgs {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: msg,
			})
		}
		if val.set {
			*f.dst = val.str
		}
	}

	// --- State fields (Rules 3 and 4 operate on these). ---
	for _, domain := range stateFields {
		if v, ok := fm[domain]; ok {
			if s, isStr := asString(v); isStr {
				a.States[domain] = s
			} else {
				results = append(results, Result{
					File: relPath, Rule: RuleStructural, Severity: SeverityError,
					Message: fmt.Sprintf("state field `%s` must be a string", domain),
				})
			}
		}
	}
	if v, ok := fm[DomainPhase]; ok {
		a.HasPhaseKey = true
		if s, isStr := asString(v); isStr {
			a.Phase = s
			a.HasPhase = true
		} else {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: "`phase` must be a string",
			})
		}
	}

	// --- Classification (Rule 6). ---
	if v, ok := fm["dimension"]; ok {
		a.HasDimension = true
		if s, isStr := asString(v); isStr {
			a.Dimension = s
		} else {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: "`dimension` must be a string",
			})
		}
	}
	if v, ok := fm["dimensions-secondary"]; ok {
		list, valid := asStringList(v)
		if !valid {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: "`dimensions-secondary` must be a list of strings",
			})
		} else {
			a.DimensionsSecondary = list
		}
	}

	// --- Engineering Domain (Rule 11). ---
	if v, ok := fm["domain"]; ok {
		if s, isStr := asString(v); isStr {
			a.Domain = s
		} else {
			results = append(results, Result{
				File: relPath, Rule: RuleStructural, Severity: SeverityError,
				Message: "`domain` must be a string",
			})
		}
	}

	// --- Relationships (Rule 5). ---
	for _, field := range relationshipFields {
		if v, ok := fm[field]; ok {
			list, valid := asStringList(v)
			if !valid {
				results = append(results, Result{
					File: relPath, Rule: Rule5, Severity: SeverityError,
					Message: fmt.Sprintf("relationship field `%s` must be a list of references", field),
				})
			} else {
				a.Relations[field] = list
			}
		}
	}

	// --- Change-log (Rule 7). ---
	if v, ok := fm["change-log"]; ok {
		list, valid := asList(v)
		if !valid {
			results = append(results, Result{
				File: relPath, Rule: Rule7, Severity: SeverityError,
				Message: "`change-log` must be a list of {date, domain, from, to, by} entries",
			})
		} else {
			for i, item := range list {
				entry, err := parseChangeLogEntry(item)
				if err != nil {
					results = append(results, Result{
						File: relPath, Rule: Rule7, Severity: SeverityError,
						Message: fmt.Sprintf("change-log entry %d is malformed: %v", i+1, err),
					})
					continue
				}
				a.ChangeLog = append(a.ChangeLog, entry)
			}
		}
	}

	// --- Content. ---
	// The Markdown adapter sets BodyLines after classification; the JSON
	// adapter sets ContentFields. The engine's section rules dispatch on
	// which of the two is present.

	return a, results
}

// parseChangeLogEntry validates one change-log entry's shape. Domain
// ownership, value validity and transition legality are checked by Rule 7.
func parseChangeLogEntry(item any) (ChangeLogEntry, error) {
	m, ok := item.(map[string]any)
	if !ok {
		return ChangeLogEntry{}, fmt.Errorf("entry must be a mapping")
	}
	var e ChangeLogEntry
	required := []struct {
		key    string
		target *string
	}{
		{"date", &e.Date}, {"domain", &e.Domain}, {"from", &e.From}, {"to", &e.To},
	}
	for _, r := range required {
		v, ok := m[r.key]
		if !ok {
			return ChangeLogEntry{}, fmt.Errorf("missing required field %q", r.key)
		}
		if r.key == "date" {
			// The canonical ADRs write unquoted dates (e.g.
			// `date: 2026-08-05`), which yaml.v3 resolves as a
			// timestamp node; normalize it back to YYYY-MM-DD.
			s, isStr := asDateString(v)
			if !isStr {
				return ChangeLogEntry{}, fmt.Errorf("field %q must be a date", r.key)
			}
			e.Date = s
			continue
		}
		s, isStr := v.(string)
		if !isStr {
			return ChangeLogEntry{}, fmt.Errorf("field %q must be a string", r.key)
		}
		*r.target = s
	}
	// The authority (by): a plain string is a user; the structured
	// {"kind", "name"} object declares an agent or worker (RFC).
	by, ok := m["by"]
	if !ok {
		return ChangeLogEntry{}, fmt.Errorf("missing required field %q", "by")
	}
	switch t := by.(type) {
	case string:
		e.By = t
		e.ByKind = KindUser
	case map[string]any:
		name, _ := asString(t["name"])
		kind, _ := asString(t["kind"])
		if kind == "" {
			kind = KindUser
		}
		if !IsAuthorKind(kind) || name == "" {
			return ChangeLogEntry{}, fmt.Errorf("field %q object requires kind (%s) and a non-empty name", "by", strings.Join(AuthorKinds, " | "))
		}
		e.By = name
		e.ByKind = kind
	default:
		return ChangeLogEntry{}, fmt.Errorf("field %q must be a string or an object {\"kind\", \"name\"}", "by")
	}
	if !validDate(e.Date) {
		return ChangeLogEntry{}, fmt.Errorf("`date` %q is not a valid YYYY-MM-DD date", e.Date)
	}
	return e, nil
}

// validDate reports whether s is a real calendar date in YYYY-MM-DD form.
func validDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// --- Note contract (ADR-019 D7) ---

// Note roles are the fixed `role` vocabulary of the cmt- note content
// schema (ADR-019 D7). The role is a field of the structured JSON
// content — NOT `classification.domain` (that field is the Engineering
// Domain, ADR-008).
const (
	NoteRoleImplementation = "implementation"
	NoteRoleReview         = "review"
	NoteRoleFix            = "fix"
	// NoteRoleReply is the reply role (ADR-019 D8 revised): a reply
	// comment attached to ONE parent note through the replies-to
	// relationship — {role, body}. Replies are not evidence and never
	// satisfy the transition gates on their own.
	NoteRoleReply = "reply"
)

// noteRoleContentKeys maps each note role to its allowed content keys
// (the strict per-role schema of ADR-019 D7: unknown extra keys are
// rejected, consistent with ADR-016 strict JSON).
var noteRoleContentKeys = map[string]map[string]bool{
	NoteRoleImplementation: {"role": true, "summary": true, "changes": true, "tests": true},
	NoteRoleReview:         {"role": true, "verdict": true, "notes": true},
	NoteRoleFix:            {"role": true, "addresses": true, "detail": true},
	NoteRoleReply:          {"role": true, "body": true},
}

// validateNoteContent validates the structured content of a cmt-
// artifact against the ADR-019 D7 schemas: `role` must be one of the
// three roles, the per-role required fields must be present and
// well-formed (summary/detail non-empty strings, verdict one of the
// review verdicts, list fields arrays of strings), unknown roles and
// unknown extra keys are errors. Notes are JSON-native only: a cmt-
// artifact without the structured content object (Markdown variant) is
// refused.
//
// It is invoked by the graph pass (R13) for every cmt- artifact of the
// analyzed set, so the contract holds at repository level (`eka
// validate`/sync) and at CKO level (note publish).
func validateNoteContent(a *Artifact) []Result {
	var results []Result
	add := func(format string, args ...any) {
		results = append(results, Result{
			File: a.RelPath, Rule: Rule13, Severity: SeverityError,
			Message: fmt.Sprintf(format, args...),
		})
	}
	if a.ContentFields == nil {
		add("cmt- notes are JSON-native only (ADR-019 D7): the note requires the structured content object with a `role` field; the Markdown variant is not supported")
		return results
	}
	role, ok := a.ContentFields["role"].(string)
	if !ok || role == "" {
		add("note content requires a non-empty string `role` field (allowed: %s)", strings.Join(noteRolesList(), ", "))
		return results
	}
	allowed, known := noteRoleContentKeys[role]
	if !known {
		add("unknown note role %q (allowed: %s)", role, strings.Join(noteRolesList(), ", "))
		return results
	}
	for k := range a.ContentFields {
		if !allowed[k] {
			add("note content key %q is not allowed for role %q (allowed: %s)",
				k, role, strings.Join(sortedKeys(allowed), ", "))
		}
	}
	switch role {
	case NoteRoleImplementation:
		if s, ok := a.ContentFields["summary"].(string); !ok || strings.TrimSpace(s) == "" {
			add("implementation note requires a non-empty string `summary`")
		}
		for _, listKey := range []string{"changes", "tests"} {
			if _, ok := a.ContentFields[listKey]; !ok {
				add("implementation note requires the `%s` list", listKey)
				continue
			}
			if _, ok := asStringListAny(a.ContentFields[listKey]); !ok {
				add("implementation note `%s` must be a list of strings", listKey)
			}
		}
	case NoteRoleReview:
		if verdict, ok := a.ContentFields["verdict"].(string); !ok || (verdict != "approve" && verdict != "changes-requested") {
			add("review note requires `verdict` to be one of: approve, changes-requested")
		}
		if _, ok := a.ContentFields["notes"]; !ok {
			add("review note requires the `notes` list")
		} else if _, ok := asStringListAny(a.ContentFields["notes"]); !ok {
			add("review note `notes` must be a list of strings")
		}
	case NoteRoleFix:
		if s, ok := a.ContentFields["detail"].(string); !ok || strings.TrimSpace(s) == "" {
			add("fix note requires a non-empty string `detail`")
		}
		if _, ok := a.ContentFields["addresses"]; !ok {
			add("fix note requires the `addresses` list")
		} else if _, ok := asStringListAny(a.ContentFields["addresses"]); !ok {
			add("fix note `addresses` must be a list of strings (note identities)")
		}
	case NoteRoleReply:
		if s, ok := a.ContentFields["body"].(string); !ok || strings.TrimSpace(s) == "" {
			add("reply note requires a non-empty string `body`")
		}
	}
	return results
}

// noteRolesList renders the role tokens in canonical order
// (deterministic diagnostics).
func noteRolesList() []string {
	return []string{NoteRoleImplementation, NoteRoleReview, NoteRoleFix, NoteRoleReply}
}

// sortedKeys renders a key set sorted lexicographically (deterministic
// diagnostics).
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// asStringListAny coerces an any value to a string list (the decoded
// JSON/YAML forms of `["..."]`).
func asStringListAny(v any) ([]string, bool) {
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(l))
	for _, item := range l {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// --- YAML value coercion helpers. ---

// asDateString coerces a frontmatter date value: plain strings pass through,
// and yaml.v3 timestamp nodes (unquoted YYYY-MM-DD values, used by the
// canonical ADRs) are normalized to YYYY-MM-DD.
func asDateString(v any) (string, bool) {
	if s, ok := v.(string); ok {
		return s, true
	}
	if t, ok := v.(time.Time); ok {
		return t.Format("2006-01-02"), true
	}
	return "", false
}

// asString coerces a scalar to string.
func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func asList(v any) ([]any, bool) {
	l, ok := v.([]any)
	return l, ok
}

func asStringList(v any) ([]string, bool) {
	l, ok := asList(v)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(l))
	for _, item := range l {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// asInt coerces the integer forms produced by yaml.v3 and by the JSON
// authoring adapter (encoding/json decodes numbers as float64).
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		// JSON numbers: whole values only (revision/instance-version are
		// integers; 1.5 is not an instance version).
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case string:
		// Interpretation (documented): quoted integers such as
		// `instance-version: "1"` are rejected; the spec defines the
		// field as an integer and the canonical ADRs write it unquoted.
		return 0, false
	default:
		return 0, false
	}
}
