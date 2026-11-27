package contexts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/v2/conformance"
	"github.com/maleolabs/eka-core/v2/exchange"
	"github.com/maleolabs/eka-core/v2/pathglob"
)

// This file implements the snapshot side of the constraint index: the
// repository's own Discovery + Architecture knowledge (authority strata 1-2)
// reduced to the latest active instance line per (type, id), grouped by type
// token. It lives in core — beside the store-backed retrieval door — so the
// store and a committed snapshot share ONE selection/grouping and a consumer
// (the CLI) derives its projection from core, never from its own copy.

// ConstraintGroup is one type group of the constraint index: the artifact
// type token and the ids of the latest active stratum-1/2 knowledge lines of
// that type, in render order (types by (stratum, token), ids sorted
// ascending).
type ConstraintGroup struct {
	Type string
	IDs  []string
}

// ConstraintIndex reduces units to the render-ordered constraint index: the
// active (existence state "active") units of the Discovery and Architecture
// domains (authority strata 1-2, via conformance.Stratum — never hardcoded),
// collapsed to the latest instance version per (type, id) line and grouped by
// type token. Types are ordered by (stratum, token) and ids are sorted within
// a type, so the render order is deterministic. Units whose type is not a
// strata-1/2 constraint, or that are not active, contribute nothing.
func ConstraintIndex(units []*exchange.Unit) []ConstraintGroup {
	type lineKey struct{ typ, id string }
	latest := map[lineKey]int{}
	for _, u := range units {
		if _, ok := constraintStratumOf(u.Identity.Type); !ok {
			continue
		}
		if u.StateVector.ExistenceState != "active" {
			continue
		}
		k := lineKey{u.Identity.Type, u.Identity.ID}
		if v, seen := latest[k]; !seen || u.Identity.InstanceVersion > v {
			latest[k] = u.Identity.InstanceVersion
		}
	}
	byType := make(map[string][]string, len(latest))
	for k := range latest {
		byType[k.typ] = append(byType[k.typ], k.id)
	}
	return groupConstraintIndex(byType)
}

// SubtreeConstraints is the subtree binding of the constraint index: the
// constraint lines whose declared `appliesTo` scope matches at least one
// tracked file under dir (file == dir, or dir + "/" is a prefix), grouped
// exactly like ConstraintIndex. It selects the same strata-1-2 latest-active
// lines and keeps the lines that bind the subtree; a line with no scope binds
// no path.
//
// A malformed `appliesTo` is an error (reported, never silently ignored) —
// consistent with the store-backed retrieval door. The order is deterministic
// (types by (stratum, token), ids sorted); when nothing binds, the result is
// nil.
func SubtreeConstraints(units []*exchange.Unit, tracked []string, dir string) ([]ConstraintGroup, error) {
	type lineKey struct{ typ, id string }
	latest := map[lineKey]scopedLine{}
	for _, u := range units {
		if _, ok := constraintStratumOf(u.Identity.Type); !ok {
			continue
		}
		if u.StateVector.ExistenceState != "active" {
			continue
		}
		patterns, declared, err := exchange.AppliesTo(u)
		if err != nil {
			return nil, fmt.Errorf("%s: `appliesTo` %v", u.CanonicalIdentityForm, err)
		}
		if !declared {
			continue
		}
		k := lineKey{u.Identity.Type, u.Identity.ID}
		if prev, seen := latest[k]; seen && u.Identity.InstanceVersion <= prev.version {
			continue
		}
		latest[k] = scopedLine{
			typ:      u.Identity.Type,
			id:       u.Identity.ID,
			version:  u.Identity.InstanceVersion,
			patterns: patterns,
		}
	}
	byType := map[string][]string{}
	for _, c := range latest {
		if constraintBinds(c, tracked, dir) {
			byType[c.typ] = append(byType[c.typ], c.id)
		}
	}
	if len(byType) == 0 {
		return nil, nil
	}
	return groupConstraintIndex(byType), nil
}

// scopedLine is one constraint line (latest active instance of the strata-1/2
// domains) carrying its declared `appliesTo` scope.
type scopedLine struct {
	typ      string
	id       string
	version  int
	patterns []pathglob.Pattern
}

// groupConstraintIndex turns a type→ids map into the render-ordered index:
// types by (stratum, token), ids sorted within a type. It is the ONE grouping
// the root index and every nested subtree index share.
func groupConstraintIndex(byType map[string][]string) []ConstraintGroup {
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		si, sj := constraintStratum(types[i]), constraintStratum(types[j])
		if si != sj {
			return si < sj
		}
		return types[i] < types[j]
	})
	index := make([]ConstraintGroup, 0, len(types))
	for _, t := range types {
		ids := byType[t]
		sort.Strings(ids)
		index = append(index, ConstraintGroup{Type: t, IDs: ids})
	}
	return index
}

// constraintStratumOf resolves the authority stratum of an artifact type token
// and reports whether it is in the projected range (strata 1-2, Discovery +
// Architecture). Unknown tokens are not constraints.
func constraintStratumOf(token string) (int, bool) {
	d, ok := conformance.DomainForToken(token)
	if !ok {
		return 0, false
	}
	s := conformance.Stratum(d)
	if s != 1 && s != 2 {
		return 0, false
	}
	return s, true
}

// constraintStratum resolves the authority stratum of an artifact type token
// via the shared conformance mapping (0 for an unknown token).
func constraintStratum(token string) int {
	d, _ := conformance.DomainForToken(token)
	return conformance.Stratum(d)
}

// constraintBinds reports whether any tracked file under dir matches one of
// the line's declared patterns.
func constraintBinds(c scopedLine, tracked []string, dir string) bool {
	for _, f := range tracked {
		if !underDir(f, dir) {
			continue
		}
		for _, p := range c.patterns {
			if p.Match(f) {
				return true
			}
		}
	}
	return false
}

// underDir reports whether file is dir itself or lives below it.
func underDir(file, dir string) bool {
	return file == dir || strings.HasPrefix(file, dir+"/")
}
