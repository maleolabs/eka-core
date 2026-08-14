package exchange

import (
	"fmt"
)

// This file implements the public package emission wrapper:
//
//	files, err := exchange.Emit(pkg)
//
// Emit projects a fully assembled Package model onto the deterministic
// RSF entry set (header.json, manifest.json, units/…, attachments/…,
// declarations.json, integrity.json) — the same byte projection the
// serializer produces for Export — without touching the filesystem.
// It is the emission side of the EKA Knowledge Runtime snapshot
// writer: the sync engine assembles the Package from its canonical
// store and hands it to Emit, then writes the returned files itself.
//
// Emit reuses the unexported serializer (assemble) unchanged: same
// deterministic bytes, same digest computation. Do NOT modify
// serialize.go; Emit only adapts the model to the serializer's input
// shape.

// EmittedFile is one deterministic package entry: a logical file with
// its byte-exact content.
type EmittedFile struct {
	// Name is the entry name ("header.json", "units/<ns>/<type>-<id>-v<n>/unit.json", …).
	Name string
	// Data is the entry bytes.
	Data []byte
}

// MarshalUnit renders one unit's canonical unit.json bytes: the exact
// bytes the serializer writes to <unit-dir>/unit.json (compact JSON,
// fixed declared field order, no trailing LF). It is the public
// serialization counterpart of DecodeUnit (decode.go) — the migration
// path reconstructs units from the v1 store and persists their
// canonical bytes, and the docs-mode pull serializes units assembled
// from the docs tree into the same bytes their package digest covers.
// Deterministic: identical units marshal to identical bytes.
func MarshalUnit(u *Unit) ([]byte, error) {
	return marshal(u)
}

// Emit projects pkg onto the deterministic RSF entry set. The package
// must carry a complete Header (ExportScope, Namespace,
// PackageIdentityLabel), Declarations, Units (ContentPayload filled)
// and Attachments. The returned entries are sorted by name; two Emits
// of identical packages produce byte-identical entries.
func Emit(pkg *Package) ([]EmittedFile, error) {
	// The serializer requires the closure seeds and the external
	// reference list as non-nil slices (JSON encodes [] not null);
	// normalize defensively.
	seeds := pkg.Declarations.Closure.Seeds
	if seeds == nil {
		seeds = []string{}
	}
	externals := pkg.Declarations.ExternalReferences
	if externals == nil {
		externals = []ExternalReference{}
	}

	b := &built{
		scope:       pkg.Header.ExportScope,
		seeds:       seeds,
		namespace:   pkg.Header.Namespace,
		label:       pkg.Header.PackageIdentityLabel,
		units:       pkg.Units,
		attachments: pkg.Attachments,
		externals:   externals,
	}
	entries, err := assemble(b)
	if err != nil {
		return nil, fmt.Errorf("emission failed: %w", err)
	}
	out := make([]EmittedFile, 0, len(entries))
	for _, e := range entries {
		out = append(out, EmittedFile{Name: e.name, Data: e.data})
	}
	return out, nil
}
