package exchange

import (
	"fmt"
)

// This file implements the import-side resolvers (Exchange §7.4):
//
//   - IdentityResolver indexes the existing target repository artifacts
//     (canonical identity form -> loaded artifact, via the shared loader)
//     and classifies package units/attachments against that index;
//   - RelationshipResolver resolves every package relationship in the
//     Exchange §7.4 order: local (carried by the package) -> global
//     (already in the target repository) -> external (declared in the
//     package Declarations Block). Unresolved non-draft references are
//     blocking; unresolved references on draft units are warnings (draft
//     tolerance, rule 5).
//
// The identity index is built once per import run (conformance.Scan +
// content read via load.go) and reused by the conflict analyzer — one
// repository read, deterministic classification.

// IdentityResolver indexes the target repository and classifies package
// content against it.
type IdentityResolver struct {
	// repo is the loaded target repository (nil when the repository holds
	// no artifacts).
	repo *loadedRepo
}

// newIdentityResolver builds the resolver index from the target repository.
func newIdentityResolver(root string) (*IdentityResolver, error) {
	repo, err := load(root)
	if err != nil {
		return nil, fmt.Errorf("cannot index target repository: %w", err)
	}
	return &IdentityResolver{repo: repo}, nil
}

// repoUnit returns the projection of the repository artifact with the
// given canonical identity form (nil when absent). The projection uses the
// exact same mapping as the exporter (toUnit), so package/repository
// payload comparison is apples-to-apples.
func (r *IdentityResolver) repoUnit(form string) *Unit {
	la := r.repo.instanceByForm[form]
	if la == nil {
		return nil
	}
	return toUnit(la)
}

// formExists reports whether the repository already holds the exact
// instance named by the canonical form.
func (r *IdentityResolver) formExists(form string) bool {
	_, ok := r.repo.instanceByForm[form]
	return ok
}

// resolveGlobal resolves a relationship target against the target
// repository: true when the exact instance exists.
func (r *IdentityResolver) resolveGlobal(form string) bool {
	return r.formExists(form)
}

// RelationshipResolver resolves package relationships per Exchange §7.4.
type RelationshipResolver struct {
	// pkgUnits is the set of canonical identity forms carried by the
	// package (local resolution).
	pkgUnits map[string]bool
	// target is the identity resolver of the target repository (global
	// resolution).
	target *IdentityResolver
	// declarations maps "source\x00type\x00target" -> declared.
	declarations map[string]ExternalReference
}

// newRelationshipResolver builds the resolver for one package.
func newRelationshipResolver(pkg *loadedPackage, target *IdentityResolver) *RelationshipResolver {
	units := map[string]bool{}
	for form := range pkg.unitByForm {
		units[form] = true
	}
	decls := map[string]ExternalReference{}
	for _, d := range pkg.declarations.ExternalReferences {
		decls[declKey(d)] = d
	}
	return &RelationshipResolver{
		pkgUnits:     units,
		target:       target,
		declarations: decls,
	}
}

func declKey(d ExternalReference) string {
	return d.Source + "\x00" + d.Type + "\x00" + d.Target
}

// isDeclared reports whether the exact (source, type, target) triple is
// declared as an External Reference.
func (r *RelationshipResolver) isDeclared(source, relType, target string) bool {
	_, ok := r.declarations[source+"\x00"+relType+"\x00"+target]
	return ok
}

// resolved reports the resolution outcome of one relationship target for
// one source unit (Exchange §7.4 order: local -> global -> external).
type resolution struct {
	// ok reports that the reference is resolved (local or global).
	ok bool
	// declared reports whether the target is declared external.
	declared bool
}

// resolve runs the Exchange §7.4 resolution order for one relationship.
func (r *RelationshipResolver) resolve(source, relType, target string) resolution {
	if r.pkgUnits[target] {
		return resolution{ok: true, declared: r.isDeclared(source, relType, target)}
	}
	if r.target.resolveGlobal(target) {
		return resolution{ok: true, declared: r.isDeclared(source, relType, target)}
	}
	return resolution{ok: false, declared: r.isDeclared(source, relType, target)}
}

// isDraft reports whether a unit qualifies for draft tolerance: content
// state "draft" (rule 5).
func isDraft(u *Unit) bool {
	return u.StateVector.ContentState == "draft"
}
