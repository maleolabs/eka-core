package contexts

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/pathglob"
)

// This file implements the read side of the EKA Standard 1.5
// applicability declaration (specification/exchange.md, "Content:
// applicability declaration (optional)"): the deterministic
// path-to-constraint retrieval of `eka context --for <path>`. Given a
// repository-relative path it returns exactly the units of that
// repository whose declared `appliesTo` scope matches the path — the
// higher-authority constraints that bind it. One hop, no graph
// expansion, no similarity search: a path that matches nothing yields an
// empty set, never an error.

// UnitsForPath returns the units of one repository (its provenance pair)
// whose declared `appliesTo` scope matches path, ordered by authority
// stratum ascending, then canonical identity form. The set is not
// restricted by artifact type — the constraint families (std-, spec-,
// adr-, req-) are a retrieval convention, not a closed schema.
//
// Retrieval is REPOSITORY-scoped, not project-scoped: `appliesTo`
// patterns are relative to the repository root (spec), so a path is
// matched only against the declarations of the repository it belongs to.
// A project may group several repositories (workspace registry), and a
// project-wide scan would let a sibling repository's `src/**` spuriously
// bind this repository's `src/...`; the CLI resolves the repository of
// the current working directory and passes its name as sourceRepo.
//
// A unit with a malformed declaration (a non-string element, or a
// pattern that cannot be interpreted) is an error — the declaration
// cannot be read, so it is reported, never silently ignored. Every
// Entry carries Role "appliesTo" (the field that pulled it in) and no
// issue number (numbers are a display nicety supplied by `eka get`).
func (e *Engine) UnitsForPath(projectID, sourceRepo, path string) ([]Entry, error) {
	units, err := e.rt.Knowledge.Units(projectID, sourceRepo)
	if err != nil {
		return nil, fmt.Errorf("contexts: %w", err)
	}
	out := []Entry{}
	for _, u := range units {
		patterns, declared, err := declaredScope(u)
		if err != nil {
			return nil, fmt.Errorf("contexts: %s: `appliesTo` %v", u.CanonicalIdentityForm, err)
		}
		if !declared {
			continue
		}
		for _, p := range patterns {
			if p.Match(path) {
				out = append(out, entryFor(u, "appliesTo", 0))
				break // One matching pattern is enough; the unit appears once.
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stratum != out[j].Stratum {
			return out[i].Stratum < out[j].Stratum
		}
		return out[i].CanonicalForm < out[j].CanonicalForm
	})
	return out, nil
}

// declaredScope reads one unit's optional `appliesTo` declaration from its
// content payload (the canonical structured content object). It reports
// declared=false when the unit carries no structured content object — a
// payload that is not a JSON object binds no path — or no `appliesTo` key.
func declaredScope(u *exchange.Unit) (patterns []pathglob.Pattern, declared bool, err error) {
	var content map[string]any
	if json.Unmarshal(u.ContentPayload, &content) != nil || content == nil {
		return nil, false, nil
	}
	raw, ok := content["appliesTo"]
	if !ok {
		return nil, false, nil
	}
	patterns, err = pathglob.Interpret(raw)
	if err != nil {
		return nil, false, err
	}
	return patterns, true, nil
}
