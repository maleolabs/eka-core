package runtime

import (
	"fmt"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/store"
)

// This file implements the KnowledgeService: the Engineering-Knowledge
// read side of the Runtime. It is deliberately NOT CRUD — the shape is
// knowledge-shaped: the projection source (units of a project), the
// per-repository view, single-object resolution, filtered search and
// workspace counts.

// KnowledgeService reads the canonical Engineering Knowledge of the
// workspace: units, objects, search and counts. Concrete and
// documented — no interface type.
type KnowledgeService struct{ rt *Runtime }

// UnitsByProject returns the complete Engineering Knowledge of one
// project: the union of every registered repository's units, decoded
// from their immutable payloads, sorted by canonical form. This is the
// projection source of the Runtime — the one read the projection
// engine consumes.
func (s *KnowledgeService) UnitsByProject(projectID string) ([]*exchange.Unit, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return nil, err
	}
	return st.UnitsByProject(projectID)
}

// Units returns the canonical units of one repository (its provenance
// pair), decoded from their immutable payloads, sorted by canonical
// form.
func (s *KnowledgeService) Units(projectID, sourceRepo string) ([]*exchange.Unit, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return nil, err
	}
	return st.Units(projectID, sourceRepo)
}

// Object loads one Canonical Knowledge Object by its canonical identity
// form ("<ns>/<type>:<id>:<v>") — the single-object resolution of the
// Runtime ("Load/Resolve Knowledge"). The form must be canonical: use
// Resolver.Resolve for reference parsing and line resolution.
func (s *KnowledgeService) Object(form string) (*exchange.Unit, bool, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return nil, false, err
	}
	return st.Unit(form)
}

// SearchQuery filters the Knowledge search. Empty filter fields match
// anything; the project is required (Knowledge is project-shaped).
// Matching is exact-match only — no partial or prefix matching in this
// milestone (documented).
type SearchQuery struct {
	// ProjectID is the required project scope.
	ProjectID string
	// Namespace, Type and ID are identity filters ("" = any).
	Namespace string
	Type      string
	ID        string
	// Dimension, Domain and Phase are the classification/context
	// filters ("" = any) — the ref index columns, derived from the
	// payloads at insert.
	Dimension string
	Domain    string
	Phase     string
}

// Search filters the complete Engineering Knowledge of one project by
// the exact-match criteria of the query, sorted by canonical form.
//
// Implementation trade-off (documented): the filter runs in memory
// over store.UnitsByProject — one SQL read, then deterministic
// filtering. This is fine at runtime scale (workspace-local knowledge)
// and keeps the search semantics explicit; a SQL-side filter would
// duplicate the filter logic in SQL without a measurable win. No
// partial/prefix matching in this milestone.
func (s *KnowledgeService) Search(query SearchQuery) ([]*exchange.Unit, error) {
	if query.ProjectID == "" {
		return nil, fmt.Errorf("runtime: search requires a project id")
	}
	units, err := s.UnitsByProject(query.ProjectID)
	if err != nil {
		return nil, err
	}
	out := make([]*exchange.Unit, 0, len(units))
	for _, u := range units {
		if query.Namespace != "" && u.Identity.Namespace != query.Namespace {
			continue
		}
		if query.Type != "" && u.Identity.Type != query.Type {
			continue
		}
		if query.ID != "" && u.Identity.ID != query.ID {
			continue
		}
		if query.Dimension != "" && u.Classification.Dimension != query.Dimension {
			continue
		}
		if query.Domain != "" && u.Classification.Domain != query.Domain {
			continue
		}
		if query.Phase != "" && u.Phase != query.Phase {
			continue
		}
		out = append(out, u)
	}
	return out, nil
}

// Counts returns the canonical store totals of the workspace: objects
// (references — the current objects of the immutable model), immutable
// payloads, attachments.
func (s *KnowledgeService) Counts() (objects, payloads, attachments int, err error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return 0, 0, 0, err
	}
	if objects, err = st.RefCount(); err != nil {
		return 0, 0, 0, err
	}
	if payloads, err = st.PayloadCount(); err != nil {
		return 0, 0, 0, err
	}
	if attachments, err = st.AttachmentCount(); err != nil {
		return 0, 0, 0, err
	}
	return objects, payloads, attachments, nil
}

// --- Issue numbers (RFC: per-group incremental numbers) ---

// NumberForLine returns the issue number of one line (0 = the line
// carries no number).
func (s *KnowledgeService) NumberForLine(projectID, ns, typeToken, id string) (int, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return 0, err
	}
	return st.NumberForLine(projectID, ns, typeToken, id)
}

// LineByNumber resolves an issue number to every matching line of the
// project (one per group — work items, tickets and notes count
// independently, so "#42" can match up to three lines). Sorted by
// (group, form).
func (s *KnowledgeService) LineByNumber(projectID string, number int) ([]store.NumberedLine, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return nil, err
	}
	return st.LineByNumber(projectID, number)
}

// LineByNumberGroup resolves an issue number within ONE group (the
// unambiguous form: `eka view ticket #42`).
func (s *KnowledgeService) LineByNumberGroup(projectID, group string, number int) (store.NumberedLine, bool, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return store.NumberedLine{}, false, err
	}
	return st.LineByNumberGroup(projectID, group, number)
}

// NumbersByProject returns the line-form -> issue number map of the
// project (only numbered lines).
func (s *KnowledgeService) NumbersByProject(projectID string) (map[string]int, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return nil, err
	}
	return st.NumbersByProject(projectID)
}
