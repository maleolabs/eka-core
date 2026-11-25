package contexts

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/pathglob"
)

// This file implements the read side of the EKA Standard 1.7 declared-checks
// declaration (specification/exchange.md, "Content: declared checks
// (optional)"): the deterministic retrieval of the checks a repository's
// artifacts declare about themselves. Given a repository (its provenance pair)
// it returns every declared check, each carrying the canonical identity form of
// the artifact that declared it — so a failing check resolves to the standard it
// verifies. One scan, no graph expansion: an artifact that declares no checks
// contributes nothing, never an error.

// DeclaredCheck is one check declared by one artifact: the declaring artifact's
// canonical identity form, the shell command run at the repository root, and
// the repository-relative scope patterns the check is relevant to. An empty
// Scope matches no path (the check runs only without a changed-set filter).
type DeclaredCheck struct {
	CanonicalForm string
	Command       string
	Scope         []pathglob.Pattern
}

// DeclaredChecks returns every check declared by the units of ONE repository
// (its provenance pair), ordered by canonical identity form, then command, then
// the first scope pattern string.
//
// Retrieval is REPOSITORY-scoped, exactly like UnitsForPath: `scope` patterns
// are relative to the repository root, so a check is only relevant to the
// repository it belongs to. A malformed declaration is an error (consistent
// with UnitsForPath/UnmatchedScopes), so it is reported, never silently
// ignored. A unit with no `checks` — or a payload that is not a JSON object —
// contributes nothing.
func (e *Engine) DeclaredChecks(projectID, sourceRepo string) ([]DeclaredCheck, error) {
	units, err := e.rt.Knowledge.Units(projectID, sourceRepo)
	if err != nil {
		return nil, fmt.Errorf("contexts: %w", err)
	}
	out := []DeclaredCheck{}
	for _, u := range units {
		checks, err := declaredChecksOf(u)
		if err != nil {
			return nil, fmt.Errorf("contexts: %s: `checks` %v", u.CanonicalIdentityForm, err)
		}
		out = append(out, checks...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CanonicalForm != out[j].CanonicalForm {
			return out[i].CanonicalForm < out[j].CanonicalForm
		}
		if out[i].Command != out[j].Command {
			return out[i].Command < out[j].Command
		}
		return firstScope(out[i].Scope) < firstScope(out[j].Scope)
	})
	return out, nil
}

// declaredChecksOf reads one unit's optional `checks` declaration from its
// content payload (the canonical structured content object). A unit that
// carries no structured content object, or no `checks` key, contributes
// nothing.
func declaredChecksOf(u *exchange.Unit) ([]DeclaredCheck, error) {
	var content map[string]any
	if json.Unmarshal(u.ContentPayload, &content) != nil || content == nil {
		return nil, nil
	}
	raw, ok := content["checks"]
	if !ok {
		return nil, nil
	}
	checks, err := pathglob.InterpretChecks(raw)
	if err != nil {
		return nil, err
	}
	out := make([]DeclaredCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, DeclaredCheck{CanonicalForm: u.CanonicalIdentityForm, Command: c.Command, Scope: c.Scope})
	}
	return out, nil
}

// firstScope returns the first scope pattern string, or "" for an empty scope —
// the final deterministic ordering key of DeclaredChecks.
func firstScope(scope []pathglob.Pattern) string {
	if len(scope) == 0 {
		return ""
	}
	return scope[0].String()
}
