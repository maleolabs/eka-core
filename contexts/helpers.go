package contexts

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the internal helpers of the Context Engine:
// the deterministic mapping helpers from exchange units to the Entry
// references of the Context Object, the strata grouping, and the
// shared serializers. All helpers are pure functions of their inputs —
// no engine state, no ambient data.

// primaryState returns the primary state value of a unit with the
// deterministic priority of the context projection: execution-state
// when non-empty, else container-state, else note-state, else
// planning-state, else content-state ("" when none — an empty state
// vector). The priority is fixed: it is the single state a consumer
// reads first when the unit carries several owned state domains.
func primaryState(u *exchange.Unit) string {
	switch {
	case u.StateVector.ExecutionState != "":
		return u.StateVector.ExecutionState
	case u.StateVector.ContainerState != "":
		return u.StateVector.ContainerState
	case u.StateVector.NoteState != "":
		return u.StateVector.NoteState
	case u.StateVector.PlanningState != "":
		return u.StateVector.PlanningState
	default:
		return u.StateVector.ContentState
	}
}

// entryFor maps one exchange unit to its compact Entry reference with
// the given role and issue number. The domain derives from the unit's
// classification, else from the artifact type token (the single shared
// source of truth); the stratum is the authority stratum of that
// domain. An unknown type token (impossible for store units — the
// conformance gate rejects them at sync) yields an empty domain.
func entryFor(u *exchange.Unit, role string, number int) Entry {
	domain, err := domainOf(u)
	if err != nil {
		// Unreachable for store units (conformance-validated): keep
		// the entry buildable with an empty domain instead of failing
		// the whole context for a unit that cannot exist.
		domain = ""
	}
	return Entry{
		CanonicalForm: u.CanonicalIdentityForm,
		LineForm:      lineFormOf(u),
		Number:        number,
		Type:          u.Identity.Type,
		ID:            u.Identity.ID,
		Domain:        domain,
		Stratum:       conformance.Stratum(conformance.Domain(domain)),
		State:         primaryState(u),
		Role:          role,
		ObjectHash:    u.Digest,
	}
}

// domainOf derives the engineering domain of a unit:
// Classification.Domain when non-empty, else conformance.DomainForToken
// on the artifact type token (the single shared source of truth — the
// same derivation the machine document uses). An unknown token is a
// deterministic error.
func domainOf(u *exchange.Unit) (string, error) {
	if d := u.Classification.Domain; d != "" {
		return d, nil
	}
	d, ok := conformance.DomainForToken(u.Identity.Type)
	if !ok {
		return "", errUnknownType(u.Identity.Type)
	}
	return string(d), nil
}

// errUnknownType is the shared deterministic error of an artifact type
// token without a home Engineering Domain.
func errUnknownType(typeToken string) error {
	return &unknownTypeError{typeToken: typeToken}
}

// unknownTypeError is the concrete error value of an unknown artifact
// type token (concrete and documented — no interface type).
type unknownTypeError struct{ typeToken string }

func (e *unknownTypeError) Error() string {
	return "contexts: unknown artifact type " + strconv.Quote(e.typeToken)
}

// lineFormOf renders the qualified line form of a unit:
// "<ns>/<type>:<id>" (the line identity, lowest instance address).
func lineFormOf(u *exchange.Unit) string {
	return u.Identity.Namespace + "/" + u.Identity.Type + ":" + u.Identity.ID
}

// lineFormOfForm strips the instance-version suffix of a canonical
// identity form ("<ns>/<type>:<id>:<v>"), yielding the qualified line
// form "<ns>/<type>:<id>". A form without a trailing digit suffix
// passes through unchanged.
func lineFormOfForm(form string) string {
	lastColon := -1
	for i := 0; i < len(form); i++ {
		if form[i] == ':' {
			lastColon = i
		}
	}
	if lastColon < 0 {
		return form
	}
	suffix := form[lastColon+1:]
	if suffix == "" {
		return form
	}
	if _, err := strconv.Atoi(suffix); err != nil {
		return form
	}
	return form[:lastColon]
}

// marshal is the shared deterministic serializer of the context
// interface: compact = json.Marshal (one line), pretty = the
// two-space-indented form; both end in a single trailing newline.
// The wire content is identical (same field order, same values) —
// only the whitespace differs, so both forms parse to the same
// object (the machine-style serializer, re-implemented locally: the
// engine never imports the machine package).
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

// groupStrata groups the collected units into the strata landscape of
// the Context Object: only non-empty strata, stratum ascending (1..5,
// 1 = highest authority), units sorted by canonical form within a
// stratum, each unit in exactly one stratum (its own domain's). The
// numbers map carries the line-form -> issue-number lookup ("" key
// values stay 0); the roles map carries the line-form -> role lookup
// (the first-wins collected role).
func groupStrata(units []*exchange.Unit, numbers map[string]int, roles map[string]string) []Stratum {
	sorted := append([]*exchange.Unit(nil), units...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CanonicalIdentityForm < sorted[j].CanonicalIdentityForm
	})
	byStratum := make(map[int][]*exchange.Unit)
	for _, u := range sorted {
		domain, err := domainOf(u)
		if err != nil {
			// Unreachable for store units (conformance-validated):
			// defensive skip keeps the grouping deterministic.
			continue
		}
		s := conformance.Stratum(conformance.Domain(domain))
		byStratum[s] = append(byStratum[s], u)
	}
	strata := make([]Stratum, 0, len(byStratum))
	for s := 1; s <= 5; s++ {
		units, ok := byStratum[s]
		if !ok {
			continue
		}
		domain, err := domainOf(units[0])
		if err != nil {
			continue // Unreachable for store units, see above.
		}
		stratum := Stratum{Stratum: s, Domain: domain}
		for _, u := range units {
			stratum.Units = append(stratum.Units, entryFor(u, roles[lineFormOf(u)], numbers[lineFormOf(u)]))
		}
		strata = append(strata, stratum)
	}
	return strata
}
