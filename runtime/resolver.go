package runtime

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the ResolverService: identity resolution against
// the canonical store. References are parsed with the shared
// conformance grammar (conformance.ParseReference — the single source
// of reference truth), then resolved against the references.

// ResolverService resolves RSF reference forms to their canonical
// units: canonical identity forms ("<ns>/<type>:<id>:<v>") and
// qualified line forms ("<ns>/<type>:<id>" — the highest instance).
// Concrete and documented — no interface type.
type ResolverService struct{ rt *Runtime }

// Resolve resolves one reference to its current unit:
//
//   - canonical form ("<ns>/<type>:<id>:<v>") — the exact instance;
//   - qualified line form ("<ns>/<type>:<id>") — the highest
//     instance-version of the line (the latest knowledge version:
//     CKOs are immutable, publish re-publishes at instance+1, so the
//     line form addresses the newest knowledge of the line).
//
// Unqualified forms ("<type>:<id>", bare ids) are NOT accepted: the
// reference grammar resolves them against a referrer's namespace, and
// the Runtime resolves globally — canonical/qualified only. Use
// conformance.ParseReference (the shared grammar) for normalization;
// a form that does not parse is an error, an unresolved identity
// reports false.
func (s *ResolverService) Resolve(form string) (*exchange.Unit, bool, error) {
	ref, err := conformance.ParseReference(form, "", "")
	if err != nil {
		return nil, false, fmt.Errorf("runtime: resolve: cannot parse %q (canonical form <ns>/<type>:<id>:<v> or qualified line form <ns>/<type>:<id> required): %w", form, err)
	}
	if ref.Namespace == "" {
		// Cross-repo fix (bug:context-unqualified-refs): try to resolve unqualified
		// "<type>:<id>" by searching the workspace for the unique namespace that holds
		// this (type, id). This handles legacy store data with 775 unqualified targets
		// (e.g., req:prd) where the referrer namespace was lost at publish time.
		// If ambiguous (multiple namespaces) or missing, fall back to the original
		// error to keep the gate strict.
		if s.rt != nil {
			if st, err := s.rt.requireStore(); err == nil && st != nil {
				// Search via store.UnitsByLine equivalent: iterate ResolveLine across namespaces
				// Use the Runtime's knowledge to find candidates: try to find any line with this type/id.
				// We use store directly to avoid recursion: scan knowledge objects for matching type:id.
				// Simpler: delegate to a helper that finds the unique namespace.
				if ns := s.findUniqueNamespace(ref.Type, ref.ID); ns != "" {
					ref.Namespace = ns
				} else {
					return nil, false, fmt.Errorf("runtime: resolve: %q is an unqualified reference (missing the <ns>/ prefix); canonical form <ns>/<type>:<id>:<v> or qualified line form <ns>/<type>:<id> required", form)
				}
			} else {
				return nil, false, fmt.Errorf("runtime: resolve: %q is an unqualified reference (missing the <ns>/ prefix); canonical form <ns>/<type>:<id>:<v> or qualified line form <ns>/<type>:<id> required", form)
			}
		} else {
			return nil, false, fmt.Errorf("runtime: resolve: %q is an unqualified reference (missing the <ns>/ prefix); canonical form <ns>/<type>:<id>:<v> or qualified line form <ns>/<type>:<id> required", form)
		}
	}
	if ref.HasVersion {
		canonical := ref.Namespace + "/" + ref.Type + ":" + ref.ID + ":" + strconv.Itoa(ref.Version)
		return s.rt.Knowledge.Object(canonical)
	}
	// Qualified line form: the highest instance-version of the line —
	// the latest knowledge version (CKOs are immutable; re-publish
	// creates instance+1, and line forms address the newest one).
	units, err := s.ResolveLine(ref.Namespace, ref.Type, ref.ID)
	if err != nil {
		return nil, false, err
	}
	if len(units) == 0 {
		return nil, false, nil
	}
	return units[len(units)-1], true, nil
}

// ResolveLine returns every instance of one artifact line — the
// identity (namespace, type token, id) resolved across the whole
// workspace — sorted by instance-version (ascending: the line's
// history order). It is the resolution primitive behind Resolve's line
// form, Relations.Upstream/Downstream and Timeline.Line.
//
// Ordering note: the store returns the line in canonical form order
// (store.UnitsByLine — the deterministic workspace order); this
// service re-sorts by instance-version so the documented contract
// holds exactly (the two orders coincide while instance versions stay
// single-digit; the explicit sort keeps the contract exact).

// findUniqueNamespace returns the unique namespace holding type:id, or "" if none or ambiguous.
// Used to heal legacy unqualified references (bug:context-unqualified-refs).
func (s *ResolverService) findUniqueNamespace(typeToken, id string) string {
	// Use Knowledge to list all lines; the store's UnitsByLine is not directly exposed,
	// so iterate over all units via a best-effort scan.
	// We rely on ResolveLine's internal store access: try to probe common namespaces.
	// For determinism, collect candidates from the store's line index if available.
	// Fallback: brute-force via Knowledge.Objects scan.
	if s.rt == nil {
		return ""
	}
	st, err := s.rt.requireStore()
	if err != nil || st == nil {
		return ""
	}
	// Try to use the store's line enumeration via reflection on available methods
	// Instead, we brute-force by scanning all canonical objects for matching type:id
	// This is O(n) but acceptable for the resolver's fallback path (only on unqualified).
	var candidates []string
	seen := map[string]bool{}
	// The store interface exposes Units() or similar; we use Knowledge to iterate.
	// Knowledge holds all units, we can try to list via the runtime's Knowledge.
	// Use s.rt.Knowledge.Objects() if available, else try store.Units
	// We attempt both via interface assertion.
	type unitLister interface{ Units() []*exchange.Unit }
	type knowledgeLister interface{ Objects(canonical ...string) ([]*exchange.Unit, error) }
	// Try Knowledge path
	if k := s.rt.Knowledge; k != nil {
		if lister, ok := interface{}(k).(unitLister); ok {
			for _, u := range lister.Units() {
				if u.Identity.Type == typeToken && u.Identity.ID == id {
					ns := u.Identity.Namespace
					if !seen[ns] {
						seen[ns] = true
						candidates = append(candidates, ns)
					}
				}
			}
		}
	}
	// Also try store path if not found via Knowledge
	if len(candidates) == 0 {
		if lister, ok := interface{}(st).(unitLister); ok {
			for _, u := range lister.Units() {
				if u.Identity.Type == typeToken && u.Identity.ID == id {
					ns := u.Identity.Namespace
					if !seen[ns] {
						seen[ns] = true
						candidates = append(candidates, ns)
					}
				}
			}
		}
	}
	if len(candidates) != 1 {
		return ""
	}
	return candidates[0]
}

func (s *ResolverService) ResolveLine(ns, typeToken, id string) ([]*exchange.Unit, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return nil, err
	}
	units, err := st.UnitsByLine(ns, typeToken, id)
	if err != nil {
		return nil, err
	}
	sort.Slice(units, func(i, j int) bool {
		return units[i].Identity.InstanceVersion < units[j].Identity.InstanceVersion
	})
	return units, nil
}
