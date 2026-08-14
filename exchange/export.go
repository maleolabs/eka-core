package exchange

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
)

// This file implements the export entry point:
//
//	result, err := exchange.Export(root, opts)
//
// Export is the reference implementation of the Exchange §12 pipeline
// (RSF §13.1): validate -> resolve scope -> select units -> detect external
// references -> assemble the object model -> project to the RSF -> compute
// integrity -> emit atomically. It always runs the conformance validation
// gate first: a repository with blocking violations is refused and no
// package is produced (Exchange §12.5).
//
// Error contract (deterministic, "eka: " prefix rendered by the CLI):
//   - *ValidationError: the repository failed the validation gate (exit 1);
//     the caller should render the carried report.
//   - *UsageError: malformed/unknown/missing export targets, or an
//     explicit scope contradicting the targets (exit 2).
//   - *ContentError: the repository content violates the identity charset
//     guard (path-traversal defense, RSF §5.2.3); the export is refused
//     before any write (exit 2 via the same mechanism as UsageError).
//   - any other error: serialization or filesystem failure (exit 2).

// ValidationError reports that the source repository failed the
// conformance validation gate; the export was refused and no package was
// produced (Exchange §12.5). The Report is carried so the CLI can render
// the full findings.
type ValidationError struct {
	Report *conformance.Report
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("export refused: repository validation failed with %d blocking error(s); no package produced",
		e.Report.ErrorCount())
}

// ContentError reports that the source repository carries content the
// exporter refuses to serialize: an identity component (Namespace, Type or
// ID) violating the RSF §5.2.3 charset contract. Such a component would be
// interpolated into package entry paths (units/<ns>/<type>-<id>-v<nn>),
// so it is a path-traversal vector; the export stops before any entry name
// is built or any file is written (the guard runs in the load phase). The
// conformance gate does not catch this (documented Rule 2 gap: the
// filename's id segment is not required to equal the frontmatter id), so
// the exchange layer enforces it itself. Mapped to exit 2 by the CLI's
// existing non-ValidationError path.
type ContentError struct{ msg string }

func (e *ContentError) Error() string { return e.msg }

func contentErrorf(format string, args ...any) error {
	return &ContentError{msg: fmt.Sprintf(format, args...)}
}

// IsUsageError reports whether err is a UsageError (exit code 2).
func IsUsageError(err error) bool {
	var ue *UsageError
	return errors.As(err, &ue)
}

// Export validates root and produces the RSF package selected by opts.
// On success it returns the written result; on failure it returns a typed
// error and never leaves a partial package behind.
func Export(root string, opts Options) (*Result, error) {
	// 1. Validation gate (Exchange §12.5, RSF §13.1 step 1). Warnings are
	// tolerated; blocking violations refuse the export.
	report, err := conformance.Validate(root)
	if err != nil {
		return nil, fmt.Errorf("export failed: validation error: %w", err)
	}
	if !report.Pass() {
		return nil, &ValidationError{Report: report}
	}

	// 2-5. Scope resolution + model construction.
	b, err := build(root, opts)
	if err != nil {
		return nil, err
	}

	// 6-7. RSF projection + integrity.
	entries, err := assemble(b)
	if err != nil {
		return nil, err
	}

	// 8. Emission: single-file container or directory layout.
	output, dir, err := resolveOutput(opts.Output, b.label)
	if err != nil {
		return nil, err
	}
	if dir {
		if err := writeDir(output, entries); err != nil {
			return nil, fmt.Errorf("export failed: %w", err)
		}
	} else {
		if err := writeZIP(output, entries); err != nil {
			return nil, fmt.Errorf("export failed: %w", err)
		}
	}

	pkg := &Package{
		Header: Header{
			SerializationVersion:  SerializationVersion,
			ExchangeFormatVersion: ExchangeFormatVersion,
			SpecificationVersion:  SpecificationVersion,
			Exporter:              Exporter,
			PackageIdentityLabel:  b.label,
			ExportScope:           b.scope,
			Namespace:             b.namespace,
		},
		Units:       b.units,
		Attachments: b.attachments,
		Declarations: Declarations{
			Closure:            ClosureDeclaration{Scope: b.scope, Seeds: b.seeds},
			ExternalReferences: b.externals,
			Extensions:         []ExtensionDecl{},
		},
	}
	// Manifest and Integrity are filled from the serialized projections so
	// the returned model matches the written bytes exactly.
	pkg.Manifest = decodeManifest(entries)
	pkg.Integrity = decodeIntegrity(entries)

	return &Result{
		Label:              b.label,
		Output:             output,
		Directory:          dir,
		Package:            pkg,
		Validation:         report,
		Units:              len(b.units),
		Attachments:        len(b.attachments),
		ExternalReferences: len(b.externals),
	}, nil
}

// resolveOutput maps Options.Output to the concrete destination:
//   - "" -> "<label>.ekapkg" in the current directory (single-file);
//   - an existing directory, or a path ending in a path separator ->
//     directory layout rooted at that path;
//   - anything else -> single-file ZIP at that path (parent directories
//     are created).
func resolveOutput(output, label string) (string, bool, error) {
	if output == "" {
		abs, err := filepath.Abs(label + PackageExtension)
		if err != nil {
			return "", false, fmt.Errorf("cannot resolve output path: %w", err)
		}
		return abs, false, nil
	}
	if strings.HasSuffix(output, string(os.PathSeparator)) || strings.HasSuffix(output, "/") {
		return output, true, nil
	}
	if info, err := os.Stat(output); err == nil && info.IsDir() {
		return output, true, nil
	}
	abs, err := filepath.Abs(output)
	if err != nil {
		return "", false, fmt.Errorf("cannot resolve output path: %w", err)
	}
	return abs, false, nil
}

// jsonUnmarshal is the shared decode helper for re-reading written blocks.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// decodeManifest parses the written manifest.json back into the model.
func decodeManifest(entries []entry) Manifest {
	for _, e := range entries {
		if e.name == "manifest.json" {
			var m Manifest
			if jsonUnmarshal(e.data, &m) == nil {
				return m
			}
		}
	}
	return Manifest{}
}

// decodeIntegrity parses the written integrity.json back into the model.
func decodeIntegrity(entries []entry) Integrity {
	for _, e := range entries {
		if e.name == "integrity.json" {
			var in Integrity
			if jsonUnmarshal(e.data, &in) == nil {
				return in
			}
		}
	}
	return Integrity{}
}
