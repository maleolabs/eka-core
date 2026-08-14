package conformance

import (
	"sort"
	"strings"
)

// This file implements the Engineering Domain ontology of the EKA v1.1
// standard: the canonical mapping of the 27 artifact type tokens (26 + cmt, ADR-019 D3) and the
// 12 knowledge dimensions onto the five Engineering Domains, and the
// five-stratum authority ordering over them (stratum 1 = highest
// authority).
//
// The mapping is the single source of truth for domain derivation: the
// validator (Rules R10-R12), the exchange/ layer (unit classification)
// and the view/ layer all consume these pure functions instead of
// duplicating the table.
//
// Grounding: standard/eka-specification-v1.1.md §Engineering Domain
// (Wave 1 mapping table):
//
//	Discovery    (1): vis-, str-, req-, fnd- | intent, requirements, research
//	Architecture (2): arc-, adr-, dec-, spec-, std-, gls- | architecture,
//	                 decisions, specifications, standards, vocabulary
//	Planning     (3): scp-, epc-, plan-, trc- | planning
//	Execution    (4): rvw-, ctr-, tkt-, sto-, ts-, bug-, td-, ch-, spk-,
//	                 ses- | quality
//	Operations   (5): run-, rel- | operations, records

// Domain is one of the five canonical Engineering Domains. The string
// value is the canonical spelling used in frontmatter, unit.json and the
// CLI (e.g. "Execution").
type Domain string

const (
	Discovery    Domain = "Discovery"
	Architecture Domain = "Architecture"
	Planning     Domain = "Planning"
	Execution    Domain = "Execution"
	Operations   Domain = "Operations"
)

// domains is the canonical domain set in stratum order (1 = highest
// authority). Order is significant: it is the deterministic iteration
// order used by StrataAbove and DomainNames.
var domains = []Domain{Discovery, Architecture, Planning, Execution, Operations}

// tokenDomain maps every artifact type token to its home Engineering
// Domain (the Wave 1 mapping table, token column).
var tokenDomain = map[string]Domain{
	// Discovery (1).
	"vis": Discovery, "str": Discovery, "req": Discovery, "fnd": Discovery,
	// Architecture (2).
	"arc": Architecture, "adr": Architecture, "dec": Architecture,
	"spec": Architecture, "std": Architecture, "gls": Architecture,
	// Planning (3).
	"scp": Planning, "epc": Planning, "plan": Planning, "trc": Planning,
	// Execution (4).
	"rvw": Execution, "ctr": Execution, "tkt": Execution,
	"sto": Execution, "ts": Execution, "bug": Execution, "td": Execution,
	"ch": Execution, "spk": Execution, "ses": Execution, "cmt": Execution,
	// Operations (5).
	"run": Operations, "rel": Operations,
}

// dimensionDomain maps every knowledge dimension to its home Engineering
// Domain (the Wave 1 mapping table, dimension column). Execution owns the
// `quality` dimension plus every operating token; the remaining operating
// dimensions (operations, records) live in Operations.
var dimensionDomain = map[string]Domain{
	// Discovery (1).
	"intent": Discovery, "requirements": Discovery, "research": Discovery,
	// Architecture (2).
	"architecture": Architecture, "decisions": Architecture,
	"specifications": Architecture, "standards": Architecture,
	"vocabulary": Architecture,
	// Planning (3).
	"planning": Planning,
	// Execution (4).
	"quality": Execution,
	// Operations (5).
	"operations": Operations, "records": Operations,
}

// DomainForToken returns the home Engineering Domain of an artifact type
// token. The second return value is false for unknown tokens (a token not
// in the 27-token table has no home domain).
func DomainForToken(token string) (Domain, bool) {
	d, ok := tokenDomain[token]
	return d, ok
}

// DomainForDimension returns the home Engineering Domain of a knowledge
// dimension. The second return value is false for unknown dimensions.
func DomainForDimension(dim string) (Domain, bool) {
	d, ok := dimensionDomain[dim]
	return d, ok
}

// Stratum returns the authority stratum of a domain: 1 = highest
// authority (Discovery), 5 = lowest (Operations). An unknown domain
// yields 0.
func Stratum(d Domain) int {
	switch d {
	case Discovery:
		return 1
	case Architecture:
		return 2
	case Planning:
		return 3
	case Execution:
		return 4
	case Operations:
		return 5
	default:
		return 0
	}
}

// StrataAbove returns the domains with strictly higher authority than d
// (lower stratum numbers), sorted by stratum ascending then name.
// Deterministic.
func StrataAbove(d Domain) []Domain {
	var out []Domain
	for _, cand := range domains {
		if Stratum(cand) < Stratum(d) {
			out = append(out, cand)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if Stratum(out[i]) != Stratum(out[j]) {
			return Stratum(out[i]) < Stratum(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// DomainNames returns all five canonical domain names, sorted
// lexicographically. Deterministic.
func DomainNames() []string {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		out = append(out, string(d))
	}
	sort.Strings(out)
	return out
}

// IsDomain reports whether s is one of the five canonical Engineering
// Domain names.
func IsDomain(s string) bool {
	for _, d := range domains {
		if string(d) == s {
			return true
		}
	}
	return false
}

// ParseDomain resolves a user-supplied Engineering Domain token to its
// canonical Domain: the canonical name ("Architecture") or the
// lowercase query token ("architecture"), matched case-insensitively.
// The second return value is false for unknown tokens.
func ParseDomain(s string) (Domain, bool) {
	for _, d := range domains {
		if strings.EqualFold(string(d), s) {
			return d, true
		}
	}
	return "", false
}
