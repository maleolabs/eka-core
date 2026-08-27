package exchange

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
)

// This file implements the RSF -> exchange model deserialization: the
// inverse of serialize.go, byte-exact. loadPackage performs, in order:
//
//  1. entry-level structural checks: the six logical element locations
//     exist, every entry maps to a logical element (unknown entries
//     rejected, RSF §9.5 reject-by-default), unit entries parse into
//     identities and map back to manifest entries (RSF §8.2
//     self-consistency);
//  2. strict JSON decoding of every block (DisallowUnknownFields: a field
//     unknown to a v1 implementer is a forward-compatibility violation and
//     is rejected explicitly — RSF §9.5);
//  3. integrity verification (RSF §9.4, Exchange §17.1): package digest
//     recomputed over every entry except manifest.json and integrity.json
//     (serialize.go deviation 5), per-unit digest over unit.json ||
//     content, per-attachment digest over the raw payload;
//  4. self-consistency: header ↔ manifest version echoes, label echoes,
//     counts echoes, manifest ↔ units 1:1 correspondence (Exchange §10.6).
//
// Any failure here rejects the package before the Exchange §11.1 phases
// run (RSF §13.2 step 1; Exchange §17.1). All failures are *PackageError
// (exit code 2: package invalid/malformed).

// loadedPackage is the deserialized package model of one import run.
type loadedPackage struct {
	header       Header
	manifest     Manifest
	declarations Declarations
	integrity    Integrity
	units        []*Unit          // sorted by canonical identity key
	attachments  []*Attachment    // sorted by attachment ID
	unitByForm   map[string]*Unit // canonical identity form -> unit
	attByID      map[string]*Attachment
	reader       *PackageReader
}

// loadPackage opens and fully validates the package: structure, strict
// decode, integrity, self-consistency. On success every unit carries its
// ContentPayload and Digest; every attachment carries its Data.
//
// Legacy support (spec-standard-v2 §4): the v2.0 importer expects the
// camelCase RSF v2.0 keys. Packages declaring Serialization Version
// "1.1" (or the pre-1.1 line "1") carry the legacy kebab/snake keys and
// are normalized by a key-rename pass before the strict decode — one
// decode path, two key spellings.
func loadPackage(path string) (*loadedPackage, error) {
	r, err := OpenPackage(path)
	if err != nil {
		return nil, &PackageError{msg: err.Error()}
	}
	pkg := &loadedPackage{
		unitByForm: map[string]*Unit{},
		attByID:    map[string]*Attachment{},
		reader:     r,
	}

	// 1. Entry-level structure: every entry maps to a logical element.
	for _, name := range r.sortedEntries() {
		if !isKnownEntry(name) {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s contains unknown entry %q (reject-by-default: a field or element unknown to a v1 importer is a forward-compatibility violation)", r.Path(), name)}
		}
	}

	// 1b. Serialization version sniff + legacy key normalization: the
	// header is decoded leniently to read the declared version (the
	// v1.1 header carries `serialization_version`; the v2.0 header
	// carries `serializationVersion`), and legacy packages get their
	// entries rewritten to the v2.0 keys before any strict decode.
	// The rewrite operates on the raw bytes only — digest verification
	// below recomputes over the package's own bytes, so legacy digests
	// stay authoritative.
	headerData, ok := r.Entry("header.json")
	if !ok {
		return nil, &PackageError{msg: fmt.Sprintf("package %s is missing the required header.json entry", r.Path())}
	}
	legacy := isLegacyHeader(headerData)
	entries := r
	if legacy {
		entries, err = legacyKeyRename(r)
		if err != nil {
			return nil, &PackageError{msg: err.Error()}
		}
		headerData, _ = entries.Entry("header.json")
	}

	// 2. Strict decode of the four top-level blocks.
	if err := strictDecode("header.json", headerData, &pkg.header); err != nil {
		return nil, &PackageError{msg: err.Error()}
	}
	manifestData, ok := entries.Entry("manifest.json")
	if !ok {
		return nil, &PackageError{msg: fmt.Sprintf("package %s is missing the required manifest.json entry", r.Path())}
	}
	if err := strictDecode("manifest.json", manifestData, &pkg.manifest); err != nil {
		return nil, &PackageError{msg: err.Error()}
	}
	declData, ok := entries.Entry("declarations.json")
	if !ok {
		return nil, &PackageError{msg: fmt.Sprintf("package %s is missing the required declarations.json entry", r.Path())}
	}
	if err := strictDecode("declarations.json", declData, &pkg.declarations); err != nil {
		return nil, &PackageError{msg: err.Error()}
	}
	integrityData, ok := entries.Entry("integrity.json")
	if !ok {
		return nil, &PackageError{msg: fmt.Sprintf("package %s is missing the required integrity.json entry", r.Path())}
	}
	if err := strictDecode("integrity.json", integrityData, &pkg.integrity); err != nil {
		return nil, &PackageError{msg: err.Error()}
	}

	// 3. Map unit entry trees to Unit models; validate path/identity
	// consistency (a unit's entry directory must match its identity).
	unitDirs := map[string]bool{} // "units/<ns>/<type>-<id>-v<nn>" -> seen
	for _, name := range r.sortedEntries() {
		if !strings.HasPrefix(name, "units/") {
			continue
		}
		dir, _, ok := splitUnitEntry(name)
		if !ok {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s contains malformed unit entry %q (expected <unit-dir>/unit.json or <unit-dir>/content)", r.Path(), name)}
		}
		unitDirs[dir] = true
	}
	// Manifest <-> units 1:1 (Exchange §10.6): every manifest entry names
	// a unique unit form whose entry directory exists, and every package
	// unit directory is referenced by exactly one manifest entry.
	seenForms := map[string]bool{}
	dirCounts := map[string]int{}
	for _, mu := range pkg.manifest.Units {
		if seenForms[mu.CanonicalIdentityForm] {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s is not self-consistent: the manifest lists unit form %s more than once (Exchange §10.6)",
				r.Path(), mu.CanonicalIdentityForm)}
		}
		seenForms[mu.CanonicalIdentityForm] = true
		dir := UnitDirName(Identity{Namespace: mu.Namespace, Type: mu.Type, ID: mu.ID, InstanceVersion: mu.InstanceVersion})
		dirCounts[dir]++
		if !unitDirs[dir] {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s is not self-consistent: manifest entry %s has no unit entry directory %s (Exchange §10.6)",
				r.Path(), mu.CanonicalIdentityForm, dir)}
		}
	}
	for dir := range unitDirs {
		if n := dirCounts[dir]; n != 1 {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s is not self-consistent: unit entry directory %s is referenced %d time(s) by the manifest (Exchange §10.6 requires exactly one)",
				r.Path(), dir, n)}
		}
	}
	for _, mu := range pkg.manifest.Units {
		dir := UnitDirName(Identity{Namespace: mu.Namespace, Type: mu.Type, ID: mu.ID, InstanceVersion: mu.InstanceVersion})
		unitJSON, ok := entries.Entry(dir + "/unit.json")
		if !ok {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s: unit %s is missing its unit.json entry", r.Path(), mu.CanonicalIdentityForm)}
		}
		var u Unit
		if err := strictDecode(dir+"/unit.json", unitJSON, &u); err != nil {
			return nil, &PackageError{msg: err.Error()}
		}
		content, ok := r.Entry(dir + "/content")
		if !ok {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s: unit %s is missing its content entry", r.Path(), mu.CanonicalIdentityForm)}
		}
		if err := validateUnitEntryConsistency(dir, mu, &u); err != nil {
			return nil, &PackageError{msg: err.Error()}
		}
		u.UnitDir = dir
		u.ContentPayload = content
		pkg.units = append(pkg.units, &u)
		pkg.unitByForm[u.CanonicalIdentityForm] = &u
	}
	// Units sorted by canonical identity key (RSF §6.3).
	sort.Slice(pkg.units, func(i, j int) bool {
		return pkg.units[i].CanonicalIdentityForm < pkg.units[j].CanonicalIdentityForm
	})

	// 4. Attachment entries -> model (sorted by ID).
	for _, name := range r.sortedEntries() {
		if !strings.HasPrefix(name, "attachments/") {
			continue
		}
		id := strings.TrimPrefix(name, "attachments/")
		data, _ := r.Entry(name)
		pkg.attachments = append(pkg.attachments, &Attachment{ID: id, Data: data})
		pkg.attByID[id] = &Attachment{ID: id, Data: data}
	}
	sort.Slice(pkg.attachments, func(i, j int) bool {
		return pkg.attachments[i].ID < pkg.attachments[j].ID
	})

	// 5. Integrity verification (before any validation phase; Exchange
	// §17.1). The package digest covers every entry except manifest.json
	// and integrity.json themselves (serialize.go deviation 5).
	if err := pkg.verifyPackageDigest(r); err != nil {
		return nil, err
	}
	if err := pkg.verifyUnitDigests(r); err != nil {
		return nil, err
	}
	if err := pkg.verifyAttachmentDigests(); err != nil {
		return nil, err
	}

	// 6. Self-consistency: version echoes, label echoes, counts echoes.
	if err := pkg.verifySelfConsistency(); err != nil {
		return nil, err
	}
	return pkg, nil
}

// strictDecode decodes one package JSON block with the reject-by-default
// unknown-field policy (RSF §9.5). JSON is emitted with a trailing LF by
// the exporter (normalized encoding, RSF §9.3), which json.Decoder
// tolerates.
func strictDecode(entry string, data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("package entry %s is not valid RSF JSON: %v", entry, err)
	}
	return nil
}

// splitUnitEntry splits a units/ entry name into its unit directory and
// element kind ("unit.json" or "content").
func splitUnitEntry(name string) (dir, kind string, ok bool) {
	rest := strings.TrimPrefix(name, "units/")
	slash := strings.LastIndex(rest, "/")
	if slash < 0 {
		return "", "", false
	}
	dir = "units/" + rest[:slash]
	kind = rest[slash+1:]
	if kind != "unit.json" && kind != "content" {
		return "", "", false
	}
	return dir, kind, true
}

// validateUnitEntryConsistency verifies that the manifest entry, the entry
// directory and the unit's own identity agree (Exchange §10.6, R2:
// identity is never encoded in location; a disagreement means a corrupt or
// malicious package).
func validateUnitEntryConsistency(dir string, mu ManifestUnit, u *Unit) error {
	// unit.json identity fields must agree with the manifest entry.
	if u.Identity.Namespace != mu.Namespace || u.Identity.Type != mu.Type ||
		u.Identity.ID != mu.ID || u.Identity.InstanceVersion != mu.InstanceVersion {
		return &PackageError{msg: fmt.Sprintf(
			"package is not self-consistent: unit.json identity (%s) does not match its manifest entry (%s)",
			u.Identity.CanonicalForm(), mu.CanonicalIdentityForm)}
	}
	wantDir := UnitDirName(u.Identity)
	if dir != wantDir {
		return &PackageError{msg: fmt.Sprintf(
			"package is not self-consistent: unit %s lives at entry directory %s, expected %s",
			u.CanonicalIdentityForm, dir, wantDir)}
	}
	if u.CanonicalIdentityForm != u.Identity.CanonicalForm() {
		return &PackageError{msg: fmt.Sprintf(
			"package unit %s: canonical_identity_form %q does not match the identity tuple %s",
			dir, u.CanonicalIdentityForm, u.Identity.CanonicalForm())}
	}
	if u.Content.Representation == "" || u.Content.File == "" {
		return &PackageError{msg: fmt.Sprintf(
			"package unit %s: content reference is incomplete (representation %q, file %q)",
			u.CanonicalIdentityForm, u.Content.Representation, u.Content.File)}
	}
	if u.Content.File != "content" {
		return &PackageError{msg: fmt.Sprintf(
			"package unit %s: content file %q is not the v1 layout (expected \"content\")",
			u.CanonicalIdentityForm, u.Content.File)}
	}
	if u.Content.Representation != ContentRepresentation && u.Content.Representation != StructuredJSON {
		return &PackageError{msg: fmt.Sprintf(
			"package unit %s: content representation %q is not implemented by this importer (v2.0 implements %q and the legacy %q); a non-canonical representation must be declared as an extension, and this importer implements no extensions",
			u.CanonicalIdentityForm, u.Content.Representation, StructuredJSON, ContentRepresentation)}
	}
	if mu.ContentFile != dir+"/content" {
		return &PackageError{msg: fmt.Sprintf(
			"package is not self-consistent: manifest content_file %q does not match the unit entry layout %s",
			mu.ContentFile, dir+"/content")}
	}
	return nil
}

// verifyPackageDigest recomputes the package-level SHA-256 over every entry
// except manifest.json and integrity.json (sorted entry order) and compares
// it with the recorded digests.
func (p *loadedPackage) verifyPackageDigest(r *PackageReader) error {
	var buf bytes.Buffer
	for _, name := range r.sortedEntries() {
		if name == "manifest.json" || name == "integrity.json" {
			continue
		}
		data, _ := r.Entry(name)
		buf.Write(data)
	}
	sum := sha256.Sum256(buf.Bytes())
	got := hex.EncodeToString(sum[:])
	if p.integrity.PackageDigest == "" {
		return &PackageError{msg: fmt.Sprintf("package %s: integrity.json carries no package_digest", r.Path())}
	}
	if got != p.integrity.PackageDigest {
		return &PackageError{msg: fmt.Sprintf(
			"package %s integrity verification failed: package digest mismatch (recorded %s, recomputed %s); the package has been tampered with or corrupted",
			r.Path(), p.integrity.PackageDigest, got)}
	}
	if p.manifest.PackageDigest != p.integrity.PackageDigest {
		return &PackageError{msg: fmt.Sprintf(
			"package %s is not self-consistent: manifest package_digest %s does not equal the integrity block digest %s",
			r.Path(), p.manifest.PackageDigest, p.integrity.PackageDigest)}
	}
	return nil
}

// verifyUnitDigests recomputes every per-unit digest (unit.json ||
// content, RSF §9.4) and compares it with the manifest and integrity
// records.
func (p *loadedPackage) verifyUnitDigests(r *PackageReader) error {
	integrityByForm := map[string]string{}
	for _, ud := range p.integrity.Units {
		integrityByForm[ud.CanonicalIdentityForm] = ud.Digest
	}
	for _, u := range p.units {
		unitJSON, _ := r.Entry(u.UnitDir + "/unit.json")
		var buf bytes.Buffer
		buf.Write(unitJSON)
		buf.Write(u.ContentPayload)
		sum := sha256.Sum256(buf.Bytes())
		got := hex.EncodeToString(sum[:])
		if mu, ok := p.manifestUnit(u.CanonicalIdentityForm); ok && mu.UnitDigest != got {
			return &PackageError{msg: fmt.Sprintf(
				"package integrity verification failed: manifest digest of unit %s is %s, recomputed %s",
				u.CanonicalIdentityForm, mu.UnitDigest, got)}
		}
		if want, ok := integrityByForm[u.CanonicalIdentityForm]; ok && want != got {
			return &PackageError{msg: fmt.Sprintf(
				"package integrity verification failed: integrity digest of unit %s is %s, recomputed %s",
				u.CanonicalIdentityForm, want, got)}
		}
	}
	// Every integrity digest must have a unit (1:1 with the manifest).
	if len(integrityByForm) != len(p.units) {
		return &PackageError{msg: fmt.Sprintf(
			"package is not self-consistent: integrity block lists %d unit digests but the package carries %d unit(s)",
			len(integrityByForm), len(p.units))}
	}
	return nil
}

// verifyAttachmentDigests recomputes every per-attachment digest and
// compares it with the integrity records; every integrity record must have
// an attachment entry and vice versa.
func (p *loadedPackage) verifyAttachmentDigests() error {
	integrityByID := map[string]string{}
	for _, ad := range p.integrity.Attachments {
		integrityByID[ad.ID] = ad.Digest
	}
	for _, a := range p.attachments {
		sum := sha256.Sum256(a.Data)
		got := hex.EncodeToString(sum[:])
		if want, ok := integrityByID[a.ID]; ok && want != got {
			return &PackageError{msg: fmt.Sprintf(
				"package integrity verification failed: digest of attachment %s is %s, recomputed %s",
				a.ID, want, got)}
		}
	}
	if len(integrityByID) != len(p.attachments) {
		return &PackageError{msg: fmt.Sprintf(
			"package is not self-consistent: integrity block lists %d attachment digests but the package carries %d attachment entr%s",
			len(integrityByID), len(p.attachments), plural(len(p.attachments)))}
	}
	return nil
}

// verifySelfConsistency checks the header/manifest version and label
// echoes (RSF §8.2) and the counts echoes (RSF §8.1).
func (p *loadedPackage) verifySelfConsistency() error {
	h, m := p.header, p.manifest
	if m.SerializationVersion != h.SerializationVersion ||
		m.ExchangeFormatVersion != h.ExchangeFormatVersion ||
		m.SpecificationVersion != h.SpecificationVersion {
		return &PackageError{msg: fmt.Sprintf(
			"package is not self-consistent: manifest version echoes (%s/%s/%s) do not match the package header (%s/%s/%s)",
			m.SerializationVersion, m.ExchangeFormatVersion, m.SpecificationVersion,
			h.SerializationVersion, h.ExchangeFormatVersion, h.SpecificationVersion)}
	}
	if m.PackageIdentityLabel != h.PackageIdentityLabel {
		return &PackageError{msg: fmt.Sprintf(
			"package is not self-consistent: manifest label %q does not match the header label %q",
			m.PackageIdentityLabel, h.PackageIdentityLabel)}
	}
	// The label embeds the Serialization Version; a legacy v1 package
	// derives its label with the version it declares, not the current one.
	// For an unsupported version the label check is skipped — phase 1
	// contract validation rejects it with the version diagnostic.
	if h.SerializationVersion == SerializationVersion || h.SerializationVersion == LegacySerializationVersion {
		wantLabel := PackageIdentityLabelVersion(h.ExportScope, h.Namespace, h.SerializationVersion)
		if h.PackageIdentityLabel != wantLabel {
			return &PackageError{msg: fmt.Sprintf(
				"package label %q cannot be derived from the declared scope %q and namespace %q (expected %q)",
				h.PackageIdentityLabel, h.ExportScope, h.Namespace, wantLabel)}
		}
	}
	if m.Counts.Units != len(p.units) || m.Counts.Attachments != len(p.attachments) {
		return &PackageError{msg: fmt.Sprintf(
			"package is not self-consistent: manifest counts (units %d, attachments %d) do not match the package contents (%d unit(s), %d attachment(s))",
			m.Counts.Units, m.Counts.Attachments, len(p.units), len(p.attachments))}
	}
	if m.Scope != h.ExportScope {
		return &PackageError{msg: fmt.Sprintf(
			"package is not self-consistent: manifest scope %q does not match the header scope %q",
			m.Scope, h.ExportScope)}
	}
	return nil
}

// manifestUnit returns the manifest entry of a unit, if present.
func (p *loadedPackage) manifestUnit(form string) (ManifestUnit, bool) {
	for _, mu := range p.manifest.Units {
		if mu.CanonicalIdentityForm == form {
			return mu, true
		}
	}
	return ManifestUnit{}, false
}

// PackageError is a deterministic package rejection (exit code 2): the
// package is unreadable, malformed, not self-consistent, fails integrity
// verification, or uses a version/feature this importer does not support.
// Package errors are distinct from repository-side failures (conflicts,
// relationship failures, validation failures — exit code 1).
type PackageError struct{ msg string }

func (e *PackageError) Error() string { return e.msg }

func packageErrorf(format string, args ...any) error {
	return &PackageError{msg: fmt.Sprintf(format, args...)}
}

// plural renders the count with a correct noun suffix.
func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// --- Exchange §11.1 phases 1-2 (package-side validation). ---

// checkContract (phase 1) validates the declared versions against the
// supported set, listing what was found versus what is supported. The
// supported set: v2.0 (SerializationVersion), v1.1 and the pre-1.1 line
// (LegacySerializationVersion, LegacySerializationVersionV1 — both
// decoded via the legacy key-rename pass).
func checkContract(p *loadedPackage) error {
	h := p.header
	if h.SerializationVersion != SerializationVersion &&
		h.SerializationVersion != LegacySerializationVersion &&
		h.SerializationVersion != LegacySerializationVersionV1 {
		return packageErrorf(
			"import refused: unsupported serialization version %q (found) — this importer supports %q, %q and %q (supported); the package cannot be interpreted safely",
			h.SerializationVersion, SerializationVersion, LegacySerializationVersion, LegacySerializationVersionV1)
	}
	if h.ExchangeFormatVersion != ExchangeFormatVersion {
		return packageErrorf(
			"import refused: unsupported exchange format version %q (found) — this importer supports %q (supported); the package cannot be interpreted safely",
			h.ExchangeFormatVersion, ExchangeFormatVersion)
	}
	if h.SpecificationVersion != SpecificationVersion && h.SpecificationVersion != LegacySpecificationVersion {
		return packageErrorf(
			"import refused: unsupported specification version %q (found) — this importer validates against %q (supported; legacy %q accepted); the taxonomy and state variants of the declared version cannot be applied",
			h.SpecificationVersion, SpecificationVersion, LegacySpecificationVersion)
	}
	if h.Exporter == "" {
		return packageErrorf("import refused: the package header declares no exporter identity")
	}
	if len(p.declarations.Extensions) > 0 {
		return packageErrorf(
			"import refused: the package declares %d extension(s), but this importer implements no extensions; rejection is explicit, never silent (Exchange §16.3)",
			len(p.declarations.Extensions))
	}
	return nil
}

// checkUnitIdentities (phase 2) validates that every unit identity is
// canonical (charset guard, RSF §5.2.3) and unique within the package.
func checkUnitIdentities(p *loadedPackage) error {
	seen := map[string]string{}
	for _, u := range p.units {
		for _, c := range []struct{ component, label string }{
			{u.Identity.Namespace, "namespace"},
			{u.Identity.Type, "type"},
			{u.Identity.ID, "id"},
		} {
			if err := validateIdentityComponent(c.component, c.label, u.CanonicalIdentityForm); err != nil {
				return err // *ContentError; mapped to exit 2 like PackageError.
			}
		}
		if u.Identity.InstanceVersion < 1 {
			return packageErrorf(
				"import refused: unit %s carries instance-version %d; instance versions must be >= 1",
				u.CanonicalIdentityForm, u.Identity.InstanceVersion)
		}
		if first, dup := seen[u.CanonicalIdentityForm]; dup {
			return packageErrorf(
				"import refused: duplicate identity %s within the package (also used by %s)",
				u.CanonicalIdentityForm, first)
		}
		seen[u.CanonicalIdentityForm] = u.UnitDir
	}
	// Relationship targets must be well-formed canonical identity forms.
	for _, u := range p.units {
		for _, rel := range u.Relationships {
			if rel.Type == "" {
				return packageErrorf("import refused: unit %s carries a relationship with an empty type", u.CanonicalIdentityForm)
			}
			if !isCanonicalRelationshipType(rel.Type) {
				return packageErrorf(
					"import refused: unit %s uses relationship type %q, which is not one of the five canonical types (%s) and is not declared as an extension (this importer implements no extensions)",
					u.CanonicalIdentityForm, rel.Type, strings.Join(conformance.RelationshipFieldNames(), ", "))
			}
			if _, err := parseCanonicalIdentity(rel.Target); err != nil {
				return packageErrorf(
					"import refused: unit %s relationship %q target %q is not a valid canonical identity form: %v",
					u.CanonicalIdentityForm, rel.Type, rel.Target, err)
			}
		}
	}
	return nil
}

// isCanonicalRelationshipType reports whether t is one of the five
// canonical relationship types.
func isCanonicalRelationshipType(t string) bool {
	for _, field := range conformance.RelationshipFieldNames() {
		if t == field {
			return true
		}
	}
	return false
}

// parseCanonicalIdentity parses a canonical identity form
// "<namespace>/<type>:<id>:<instance-version>" strictly: the namespace is
// present, the version is present, and re-serializing the parsed tuple
// reproduces the input exactly.
func parseCanonicalIdentity(form string) (Identity, error) {
	ref, err := conformance.ParseReference(form, "", "")
	if err != nil {
		return Identity{}, err
	}
	if ref.Namespace == "" || !ref.HasVersion {
		return Identity{}, fmt.Errorf("form %q must carry namespace and instance-version", form)
	}
	id := Identity{
		Namespace:       ref.Namespace,
		Type:            ref.Type,
		ID:              ref.ID,
		InstanceVersion: ref.Version,
	}
	if id.CanonicalForm() != form {
		return Identity{}, fmt.Errorf("form %q is not canonical", form)
	}
	return id, nil
}
