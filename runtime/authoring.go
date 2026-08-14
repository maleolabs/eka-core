package runtime

import (
	"github.com/maleolabs/eka-core/compile"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/sync"
)

// This file implements the Authoring API: the stateless gateway from
// AUTHORING representations into Canonical Knowledge Objects and
// runtime state. It is Markdown-independent by contract — the Markdown
// adapter lives in conformance/ and is invoked inside Validate and
// Compile — and it never exposes database implementation details: its
// outputs are CKOs (Compile), reports (Validate), runtime state
// (Sync) and the draft lifecycle (draft.go: NewDraft, Publish,
// PublishInline, Drafts, DiscardDraft).
//
// The service is stateless (a zero-size value; use the package-level
// Authoring variable) and synchronous.

// ValidationReport is the outcome of one Validate run (re-exported
// conformance contract type).
type ValidationReport = conformance.Report

// CompileResult is the outcome of one Compile run (re-exported compile
// contract type): the assembled package plus its CKOs.
type CompileResult = compile.Result

// SyncOptions configures one sync run (re-exported sync contract
// type): pull/push sides and the docs-mode seed.
type SyncOptions = sync.Options

// SyncResult is the deterministic outcome of one sync run (re-exported
// sync contract type).
type SyncResult = sync.Report

// ValidationError reports that a repository failed the authoring
// conformance gate (re-exported compile contract type, so
// errors.As(err, &ve) works through the Runtime).
type ValidationError = compile.ValidationError

// AuthoringService is the stateless compiler/validation/sync gateway
// of the Authoring API. It holds no state; use the package-level
// Authoring variable. Concrete and documented — no interface type.
type AuthoringService struct{}

// Validate runs the authoring conformance gate over the repository
// rooted at root and returns the report. Blocking violations are
// reported in the report (Pass() == false); no validation error is
// returned for findings — Validate always returns a report.
func (AuthoringService) Validate(root string) (*ValidationReport, error) {
	return conformance.Validate(root)
}

// Compile compiles the authoring tree at root into Canonical Knowledge
// Objects — the conformance gate, then the package assembled exactly
// as a repository-scope export would. A repository that fails the
// gate is refused with *ValidationError (the caller renders the
// report); build/assembly failures are wrapped with "compile: "
// context. The compiler never writes to disk.
func (AuthoringService) Compile(root string) (*CompileResult, error) {
	return compile.Compile(root)
}

// Sync runs one synchronization of the repository at repoPath against
// the Runtime: resolve and (auto-)register the repository, then pull
// and/or push per opts (the sync engine over the Runtime's workspace).
// Errors keep their typed classes: *ValidationError (docs gate) and
// *exchange.PackageError (corrupt snapshot) map to the validation and
// integrity failure classes; workspace/registry/usage failures are
// plain wrapped errors.
func (AuthoringService) Sync(rt *Runtime, repoPath string, opts SyncOptions) (*SyncResult, error) {
	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	return sync.Run(ws, repoPath, opts)
}

// Authoring is the package-level Authoring API: the stateless service
// variable of the Authoring API.
var Authoring AuthoringService
