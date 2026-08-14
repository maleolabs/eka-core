// Package compile implements the Knowledge Compiler: the canonical
// gateway from AUTHORING representations into Canonical Knowledge
// Objects (CKO). It is the named gateway the docs-mode pull pipeline
// and the projection engine both use, replacing the split
// conformance.Validate + exchange.RepositoryPackage call sequence.
//
// Pipeline (deterministic):
//
//	read authoring tree -> parse -> validate syntax (authoring
//	conformance R0-R12) -> normalize -> generate CKO -> integrity
//	verification (package digest) -> Result
//
// Persistence is the caller's concern: the sync engine persists the
// compiled CKOs into the workspace canonical store; the projection
// engine consumes them in memory. The compiler itself never writes to
// disk.
//
// Markdown awareness lives in the authoring adapter (the conformance
// package); this package is representation-independent by
// construction — it consumes whatever the adapter produces and never
// touches authoring syntax itself.
//
// Error classes: a repository that fails the authoring conformance
// gate is refused with *ValidationError (the caller renders the
// report); build/assembly failures are wrapped with "compile: "
// context.
package compile

import (
	"fmt"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// Result is the outcome of one successful Compile run.
type Result struct {
	// Package is the assembled RSF package model of the repository:
	// units with Digest and ContentPayload filled, attachments,
	// declarations and the Integrity Block. Never written to disk by
	// the compiler.
	Package *exchange.Package
	// CKOs are the compiled Canonical Knowledge Objects — exactly
	// Package.Units (convenience alias: the CKO is the exchange Unit
	// of the Knowledge Runtime store). Deterministically ordered.
	CKOs []*exchange.Unit
	// Validation is the report of the authoring conformance gate
	// (always passing when Compile succeeds).
	Validation *conformance.Report
}

// ValidationError reports that the repository failed the authoring
// conformance gate; the compile was refused (validation failure class,
// exit 1). The Report is carried so the caller can render the
// findings. Deterministic message: names the blocking error count.
type ValidationError struct {
	Report *conformance.Report
}

// Error renders the deterministic refusal message.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("knowledge compile refused: repository validation failed with %d blocking error(s); no knowledge seeded",
		e.Report.ErrorCount())
}

// Compile compiles the authoring tree at root into Canonical Knowledge
// Objects. The pipeline is deterministic:
//
//  1. conformance gate (authoring conformance R0-R12): blocking errors
//     refuse the compile with *ValidationError — the caller renders
//     the report;
//  2. exchange.RepositoryPackage(root) assembles the package exactly
//     as a repository-scope export would (same load + build path, same
//     unit mapping, Manifest/Integrity from the same serialized
//     projections) — the migration path and the projection engine
//     therefore always see byte-identical CKOs;
//  3. integrity sanity: the assembled package must carry a non-empty
//     PackageDigest;
//  4. the Result.
//
// The compiler never writes to disk. RepositoryPackage already orders
// everything canonically, so two compiles of identical repository
// state produce identical digests and identical CKO slices.
func Compile(root string) (*Result, error) {
	report, err := conformance.Validate(root)
	if err != nil {
		return nil, fmt.Errorf("compile: validation error: %w", err)
	}
	if !report.Pass() {
		return nil, &ValidationError{Report: report}
	}

	pkg, err := exchange.RepositoryPackage(root)
	if err != nil {
		return nil, fmt.Errorf("compile: cannot build repository package: %w", err)
	}
	if pkg.Integrity.PackageDigest == "" {
		return nil, fmt.Errorf("compile: package integrity verification failed: assembled package carries no package digest")
	}

	return &Result{Package: pkg, CKOs: pkg.Units, Validation: report}, nil
}
