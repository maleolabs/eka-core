package conformance

import (
	"fmt"
	"os"
	"path/filepath"
)

// This file implements the read-only artifact scan entry point:
//
//	artifacts, err := conformance.Scan(root)
//
// Scan reuses the exact walk policy and classification of Validate
// (collectAuthoringPaths + analyzeFile) but performs no rule evaluation:
// it returns every classified Artifact, in scan order. It exists for
// consumers that need the parsed artifact model without the R1-R12 verdict
// (e.g. the exchange/ package, whose export pipeline runs Validate as its
// own gate and then needs the artifacts themselves).
//
// Design notes:
//   - The scan scope is the <root>/docs knowledge tree only (the shared
//     policy of ADR-018 Decision 2): when docs/ does not exist, the scan
//     is EMPTY — the root is never walked wholesale. The legacy
//     whole-tree sweep is removed.
//   - R0 structural results produced during classification (unparseable
//     frontmatter, type XOR id, missing identity fields) are NOT returned:
//     Scan is a classification API, not a validation API. Callers that
//     need blocking guarantees must run Validate first.
//   - The collection gateway is the shared scan policy: every .md file,
//     plus .json files matching the v2.0 authoring naming contract
//     (`<type-token>-<id>.json`) — foreign/config JSON is never scanned.
//   - An unreadable authoring file aborts the scan with an error, matching
//     Validate: a scan that cannot see every file cannot be trusted.
//   - Scan order is filepath.WalkDir order, which is lexical per directory
//     (deterministic); callers that need canonical ordering sort by
//     identity themselves.
func Scan(root string) ([]Artifact, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("cannot access scan root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan root is not a directory: %s", root)
	}

	paths, err := collectAuthoringPaths(root)
	if err != nil {
		return nil, err
	}

	var artifacts []Artifact
	for _, path := range paths {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", rel, err)
		}
		artifact, _ := analyzeFile(rel, path, data)
		if artifact != nil {
			artifacts = append(artifacts, *artifact)
		}
	}
	return artifacts, nil
}
