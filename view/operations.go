package view

import "github.com/maleolabs/eka-core/conformance"

// This file implements the operations projection: the Operations domain
// (run-, rel-) grouped by type in fixed order. Every entry carries its
// content-state.
//
// The operations projection ignores the optional target argument.

// OperationsProjection is the Operations domain view.
type OperationsProjection struct {
	// Groups are the fixed artifact groups in order: Runbooks, Release
	// Records.
	Groups []Group
}

// Name returns the registry name of the projection.
func (p *OperationsProjection) Name() string { return "operations" }

func buildOperations(g *Graph, target string) (Projection, error) {
	return &OperationsProjection{
		Groups: domainGroups(g, conformance.Operations, []groupDef{
			{[]string{"run"}, "Runbooks"},
			{[]string{"rel"}, "Release Records"},
		}),
	}, nil
}
