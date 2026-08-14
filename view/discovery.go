package view

import "github.com/maleolabs/eka-core/conformance"

// This file implements the discovery projection: the Discovery domain
// (vis-, str-, req-, fnd-) grouped by type in fixed order. Every entry
// carries its content-state.
//
// Note on research findings: the fnd- type's distillation obligation —
// a finding must distill its conclusion into a durable artifact (per
// the research conventions) — is a content-level property documented in
// the standard, not a model property. The projection deliberately never
// parses artifact content, so the obligation is not represented in the
// model.
//
// The discovery projection ignores the optional target argument.

// DiscoveryProjection is the Discovery domain view.
type DiscoveryProjection struct {
	// Groups are the fixed artifact groups in order: Vision, Strategy,
	// Requirements, Research Findings.
	Groups []Group
}

// Name returns the registry name of the projection.
func (p *DiscoveryProjection) Name() string { return "discovery" }

func buildDiscovery(g *Graph, target string) (Projection, error) {
	return &DiscoveryProjection{
		Groups: domainGroups(g, conformance.Discovery, []groupDef{
			{[]string{"vis"}, "Vision"},
			{[]string{"str"}, "Strategy"},
			{[]string{"req"}, "Requirements"},
			{[]string{"fnd"}, "Research Findings"},
		}),
	}, nil
}
