package exchange

import (
	"fmt"
)

// This file implements the repository package builder:
//
//	pkg, err := exchange.RepositoryPackage(root)
//
// RepositoryPackage assembles the full RSF package model of the
// repository at root exactly as a repository-scope export would — the
// same load + build path, the same unit mapping, the same external
// reference detection, and a Manifest/Integrity filled from the same
// serialized projections — without running the conformance validation
// gate (the caller decides its gate policy) and without writing
// anything to disk.
//
// It exists for the EKA Knowledge Runtime migration mode: a repository
// without a snapshot is pulled from its docs tree, and the package
// assembled here must be byte-identical to a normal `eka export` of
// the same state so snapshot digests agree between the two paths.
//
// Callers that need the validation gate must run conformance.Validate
// themselves first (like sync/pull.go does); RepositoryPackage is a
// pure model construction API.

// RepositoryPackage assembles the RSF package model of root in
// Repository scope (all instances of all lines). The returned package
// mirrors an export byte-for-byte: Manifest and Integrity are decoded
// from the same serialized entries the writer would produce.
func RepositoryPackage(root string) (*Package, error) {
	b, err := build(root, Options{})
	if err != nil {
		return nil, err
	}
	entries, err := assemble(b)
	if err != nil {
		return nil, fmt.Errorf("package assembly failed: %w", err)
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
	pkg.Manifest = decodeManifest(entries)
	pkg.Integrity = decodeIntegrity(entries)
	return pkg, nil
}
