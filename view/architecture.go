package view

import "github.com/maleolabs/eka-core/conformance"

// This file implements the architecture projection: the Architecture
// domain (adr-, dec-, arc-, spec-, std-, gls-) grouped by type in fixed
// order. Decisions merge the two decision artifacts — adr- (content-
// state variant proposed/accepted/superseded) and dec- (variant
// draft/accepted/superseded); the remaining groups are
// single-token: arc-, spec-, std-, gls-. Every entry carries its
// content-state.
//
// The architecture projection ignores the optional target argument.

// ArchitectureProjection is the Architecture domain view.
type ArchitectureProjection struct {
	// Groups are the fixed artifact groups in order: Decisions,
	// Architecture Descriptions, Specifications, Standards &
	// Guidelines, Vocabulary.
	Groups []Group
}

// Name returns the registry name of the projection.
func (p *ArchitectureProjection) Name() string { return "architecture" }

func buildArchitecture(g *Graph, target string) (Projection, error) {
	return &ArchitectureProjection{
		Groups: domainGroups(g, conformance.Architecture, []groupDef{
			{[]string{"adr", "dec"}, "Decisions"},
			{[]string{"arc"}, "Architecture Descriptions"},
			{[]string{"spec"}, "Specifications"},
			{[]string{"std"}, "Standards & Guidelines"},
			{[]string{"gls"}, "Vocabulary"},
		}),
	}, nil
}
