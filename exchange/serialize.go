package exchange

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// This file implements the RSF projection: the deterministic byte entries
// of the package (RSF §13.1 steps 6-7). The concrete v1 projection is the
// reference implementation choice documented in the package doc:
//
//	header.json          Package Header (RSF §4.3)
//	manifest.json        Manifest (RSF §8)
//	units/<ns>/<type>-<id>-v<nn>/unit.json   Unit Entry metadata (RSF §5.1)
//	units/<ns>/<type>-<id>-v<nn>/content     Content payload (RSF §6)
//	attachments/<relative-path>              Attachments Collection (RSF §7)
//	declarations.json    Declarations Block (RSF §4.4, Exchange §10.4)
//	integrity.json       Integrity Block (RSF §9.4, Exchange §17.1)
//
// Encoding conventions (RSF §9.3): UTF-8, no BOM, LF; JSON structs with
// fixed declared field order (no maps in output); no timestamps anywhere
// in v1; no absolute paths; no host names.
//
// Deliberate RSF deviations (consolidated; each is a documented v1
// implementation decision, not an accident):
//
//  1. The Package Header carries no creation timestamp. RSF §4.3 defines
//     the creation timestamp as package metadata that may differ between
//     exports (Exchange §15.4); v1 omits it entirely for byte-determinism
//     (two exports of identical repository state are byte-identical).
//  2. The Header does not announce the presence of the Integrity and
//     Declarations blocks (RSF §4.3 permits announcing them). They are
//     content-discoverable instead: integrity.json and declarations.json
//     are always present in v1 exports and self-describing.
//  3. Content payloads are carried byte-exact, without LF normalization
//     (RSF §9.3 normalizes line endings for encoded text). The declared
//     canonicalization of the eka/structured-text/1 representation IS the
//     byte-exact payload (RSF §6.3.3): content equality is decided on the
//     verbatim bytes, keeping the round-trip lossless (frontmatter fields
//     + body reconstruct the source file, load.go).
//  4. Attachment IDs are repository-relative paths with forward slashes.
//     RSF §7.2 recommends an ID rule; the repo-relative path is
//     deterministic and unique within the package (policy in load.go), so
//     the section's intent is met without copying its recommended rule
//     verbatim.
//  5. The package digest (RSF §9.4) covers every entry except
//     manifest.json and integrity.json. The manifest echoes the package
//     digest (RSF §8.1 manifest responsibility); a digest that covered
//     the manifest bytes would be self-referential (the echoed digest
//     value would be part of its own input), so the manifest is excluded.
//     integrity.json remains the authoritative integrity block; per-unit
//     and per-attachment digests are unaffected.
//
// Digests (RSF §9.4): per-unit SHA-256 over the unit's canonical
// serialization (unit.json bytes || content bytes, in that order);
// per-attachment SHA-256 over the raw payload; package digest SHA-256 over
// every other entry's bytes in sorted entry name order, excluding
// manifest.json and integrity.json themselves (deviation 5).

// entry is one deterministic package entry: a logical file in the package.
type entry struct {
	name string
	data []byte
}

// UnitDirName builds the unit entry directory
// "units/<namespace>/<type>-<id>-v<nn>". Instance-versions are integers,
// so v<nn> is canonical (never zero-padded). IDs are carried verbatim;
// the load-phase charset guard (load.go) enforces RSF §5.2.3 on every
// identity component before this builder runs, so the interpolated
// segments can never escape the package root (path-traversal defense).
// Exported for consumers that need the canonical entry location of a
// unit identity (the sync engine of the Knowledge Runtime resolves raw
// unit.json entries by it).
func UnitDirName(id Identity) string {
	return "units/" + id.Namespace + "/" + id.Type + "-" + id.ID + "-v" + strconv.Itoa(id.InstanceVersion)
}

// assemble projects the built model onto the deterministic entry set,
// computing all digests. The returned entries are sorted by name.
func assemble(b *built) ([]entry, error) {
	units := make(map[string][]byte, len(b.units))
	unitDigests := make(map[string]string, len(b.units))
	manifestUnits := make([]ManifestUnit, 0, len(b.units))

	for _, u := range b.units {
		u.UnitDir = UnitDirName(u.Identity)
		unitJSON, err := marshal(u)
		if err != nil {
			return nil, fmt.Errorf("serialization failed for %s: %w", u.CanonicalIdentityForm, err)
		}
		units[u.UnitDir+"/unit.json"] = unitJSON
		units[u.UnitDir+"/content"] = u.ContentPayload

		// Per-unit digest over the canonical unit serialization:
		// unit.json bytes || content bytes (RSF §9.4).
		digest := sha256.Sum256(append(unitJSON, u.ContentPayload...))
		u.Digest = hex.EncodeToString(digest[:])
		unitDigests[u.CanonicalIdentityForm] = u.Digest

		manifestUnits = append(manifestUnits, ManifestUnit{
			CanonicalIdentityForm: u.CanonicalIdentityForm,
			Type:                  u.Identity.Type,
			ID:                    u.Identity.ID,
			Namespace:             u.Identity.Namespace,
			InstanceVersion:       u.Identity.InstanceVersion,
			Revision:              u.Revision,
			ContentRepresentation: u.Content.Representation,
			ContentFile:           u.UnitDir + "/content",
			UnitDigest:            u.Digest,
		})
	}
	// Manifest units are already in canonical identity order (built.units
	// is sorted); sort defensively to make the contract explicit.
	sort.Slice(manifestUnits, func(i, j int) bool {
		return manifestUnits[i].CanonicalIdentityForm < manifestUnits[j].CanonicalIdentityForm
	})

	// Attachments: raw payloads + digests (RSF §7.5).
	attachmentEntries := make(map[string][]byte, len(b.attachments))
	attachmentDigests := make([]AttachmentDigest, 0, len(b.attachments))
	for _, a := range b.attachments {
		sum := sha256.Sum256(a.Data)
		a.Digest = hex.EncodeToString(sum[:])
		attachmentEntries["attachments/"+a.ID] = a.Data
		attachmentDigests = append(attachmentDigests, AttachmentDigest{ID: a.ID, Digest: a.Digest})
	}
	sort.Slice(attachmentDigests, func(i, j int) bool {
		return attachmentDigests[i].ID < attachmentDigests[j].ID
	})

	manifest := Manifest{
		Scope:                 b.scope,
		PackageIdentityLabel:  b.label,
		SerializationVersion:  SerializationVersion,
		ExchangeFormatVersion: ExchangeFormatVersion,
		SpecificationVersion:  SpecificationVersion,
		Units:                 manifestUnits,
		Counts: Counts{
			Units:              len(manifestUnits),
			Attachments:        len(b.attachments),
			ExternalReferences: len(b.externals),
			Extensions:         0, // v1 exports carry no extensions.
		},
		Closure: ClosureDeclaration{Scope: b.scope, Seeds: b.seeds},
	}
	header := Header{
		SerializationVersion:  SerializationVersion,
		ExchangeFormatVersion: ExchangeFormatVersion,
		SpecificationVersion:  SpecificationVersion,
		Exporter:              Exporter,
		PackageIdentityLabel:  b.label,
		ExportScope:           b.scope,
		Namespace:             b.namespace,
	}
	declarations := Declarations{
		Closure:            manifest.Closure,
		ExternalReferences: b.externals,
		Extensions:         []ExtensionDecl{},
	}

	all := map[string][]byte{}
	for name, data := range units {
		all[name] = data
	}
	for name, data := range attachmentEntries {
		all[name] = data
	}

	// Header and Declarations are serialized before the digest computation
	// (both are part of the package digest input). JSON is emitted with a
	// trailing LF (normalized encoding, RSF §9.3).
	var err error
	all["header.json"], err = marshalLF(header)
	if err != nil {
		return nil, fmt.Errorf("serialization failed for header: %w", err)
	}
	all["declarations.json"], err = marshalLF(declarations)
	if err != nil {
		return nil, fmt.Errorf("serialization failed for declarations: %w", err)
	}

	// Package digest over every entry in sorted name order, excluding
	// manifest.json and integrity.json themselves (deviation 5 above: the
	// manifest echoes this digest, so the manifest bytes must not be part
	// of its own input).
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	hasher := sha256.New()
	for _, name := range names {
		hasher.Write(all[name])
	}
	packageDigest := hex.EncodeToString(hasher.Sum(nil))

	// Manifest and Integrity are serialized last so the counts, per-unit
	// digests and the package digest above are final. The manifest echoes
	// the package digest (RSF §8.1); integrity.json is the authoritative
	// integrity block.
	manifest.PackageDigest = packageDigest
	all["manifest.json"], err = marshalLF(manifest)
	if err != nil {
		return nil, fmt.Errorf("serialization failed for manifest: %w", err)
	}

	integrity := Integrity{
		PackageDigest: packageDigest,
		Units:         make([]UnitDigest, 0, len(unitDigests)),
		Attachments:   attachmentDigests,
	}
	forms := make([]string, 0, len(unitDigests))
	for form := range unitDigests {
		forms = append(forms, form)
	}
	sort.Strings(forms)
	for _, form := range forms {
		integrity.Units = append(integrity.Units, UnitDigest{CanonicalIdentityForm: form, Digest: unitDigests[form]})
	}

	all["integrity.json"], err = marshalLF(integrity)
	if err != nil {
		return nil, fmt.Errorf("serialization failed for integrity: %w", err)
	}

	// Deterministic final ordering: entries sorted by name.
	names = make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]entry, 0, len(names))
	for _, name := range names {
		out = append(out, entry{name: name, data: all[name]})
	}
	return out, nil
}

// marshal renders one model element as canonical JSON: compact (no
// indent), fixed field order, LF line ending, UTF-8 without BOM.
func marshal(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// marshalLF renders one top-level JSON block with a trailing newline.
func marshalLF(v any) ([]byte, error) {
	data, err := marshal(v)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
