package conformance

import (
	"fmt"
	"strings"
)

// This file implements Rules R10-R12 of the Engineering Domain ontology
// (EKA v1.1, Wave 1 code side). The canonical mapping behind them lives
// in domain.go; these rules consume DomainForToken/Stratum exactly like
// Rules R1-R9 consume the type/state taxonomy.
//
// R10 — Stratification traceability (WARNING):
//
//	Every artifact whose stratum is not 1 (Discovery) must have a
//	resolvable reference chain (derives-from/depends-on, direct or
//	transitive) reaching an artifact in a strictly higher stratum.
//	Exempt: tkt-/ses- tokens (pure projections/operating records) and
//	cmt- tokens (notes are operating records whose only edge is
//	discusses — ADR-019 D5 — and stratification traceability is not
//	meaningful for them), plus artifacts whose content-state is draft
//	(knowledge artifacts only: work-item tokens own no content-state
//	and are never exempt via this clause — they require the chain like
//	every other non-draft artifact). Violations are warnings:
//	stratification is a structural quality signal, never a commit
//	blocker.
//
// R11 — Domain coherence (BLOCKING):
//
//	The optional `domain` frontmatter field, when present, must be one
//	of the five canonical Engineering Domains AND equal the artifact's
//	home domain (DomainForToken). Absent = OK (the domain is derived).
//
// R12 — Cross-stratum supersession prohibition (BLOCKING):
//
//	A supersedes/amends relationship may never target an artifact in a
//	strictly higher stratum (smaller stratum number): durable content
//	moves down the authority chain, never up. Unresolvable targets are
//	left to R5 (dangling references); R12 evaluates resolvable targets
//	only.

// ---------------------------------------------------------------------------
// Rule 10 — Stratification traceability
// ---------------------------------------------------------------------------

func (e *engine) rule10(a *Artifact) {
	if _, known := typeTokens[a.Type]; !known {
		return // R0 already reported the unknown type.
	}
	home, ok := DomainForToken(a.Type)
	if !ok {
		return // Unreachable for known tokens; defensive.
	}
	if Stratum(home) == 1 {
		return // Discovery: top of the authority chain, nothing above.
	}
	if a.Type == "tkt" || a.Type == "ses" || a.Type == "cmt" {
		return // Exempt tokens: projections, session records and notes
		// (operating records whose only edge is discusses, ADR-019).
	}
	if a.States[DomainContentState] == "draft" {
		return // Draft exemption (knowledge artifacts carry content-state;
		// work items never do, so they are never exempt via this clause).
	}

	// Deterministic BFS over resolvable derives-from/depends-on edges
	// (the same parse/resolution machinery R5 uses). The chain is
	// satisfied when any reached artifact lives in a strictly higher
	// stratum (lower stratum number). Self-references and cycles are
	// harmless: the visited set bounds the walk.
	visited := map[*Artifact]bool{a: true}
	queue := []*Artifact{a}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, field := range []string{"derives-from", "depends-on"} {
			for _, raw := range cur.Relations[field] {
				ref, err := parseReference(raw, cur.Namespace, cur.Type)
				if err != nil {
					continue // R5 reports malformed references.
				}
				target := e.resolve(ref)
				if target == nil || visited[target] {
					continue
				}
				targetDomain, ok := DomainForToken(target.Type)
				if !ok {
					continue // Unknown target type: R0 reports it.
				}
				if Stratum(targetDomain) < Stratum(home) {
					return // Chain reaches a strictly higher stratum.
				}
				visited[target] = true
				queue = append(queue, target)
			}
		}
	}
	e.add(a, Rule10, SeverityWarning,
		"no resolvable derives-from/depends-on chain reaches a stratum above %s (stratum %d); stratification traceability is missing (chains must reach one of: %s)",
		home, Stratum(home), strings.Join(domainNames(StrataAbove(home)), ", "))
}

func domainNames(domains []Domain) []string {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		out = append(out, string(d))
	}
	return out
}

// ---------------------------------------------------------------------------
// Rule 11 — Domain coherence
// ---------------------------------------------------------------------------

func (e *engine) rule11(a *Artifact) {
	if a.Domain == "" {
		return // Absent: the domain is derived, nothing to check.
	}
	if _, known := typeTokens[a.Type]; !known {
		return // R0 already reported the unknown type.
	}
	if !IsDomain(a.Domain) {
		e.add(a, Rule11, SeverityError,
			"unknown engineering domain %q (canonical domains: %s)",
			a.Domain, strings.Join(DomainNames(), ", "))
		return
	}
	home, _ := DomainForToken(a.Type)
	if a.Type == "cmt" {
		// Notes are contextable (ADR-019 D8 revised): a cmt- note
		// declares the Engineering Domain its discussion is contexted
		// in — ANY of the five canonical domains is coherent, so the
		// home-domain equality check does not apply to notes. Unknown
		// domains are still refused above.
		return
	}
	if a.Domain != string(home) {
		e.add(a, Rule11, SeverityError,
			"declared domain %q does not match the home domain %q of type %q",
			a.Domain, home, a.Type)
	}
}

// ---------------------------------------------------------------------------
// Rule 12 — Cross-stratum supersession prohibition
// ---------------------------------------------------------------------------

func (e *engine) rule12(a *Artifact) {
	if _, known := typeTokens[a.Type]; !known {
		return // R0 already reported the unknown type.
	}
	home, ok := DomainForToken(a.Type)
	if !ok {
		return // Unreachable for known tokens; defensive.
	}
	for _, field := range []string{"supersedes", "amends"} {
		for _, raw := range a.Relations[field] {
			ref, err := parseReference(raw, a.Namespace, a.Type)
			if err != nil {
				continue // R5 reports malformed references.
			}
			target := e.resolve(ref)
			if target == nil {
				continue // R5 reports dangling references; R12 evaluates
				// resolvable targets only.
			}
			targetDomain, ok := DomainForToken(target.Type)
			if !ok {
				continue // Unknown target type: R0 reports it.
			}
			if Stratum(targetDomain) < Stratum(home) {
				e.add(a, Rule12, SeverityError,
					"%s targets %s (%s, stratum %d), which is strictly higher than %s (stratum %d); cross-stratum supersession is prohibited",
					field, artifactIdentityForm(target), targetDomain,
					Stratum(targetDomain), home, Stratum(home))
			}
		}
	}
}

// artifactIdentityForm renders the canonical identity form of an artifact
// for diagnostics ("<namespace>/<type>:<id>:<instance-version>").
func artifactIdentityForm(a *Artifact) string {
	return fmt.Sprintf("%s/%s:%s:%d", a.Namespace, a.Type, a.ID, a.InstanceVersion)
}
