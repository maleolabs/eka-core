package runtime

import (
	"fmt"
	"sort"

	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the RelationsService: relationship traversal
// over the canonical store. The Runtime makes traversal natural —
// consumers never hand-walk relationships. Relationships live inside
// the immutable payloads (unit.Relationships, ordered by (type,
// target)); traversal resolves them against the workspace references.

// Relation is one directed relationship edge incident to a queried
// form: Type is the relationship type, Target is the canonical
// identity form of the OTHER end of the edge — the referenced unit for
// From (outgoing), the referring unit for To (incoming).
type Relation struct {
	Type   string
	Target string
}

// RelationsService traverses the relationship graph of the workspace:
// outgoing edges (From), incoming edges (To), and the resolved unit
// sets (Upstream/Downstream). Concrete and documented — no interface
// type.
type RelationsService struct{ rt *Runtime }

// From returns the outgoing relationships of the unit at form — its
// stored relationships in (type, target) order. The form must resolve
// to a stored unit: a nonexistent identity is an error (the consumer
// asked about an object that is not there), never a silent empty list.
func (s *RelationsService) From(form string) ([]Relation, error) {
	u, ok, err := s.rt.Knowledge.Object(form)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("runtime: relations: no unit with form %s", form)
	}
	out := make([]Relation, 0, len(u.Relationships))
	for _, rel := range u.Relationships {
		out = append(out, Relation{Type: rel.Type, Target: rel.Target})
	}
	return out, nil
}

// To returns every incoming relationship pointing AT target across the
// whole workspace: each returned Relation's Target is the canonical
// form of the referring unit (the other end of the reversed edge).
//
// Implementation trade-off (documented): the scan iterates every
// project (sorted by id) and every unit of each project (sorted by
// canonical form) and filters in memory — deterministic O(n) over the
// workspace knowledge, which is the point of the Runtime: consumers
// never hand-walk relationships themselves, and at workspace scale the
// linear scan is the honest cost of a workspace-wide reverse index.
func (s *RelationsService) To(target string) ([]Relation, error) {
	projects, err := s.rt.Workspace.Projects()
	if err != nil {
		return nil, err
	}
	seen := map[Relation]bool{}
	out := make([]Relation, 0)
	for _, p := range projects {
		units, err := s.rt.Knowledge.UnitsByProject(p.ID)
		if err != nil {
			return nil, err
		}
		for _, u := range units {
			for _, rel := range u.Relationships {
				if rel.Target != target {
					continue
				}
				r := Relation{Type: rel.Type, Target: u.CanonicalIdentityForm}
				if seen[r] {
					continue // Dedup: duplicate identical edges in a
					// payload must not surface twice.
				}
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// Upstream resolves every outgoing target of the unit at form: the
// units its relationships point at, sorted by canonical form. Targets
// that no longer resolve (draft tolerance: a referenced identity
// without a stored instance) are skipped — documented, never an error.
// Duplicate targets (several relationship types to the same unit)
// resolve once.
func (s *RelationsService) Upstream(form string) ([]*exchange.Unit, error) {
	rels, err := s.From(form)
	if err != nil {
		return nil, err
	}
	return s.resolveUnique(rels)
}

// Downstream resolves every unit that references form: the incoming
// edges (To) with their referring units resolved, sorted by canonical
// form. Unresolvable referring units (draft tolerance) are skipped.
// Duplicate referrers resolve once.
func (s *RelationsService) Downstream(form string) ([]*exchange.Unit, error) {
	rels, err := s.To(form)
	if err != nil {
		return nil, err
	}
	return s.resolveUnique(rels)
}

// resolveUnique resolves every relation's Target (the other end of the
// edge) to its unit, skipping unresolvable targets, deduplicating by
// canonical form, and sorting by canonical form — the shared
// deterministic projection of Upstream and Downstream.
func (s *RelationsService) resolveUnique(rels []Relation) ([]*exchange.Unit, error) {
	seen := map[string]bool{}
	out := make([]*exchange.Unit, 0, len(rels))
	for _, rel := range rels {
		u, ok, err := s.rt.Resolver.Resolve(rel.Target)
		if err != nil {
			return nil, err
		}
		if !ok {
			// Draft tolerance: a relationship target without a stored
			// instance is skipped, never an error.
			continue
		}
		if seen[u.CanonicalIdentityForm] {
			continue
		}
		seen[u.CanonicalIdentityForm] = true
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CanonicalIdentityForm < out[j].CanonicalIdentityForm
	})
	return out, nil
}
