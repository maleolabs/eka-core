package conformance

// This file encodes the EKA v1.0 type/state taxonomy used by the rules:
// the 27 artifact type tokens (26 + cmt, ADR-019 D3), the 12 knowledge
// dimensions, the six owned state domains (+ phase context attribute), the
// value sets per domain, and the forward-only transition tables (Execution
// State uses the explicit D1 table, ADR-019 §3).
//
// Grounding:
//   - skeleton/docs/exchange/validation.md (Rules 3 and 4 tables)
//   - skeleton/docs/operating/protocol.md §2 (value sets, transition rules)
//   - reference/reference-architecture.md §2.1 (26-token table) plus ADR-019 D3 (cmt)
//   - standard/eka-specification-v1.0.md §7 (State Taxonomy) and §8 (Knowledge Taxonomy)
//   - reference/decisions/adr-019-work-item-transitions-notes-gates.md (D1-D7)

// State domain names, exactly as they appear in frontmatter.
const (
	DomainContentState   = "content-state"
	DomainExecutionState = "execution-state"
	DomainPlanningState  = "planning-state"
	DomainContainerState = "container-state"
	DomainExistenceState = "existence-state"
	// DomainNoteState is the note-state domain owned by cmt- (comment/
	// note) artifacts (ADR-019 D4): {open, resolved, dismissed},
	// forward-only. It is the deterministic basis of the transition
	// gates (R13, ADR-019 D6).
	DomainNoteState = "note-state"
	DomainPhase     = "phase"
)

// stateFields lists the six owned state domains (phase is a context
// attribute, not a state domain).
var stateFields = []string{
	DomainContentState,
	DomainExecutionState,
	DomainPlanningState,
	DomainContainerState,
	DomainExistenceState,
	DomainNoteState,
}

// relationshipFields lists the seven canonical relationship fields
// validated by Rule 5 (discusses added by ADR-019 D5: the note ->
// subject edge of cmt- artifacts; replies-to added by ADR-019 D8
// revised: the reply -> parent edge of cmt- artifacts, single-parent).
var relationshipFields = []string{
	"amends",
	"supersedes",
	"derives-from",
	"depends-on",
	"validates",
	"discusses",
	"replies-to",
}

// versionedTypes are the artifact types that MUST carry a -v<nn> filename
// suffix (Rule 2).
var versionedTypes = map[string]bool{"scp": true, "plan": true}

// projectionTypes may never carry a dimension (Rule 6); ticket is also the
// empty-state-vector type (Rule 4).
var projectionTypes = map[string]bool{"ctr": true, "tkt": true, "ses": true}

// workItemTypes are the operating-layer work items whose dimension is
// informational (Rule 6) and which own the Execution State domain.
var workItemTypes = map[string]bool{"sto": true, "ts": true, "bug": true, "td": true, "ch": true, "spk": true}

// TypeInfo describes one of the 27 artifact types (26 + cmt, ADR-019 D3).
type TypeInfo struct {
	// Token is the frontmatter `type` value, without the trailing dash
	// used in filenames (e.g. "adr").
	Token string
	// Owned lists the state domains this type owns (Rule 4). Absence of a
	// domain here means the field must not appear on the artifact.
	Owned []string
	// IsKnowledge reports whether the type is a knowledge artifact whose
	// `dimension` must equal its home folder (Rule 6). Work items are
	// exempt (dimension is informational).
	IsKnowledge bool
}

// typeTokens is the canonical 27-token table (reference-architecture.md §2.1 — 26 tokens, plus cmt per ADR-019 D3 —
// validation.md Rule 4). The owned sets follow validation.md Rule 4 exactly.
var typeTokens = map[string]TypeInfo{
	"vis":  {Token: "vis", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"str":  {Token: "str", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"req":  {Token: "req", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"scp":  {Token: "scp", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"epc":  {Token: "epc", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"plan": {Token: "plan", Owned: []string{DomainContentState, DomainPlanningState, DomainExistenceState}, IsKnowledge: true},
	"ctr":  {Token: "ctr", Owned: []string{DomainContainerState, DomainExistenceState}, IsKnowledge: false},
	"tkt":  {Token: "tkt", Owned: []string{}, IsKnowledge: false},
	"sto":  {Token: "sto", Owned: []string{DomainExecutionState, DomainExistenceState}, IsKnowledge: false},
	"ts":   {Token: "ts", Owned: []string{DomainExecutionState, DomainExistenceState}, IsKnowledge: false},
	"bug":  {Token: "bug", Owned: []string{DomainExecutionState, DomainExistenceState}, IsKnowledge: false},
	"td":   {Token: "td", Owned: []string{DomainExecutionState, DomainExistenceState}, IsKnowledge: false},
	"ch":   {Token: "ch", Owned: []string{DomainExecutionState, DomainExistenceState}, IsKnowledge: false},
	"spk":  {Token: "spk", Owned: []string{DomainExecutionState, DomainExistenceState}, IsKnowledge: false},
	"ses":  {Token: "ses", Owned: []string{DomainExistenceState}, IsKnowledge: false},
	"rvw":  {Token: "rvw", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"cmt":  {Token: "cmt", Owned: []string{DomainContentState, DomainExistenceState, DomainNoteState}, IsKnowledge: false},
	"adr":  {Token: "adr", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"dec":  {Token: "dec", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"arc":  {Token: "arc", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"spec": {Token: "spec", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"std":  {Token: "std", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"run":  {Token: "run", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"rel":  {Token: "rel", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"gls":  {Token: "gls", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"trc":  {Token: "trc", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
	"fnd":  {Token: "fnd", Owned: []string{DomainContentState, DomainExistenceState}, IsKnowledge: true},
}

// dimensionTokens is the 12 knowledge dimension vocabulary (EKA §8, ADR-005).
var dimensionTokens = map[string]bool{
	"intent":         true,
	"requirements":   true,
	"architecture":   true,
	"decisions":      true,
	"specifications": true,
	"standards":      true,
	"operations":     true,
	"quality":        true,
	"planning":       true,
	"records":        true,
	"research":       true,
	"vocabulary":     true,
}

// Value sets per domain (validation.md Rule 3, protocol.md §2).

var executionStateValues = []string{"planned", "todo", "in-progress", "in-review", "done", "canceled"}
var planningStateValues = []string{"draft", "approved", "immutable"}
var containerStateValues = []string{"planned", "active", "completed"}
var existenceStateValues = []string{"active", "archived", "retired"}
var noteStateValues = []string{"open", "resolved", "dismissed"}
var phaseValues = []string{"discovery", "mvp", "milestone", "release", "growth", "maturity", "sunset"}

// contentStateVariants is the content-state value set per artifact family
// (validation.md Rule 3, EKA §7.2). "living" is the standard variant.
var contentStateVariants = map[string][]string{
	"living":   {"draft", "review", "approved", "amended"},
	"adr":      {"proposed", "accepted", "superseded"},
	"decision": {"draft", "accepted", "superseded"},
}

// contentStateVariant returns the content-state value set for an artifact
// type: adr uses the ADR variant, dec the decision variant, every other type
// the living variant.
func contentStateVariant(typeToken string) []string {
	switch typeToken {
	case "adr":
		return contentStateVariants["adr"]
	case "dec":
		return contentStateVariants["decision"]
	default:
		return contentStateVariants["living"]
	}
}

// domainValues returns the ordered value set for a state domain, selecting
// the content-state variant for the given artifact type.
func domainValues(domain, typeToken string) []string {
	switch domain {
	case DomainContentState:
		return contentStateVariant(typeToken)
	case DomainExecutionState:
		return executionStateValues
	case DomainPlanningState:
		return planningStateValues
	case DomainContainerState:
		return containerStateValues
	case DomainExistenceState:
		return existenceStateValues
	case DomainNoteState:
		return noteStateValues
	case DomainPhase:
		return phaseValues
	default:
		return nil
	}
}

// isLegalTransition reports whether the from -> to transition is legal for
// the domain on the given artifact type.
//
// Interpretation (documented): only Execution State is "strictly sequential"
// in the forward direction, but its transition rule is NOT plain array
// adjacency anymore — the D1 table (ADR-019 §3) is explicit:
//
//	forward adjacent: planned -> todo -> in-progress -> in-review -> done
//	pull-back, one step: in-review -> in-progress, in-progress -> todo
//	cancel: -> canceled from any state (incl. done)
//	re-activation: canceled -> todo
//	done: only -> canceled; terminal otherwise
//	anything else is illegal (skip, multi-step revert, todo -> planned,
//	canceled -> anything but todo)
//
// All other state domains are forward-only: the position index must
// strictly increase (never revert), but skipping is tolerated because the
// spec's forward-only rule does not demand adjacency for them. `from: "-"`
// is the initial-state marker established by the repository's own ADRs and
// is always a legal start. Phase is a context attribute, not a state
// domain: any transition between valid phase values is legal (no ordering
// constraint, EKA 11.2).
func isLegalTransition(domain, typeToken, from, to string) bool {
	if from == "-" || domain == DomainPhase {
		return true
	}
	values := domainValues(domain, typeToken)
	if indexOf(values, from) < 0 || indexOf(values, to) < 0 {
		return false
	}
	if domain == DomainExecutionState {
		return isLegalExecutionTransition(from, to)
	}
	if domain == DomainNoteState {
		// note-state (ADR-019 D4): open -> resolved | dismissed; both
		// destinations are terminal — no transition out of them.
		return from == "open" && (to == "resolved" || to == "dismissed")
	}
	return indexOf(values, to) > indexOf(values, from)
}

// isLegalExecutionTransition evaluates the D1 transition table (ADR-019
// §3) for the Execution State domain. Explicit branches — the canceled
// and pull-back rules break array adjacency, so the table must never be
// expressed as index arithmetic alone.
func isLegalExecutionTransition(from, to string) bool {
	switch from {
	case "planned":
		return to == "todo" || to == "canceled"
	case "todo":
		return to == "in-progress" || to == "canceled"
	case "in-progress":
		return to == "in-review" || to == "todo" || to == "canceled"
	case "in-review":
		return to == "done" || to == "in-progress" || to == "canceled"
	case "done":
		return to == "canceled"
	case "canceled":
		return to == "todo"
	}
	return false
}

// IsLegalExecutionTransition is the exported form of the D1 table
// evaluation, consumed by the Authoring API (`eka transition`, ADR-019
// D2): the command validates the requested transition against the same
// table the rule engine uses — one source of truth.
func IsLegalExecutionTransition(from, to string) bool {
	return isLegalExecutionTransition(from, to)
}

// indexOf returns the position of v in values, or -1.
func indexOf(values []string, v string) int {
	for i, x := range values {
		if x == v {
			return i
		}
	}
	return -1
}

// contains reports whether values contains v.
func contains(values []string, v string) bool {
	return indexOf(values, v) >= 0
}

// --- Exported accessors (additive; consumed by the exchange/ import
// engine, which must validate package units against the same taxonomy the
// validator uses — one source of truth instead of a duplicated table). ---

// TypeInfoFor returns the TypeInfo of a type token, or nil when the token
// is unknown.
func TypeInfoFor(token string) *TypeInfo {
	info, ok := typeTokens[token]
	if !ok {
		return nil
	}
	return &info
}

// OwnedDomains returns the owned state domains of an artifact type (Rule
// 4), or nil for unknown types.
func OwnedDomains(token string) []string {
	info, ok := typeTokens[token]
	if !ok {
		return nil
	}
	return info.Owned
}

// IsVersionedType reports whether the type token must carry a -v<nn>
// filename suffix (Rule 2): scp- and plan- only.
func IsVersionedType(token string) bool {
	return versionedTypes[token]
}

// ValidStateValue reports whether value is a legal value of the state
// domain on the given artifact type (content-state variant selected by the
// type; Rule 3).
func ValidStateValue(domain, typeToken, value string) bool {
	return contains(domainValues(domain, typeToken), value)
}

// DomainValues returns the ordered value set of a state domain on the
// given artifact type (content-state variant selected by the type; Rule
// 3), or nil for unknown domains. Phase is included (type-independent).
// This is the single source of truth for the allowed-value diagnostics
// rendered by the exchange/ import engine: value validation and value
// listing always derive from the same table, so a diagnostic can never
// drift from what ValidStateValue/ValidPhaseValue accept.
func DomainValues(domain, typeToken string) []string {
	return domainValues(domain, typeToken)
}

// ValidPhaseValue reports whether value is a legal phase context value
// (Rule 3).
func ValidPhaseValue(value string) bool {
	return contains(phaseValues, value)
}

// IsDimensionToken reports whether s is one of the 12 knowledge dimension
// tokens (Rule 6).
func IsDimensionToken(s string) bool {
	return dimensionTokens[s]
}

// RelationshipFieldNames returns the six canonical relationship field
// names in declared order (Rule 5; discusses added by ADR-019 D5).
func RelationshipFieldNames() []string {
	return relationshipFields
}

// WorkItemTypes reports whether a type token is one of the six operating
// work items that own the Execution State domain and are gated by R13
// (sto, ts, bug, td, ch, spk; ADR-019 D6).
func WorkItemTypes() map[string]bool {
	out := make(map[string]bool, len(workItemTypes))
	for t := range workItemTypes {
		out[t] = true
	}
	return out
}

// IsWorkItemType reports whether a type token is one of the six work
// item types (ADR-019 D6).
func IsWorkItemType(token string) bool {
	return workItemTypes[token]
}

// NumberGroup returns the issue-number group of a type token (RFC:
// per-group incremental numbers, GitHub-style — work items, tickets
// and notes each count independently). "" = the type carries no
// number. Work items (sto/ts/bug/td/ch/spk) share one group; tkt and
// cmt own their groups.
func NumberGroup(typeToken string) string {
	switch typeToken {
	case "tkt":
		return "ticket"
	case "cmt":
		return "note"
	default:
		if workItemTypes[typeToken] {
			return "work-item"
		}
		return ""
	}
}
