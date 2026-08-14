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
		// Unqualified: the grammar resolves bare forms against a
		// referrer's namespace (defNamespace) — the Runtime resolves
		// globally and has no referrer context, so the namespace must
		// be spelled out. Canonical/qualified only.
		return nil, false, fmt.Errorf("runtime: resolve: %q is an unqualified reference (missing the <ns>/ prefix); canonical form <ns>/<type>:<id>:<v> or qualified line form <ns>/<type>:<id> required", form)
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
