package exchange

// This file implements the public package-loading wrapper:
//
//	pkg, err := exchange.LoadPackage(path)
//
// It is the read-side counterpart of Export/Emit for consumers outside
// the package (the sync engine of the EKA Knowledge Runtime): the full
// RSF -> model deserialization pipeline of deserialize.go — entry
// structure, strict JSON decoding (unknown fields rejected, RSF §9.5),
// SHA-256 integrity (package, per-unit, per-attachment digests) and
// manifest self-consistency (Exchange §10.6). Any failure is a
// *PackageError (rejected package class).
//
// The returned Package carries every logical element: Header,
// Manifest, Units (with ContentPayload and Digest filled), Attachments
// (with Data and Digest), Declarations and Integrity.

// LoadPackage opens and fully validates the package at path (a
// single-file .ekapkg ZIP container or the equivalent directory
// layout) and returns the deserialized RSF object model. The package
// is verified byte-exact: structure, strict decode, integrity digests
// and self-consistency (deserialize.go). Every returned unit carries
// its ContentPayload and Digest; every attachment its Data and Digest.
func LoadPackage(path string) (*Package, error) {
	pkg, _, err := LoadPackageWithEntries(path)
	return pkg, err
}

// LoadPackageWithEntries is LoadPackage plus the raw entry map: the
// package is verified exactly as LoadPackage verifies it, and the
// caller additionally receives every package entry's raw bytes keyed
// by logical entry name (from the underlying reader). It exists for
// consumers that need the byte-exact entries alongside the model — the
// sync engine of the Knowledge Runtime persists each unit's canonical
// unit.json bytes verbatim, so the stored payload hashes agree with
// the package digests (RSF §9.4).
func LoadPackageWithEntries(path string) (*Package, map[string][]byte, error) {
	lp, err := loadPackage(path)
	if err != nil {
		return nil, nil, err
	}

	// Fill the per-unit digests from the manifest (the deserializer
	// verifies them but does not carry them back onto the units) and
	// the per-attachment digests from the integrity block.
	unitDigests := map[string]string{}
	for _, mu := range lp.manifest.Units {
		unitDigests[mu.CanonicalIdentityForm] = mu.UnitDigest
	}
	for _, u := range lp.units {
		u.Digest = unitDigests[u.CanonicalIdentityForm]
	}
	attDigests := map[string]string{}
	for _, ad := range lp.integrity.Attachments {
		attDigests[ad.ID] = ad.Digest
	}
	for _, a := range lp.attachments {
		a.Digest = attDigests[a.ID]
	}

	return loadedPackageModel(lp), rawEntries(lp.reader), nil
}

// loadedPackageModel projects a deserialized loadedPackage onto the
// public Package model (shared by LoadPackage and
// LoadPackageWithEntries).
func loadedPackageModel(lp *loadedPackage) *Package {
	return &Package{
		Header:       lp.header,
		Manifest:     lp.manifest,
		Units:        lp.units,
		Attachments:  lp.attachments,
		Declarations: lp.declarations,
		Integrity:    lp.integrity,
	}
}

// rawEntries copies the reader's entry map (name -> raw bytes).
func rawEntries(r *PackageReader) map[string][]byte {
	entries := make(map[string][]byte, len(r.entries))
	for name, data := range r.entries {
		entries[name] = data
	}
	return entries
}

// LoadPackageError reports whether err is a *PackageError (a rejected
// or malformed package).
func LoadPackageError(err error) bool {
	_, ok := err.(*PackageError)
	return ok
}
