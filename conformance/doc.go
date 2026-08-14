// Package conformance implements the official EKA conformance validator:
// the mechanical checklist defined in
// skeleton/docs/exchange/validation.md (Rules R1-R9) extended with the
// Engineering Domain rules R10-R12 of the EKA v1.1 standard, for the
// Engineering Knowledge Architecture (EKA) serialization.
//
// The package is deliberately independent from the CLI layer (cmd/eka) so
// that future tooling can import it directly as
// github.com/maleolabs/eka-core/conformance.
//
// The single entry point is Validate, which scans the <root>/docs
// knowledge tree for authoring files (.md always; .json only when the name
// matches the v2.0 authoring naming contract <type-token>-<id>.json —
// foreign/config JSON is never scanned, spec-standard-v2 §7), classifies
// them as Artifacts or Convention Documents, and applies the twelve
// conformance rules. The result is a deterministic, sorted Report of errors
// and warnings; warnings never block a pass.
//
// Scan scope (ADR-018 Decision 2): the conformance gate scans ONLY the
// <root>/docs knowledge tree when it exists. When docs/ is absent, the
// validation is SKIPPED — a clean PASS with the deterministic skip note —
// because docs-in-repo is legacy v1 authoring, not an obligation. Markdown
// outside docs/ (README.md, skeleton docs, convention docs) is never
// examined; the legacy whole-tree sweep is removed.
//
// Interpretation decisions that go beyond the literal rule text are
// documented in comments at the relevant check and collected in the
// "Interpretation decisions" section of the CLI/package documentation.
package conformance
