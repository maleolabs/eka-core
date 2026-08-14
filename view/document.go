package view

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the document projection: the detail view of ONE
// canonical document of any type (discovery, architecture, planning,
// operations, work items). It is the resolution behind the bare-argument
// fallback of `eka view <arg>` (domain first, then the specific
// document).
//
// Target grammar (the CLI's reference grammar):
//
//	<ns>/<type>:<id>[:<v>]   qualified — the namespace must match
//	<type>:<id>[:<v>]        type + id across every namespace
//	<type>-<id>              the file form (dash split on a known type)
//	<id>                     a bare id across every type
//
// Unqualified matches resolve to the lexicographically smallest
// canonical identity LINE when several namespaces/instances hold the
// same id, then to the line's highest (latest) instance (ADR-025; the
// ticket-projection precedent).

// ContentSection is one rendered content block of a document: the
// camelCase content key and its value rendered as text.
type ContentSection struct {
	Key   string
	Value string
}

// Document is the view over one canonical document.
type Document struct {
	// Identity is the canonical line form (<ns>/<type>:<id>) of the
	// resolved (latest) instance.
	Identity string
	// Number is the line's issue number (0 = none; filled by the
	// command layer from the store — the projection engine stays
	// store-shaped).
	Number int
	Type   string
	ID     string
	// InstanceVersion is the resolved instance version (the line's
	// highest when the target carried none).
	InstanceVersion int
	// State is the primary state: the execution state for work items,
	// else the content state ("" when the type owns neither).
	State    string
	HasState bool
	// States lists every owned state domain with its value, in
	// deterministic domain order ("content-state: accepted").
	States []string
	// Relationships lists the unit's relationship targets in
	// deterministic order ("depends-on <target>").
	Relationships []string
	// Dimension/Domain/Phase are the classification rows ("" when
	// absent).
	Dimension string
	Domain    string
	Phase     string
	// Content are the content sections: the type's required sections
	// in registry order first, then any extra keys sorted.
	Content []ContentSection
	// IsWorkItem reports that the document owns the Execution State
	// domain (rendered with the work-item card style).
	IsWorkItem bool
}

// DocumentByTarget resolves one canonical document from a user-supplied
// target string (the grammar above). It returns nil when nothing
// matches. Ambiguous unqualified matches resolve to the
// lexicographically smallest canonical identity.
func (g *Graph) DocumentByTarget(target string) *exchange.Unit {
	ns, typeToken, id := parseDocumentTarget(target)
	if id == "" {
		return nil
	}
	if typeToken != "" {
		if ns != "" {
			// Qualified: the exact line (highest instance).
			return g.byForm[LineForm(ns, typeToken, id)]
		}
		// Type + id across every namespace.
		return smallest(g.byType[typeToken], ns, id)
	}
	if ns != "" {
		// A bare id within one namespace: the smallest canonical line
		// form wins, the line's highest instance (ADR-025).
		return smallest(g.units, ns, id)
	}
	// A bare id across every type.
	return smallest(g.units, "", id)
}

// smallest returns the unit with the given id (optionally namespace-
// filtered) under the unqualified-resolution rule: across identity
// LINES the lexicographically smallest canonical line form wins;
// within one line the HIGHEST instance wins (ADR-025 — the line form
// resolves to the latest knowledge version). It returns nil when no
// unit matches.
func smallest(units []*exchange.Unit, ns, id string) *exchange.Unit {
	var best *exchange.Unit
	for _, u := range units {
		if u.Identity.ID != id {
			continue
		}
		if ns != "" && u.Identity.Namespace != ns {
			continue
		}
		if best == nil {
			best = u
			continue
		}
		bestLine := LineForm(best.Identity.Namespace, best.Identity.Type, best.Identity.ID)
		uLine := LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
		switch {
		case bestLine != uLine:
			// Different lines: the lexicographically smallest canonical
			// identity wins.
			if uLine < bestLine {
				best = u
			}
		case u.Identity.InstanceVersion > best.Identity.InstanceVersion:
			// Same line: the highest (latest) instance wins.
			best = u
		}
	}
	return best
}

// parseDocumentTarget splits a document target into (namespace,
// typeToken, id); any part may be empty. The grammar: a "<ns>/" prefix,
// then "<type>:<id>" (a version suffix ":<v>" is dropped — the line
// form resolves), "<type>-<id>" (dash split on a KNOWN type token), or
// a bare id.
func parseDocumentTarget(target string) (ns, typeToken, id string) {
	rest := target
	if i := strings.Index(rest, "/"); i >= 0 {
		ns = rest[:i]
		rest = rest[i+1:]
	}
	if i := strings.Index(rest, ":"); i >= 0 {
		typeToken = rest[:i]
		id = rest[i+1:]
		if j := strings.Index(id, ":"); j >= 0 {
			id = id[:j] // version suffix dropped
		}
		return ns, typeToken, id
	}
	if i := strings.Index(rest, "-"); i >= 0 && conformance.IsKnownType(rest[:i]) {
		return ns, rest[:i], rest[i+1:]
	}
	return ns, "", rest
}

// buildDocument resolves the target and projects the document.
func buildDocument(g *Graph, target string) (Projection, error) {
	if target == "" {
		return nil, fmt.Errorf("the document projection requires a target: eka view document <target>")
	}
	u := g.DocumentByTarget(target)
	if u == nil {
		// Deterministic diagnostic: a repository with no synced
		// knowledge gets the sync hint; otherwise the resolution
		// grammar hint.
		hint := "resolve with <ns>/<type>:<id>, <type>:<id>, or a bare id"
		if len(g.units) == 0 {
			hint = "the repository contains no synced documents; run 'eka sync' to seed its knowledge"
		}
		return nil, &TargetNotFoundError{Projection: "document", Target: target, Hint: hint}
	}
	d := &Document{
		Identity:        LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID),
		Type:            u.Identity.Type,
		ID:              u.Identity.ID,
		InstanceVersion: u.Identity.InstanceVersion,
		IsWorkItem:      isWorkItemType(u.Identity.Type),
		Dimension:       u.Classification.Dimension,
		Domain:          u.Classification.Domain,
		Phase:           u.Phase,
	}
	d.States = stateRows(u)
	if d.IsWorkItem {
		d.State, d.HasState = u.StateVector.ExecutionState, u.StateVector.ExecutionState != ""
	} else {
		d.State, d.HasState = u.StateVector.ContentState, u.StateVector.ContentState != ""
	}
	for _, rel := range u.Relationships {
		d.Relationships = append(d.Relationships, rel.Type+" "+rel.Target)
	}
	d.Content = documentContent(u)
	return &DocumentProjection{Document: d}, nil
}

// DocumentProjection is the document projection model.
type DocumentProjection struct {
	Document *Document
}

// Name reports the canonical projection name.
func (p *DocumentProjection) Name() string { return "document" }

// stateRows renders every owned state domain with its value, in the
// deterministic domain order of the state vector.
func stateRows(u *exchange.Unit) []string {
	type row struct{ domain, value string }
	rows := []row{
		{conformance.DomainContentState, u.StateVector.ContentState},
		{conformance.DomainExecutionState, u.StateVector.ExecutionState},
		{conformance.DomainPlanningState, u.StateVector.PlanningState},
		{conformance.DomainContainerState, u.StateVector.ContainerState},
		{conformance.DomainExistenceState, u.StateVector.ExistenceState},
	}
	var out []string
	for _, r := range rows {
		if r.value != "" {
			out = append(out, r.domain+": "+r.value)
		}
	}
	return out
}

// documentContent extracts the content sections of a unit: for
// structured-json payloads the required sections in registry order
// first, then the extra keys sorted; for structured-text (or
// unparseable payloads) the raw text as one section. Values are
// rendered as text (objects/arrays as compact JSON).
func documentContent(u *exchange.Unit) []ContentSection {
	if u.Content.Representation == exchange.StructuredJSON && len(u.ContentPayload) > 0 {
		var fields map[string]any
		if json.Unmarshal(u.ContentPayload, &fields) == nil && len(fields) > 0 {
			var out []ContentSection
			seen := map[string]bool{}
			for _, name := range conformance.RequiredSectionsFor(u.Identity.Type) {
				key := conformance.SectionKey(name)
				if v, ok := fields[key]; ok {
					out = append(out, ContentSection{Key: key, Value: renderContentValue(v)})
					seen[key] = true
				}
			}
			var extras []string
			for k := range fields {
				if !seen[k] {
					extras = append(extras, k)
				}
			}
			sort.Strings(extras)
			for _, k := range extras {
				out = append(out, ContentSection{Key: k, Value: renderContentValue(fields[k])})
			}
			return out
		}
	}
	if len(u.ContentPayload) > 0 {
		return []ContentSection{{Key: "content", Value: strings.TrimSpace(string(u.ContentPayload))}}
	}
	return nil
}

// renderContentValue renders one structured content value as text:
// strings as-is, other scalars via fmt, objects/arrays as compact JSON.
func renderContentValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		if data, err := json.Marshal(t); err == nil {
			return string(data)
		}
		return fmt.Sprint(v)
	}
}
