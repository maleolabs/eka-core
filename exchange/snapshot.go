package exchange

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file implements the snapshot transport projection of the
// Knowledge Runtime: the SOURCE-ONLY package view (header.json,
// units/, attachments/) that the sync engine commits to the repository,
// plus the source fingerprint.
//
// Why source-only (ADR-027): the derived aggregates (manifest.json,
// declarations.json, integrity.json) are whole-package projections that
// change on both sides of any parallel knowledge change — committing
// them turns every parallel merge into a synthetic conflict (and a
// line-wise merge of them is digest-inconsistent, refused byte-exact by
// the next pull). The committed snapshot therefore carries the source
// entries only:
//
//	header.json          — the package contract (identity facts)
//	units/<ns>/<type>-<id>-v<n>/{unit.json, content} — the knowledge
//	attachments/...      — the supporting payloads
//
// Verification of the source entries is per-unit and structural: every
// unit.json is strict-decoded (unknown fields rejected, RSF §9.5), its
// directory must match its identity (UnitDirName), every unit must be
// paired with its content entry, and the per-unit digest is
// recomputable from unit.json || content — the same hash the canonical
// store content-addresses by, so a corrupted unit can never be seeded
// silently. Git additionally protects every committed file object.
//
// Snapshot directories that still carry the aggregates (written by EKA
// < 0.10) are verified with the FULL package verification
// (LoadPackageWithEntries — byte-exact backward compatibility) and
// report their package digest; source-only snapshots report the source
// fingerprint (SnapshotFingerprint). Both digests back the idempotent
// pull and the sync report in the same way.

// SnapshotMode classifies one snapshot directory.
type SnapshotMode int

const (
	// SnapshotSource is a source-only snapshot (no aggregates): verified
	// structurally and per-unit; Digest is the source fingerprint.
	SnapshotSource SnapshotMode = iota
	// SnapshotLegacy is a full package snapshot carrying the derived
	// aggregates: verified byte-exact (LoadPackageWithEntries); Digest
	// is the package digest.
	SnapshotLegacy
)

// LoadSnapshotResult is the outcome of one snapshot load.
type LoadSnapshotResult struct {
	// Package carries the deserialized model (Header, Units sorted by
	// canonical identity form, Attachments sorted by ID; Manifest,
	// Declarations and Integrity are only filled in legacy mode).
	Package *Package
	// Entries maps every source entry name to its raw bytes (the
	// byte-exact unit.json entries the store persists verbatim).
	Entries map[string][]byte
	// Digest is the package digest (legacy mode) or the source
	// fingerprint (source mode) — the idempotent-pull key.
	Digest string
	// Mode reports the verification path that was applied.
	Mode SnapshotMode
}

// LoadSnapshot opens and verifies the snapshot directory at path,
// selecting the verification path by the presence of the derived
// aggregates: both manifest.json and integrity.json present → legacy
// full package verification (byte-exact, package digest); neither
// present → source verification (structural + per-unit, source
// fingerprint); exactly one present → deterministic refusal (a partial
// aggregate state is ambiguous). Every failure is a *PackageError.
func LoadSnapshot(path string) (*LoadSnapshotResult, error) {
	hasManifest := fileExists(filepath.Join(path, "manifest.json"))
	hasIntegrity := fileExists(filepath.Join(path, "integrity.json"))
	switch {
	case hasManifest && hasIntegrity:
		return loadSnapshotLegacy(path)
	case !hasManifest && !hasIntegrity:
		return loadSnapshotSource(path)
	default:
		return nil, &PackageError{msg: fmt.Sprintf(
			"package %s carries a partial aggregate state (one of manifest.json/integrity.json present, the other missing); regenerate or remove the aggregates", path)}
	}
}

// loadSnapshotLegacy verifies a full package snapshot byte-exact (the
// pre-ADR-027 layout) and reports its package digest.
func loadSnapshotLegacy(path string) (*LoadSnapshotResult, error) {
	pkg, entries, err := LoadPackageWithEntries(path)
	if err != nil {
		return nil, err
	}
	return &LoadSnapshotResult{
		Package: pkg,
		Entries: entries,
		Digest:  pkg.Integrity.PackageDigest,
		Mode:    SnapshotLegacy,
	}, nil
}

// loadSnapshotSource verifies a source-only snapshot: strict decode of
// the header (the contract), every unit strict-decoded with its
// directory matching its identity and its content entry present, every
// attachment carried verbatim, unknown entries rejected (RSF §9.5).
// The returned entries map is the raw source entry set; the digest is
// the source fingerprint.
func loadSnapshotSource(path string) (*LoadSnapshotResult, error) {
	r, err := OpenPackage(path)
	if err != nil {
		return nil, &PackageError{msg: err.Error()}
	}
	entries := rawEntries(r)

	// 1. The header is the contract: present, strict-decoded (unknown
	// fields rejected), carrying the package identity facts. Legacy
	// key spellings are refused here — a v1.1 package always carries
	// the aggregates, so a source-only snapshot is always v2.0.
	headerData, ok := entries["header.json"]
	if !ok {
		return nil, &PackageError{msg: fmt.Sprintf("package %s is missing the required header.json entry", r.Path())}
	}
	if isLegacyHeader(headerData) {
		return nil, &PackageError{msg: fmt.Sprintf(
			"package %s is a legacy (pre-v2.0) package without aggregates: unsupported — a legacy package always carries the aggregates", r.Path())}
	}
	var header Header
	if err := strictDecode("header.json", headerData, &header); err != nil {
		return nil, &PackageError{msg: err.Error()}
	}
	if header.PackageIdentityLabel == "" || header.ExportScope == "" {
		return nil, &PackageError{msg: fmt.Sprintf(
			"package %s header.json carries no package identity label or scope", r.Path())}
	}

	// 2. Units: every units/<dir>/unit.json paired with its content
	// entry, strict-decoded, its directory matching its identity, its
	// form unique in the snapshot.
	unitJSONs := map[string][]byte{}
	contents := map[string][]byte{}
	for name, data := range entries {
		if uDir, ok := strings.CutSuffix(name, "/unit.json"); ok {
			unitJSONs[uDir] = data
		} else if strings.HasSuffix(name, "/content") {
			contents[strings.TrimSuffix(name, "/content")] = data
		} else if strings.HasPrefix(name, "units/") {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s contains unknown entry %q under units/", r.Path(), name)}
		}
	}
	dirs := make([]string, 0, len(unitJSONs))
	for uDir := range unitJSONs {
		dirs = append(dirs, uDir)
	}
	sort.Strings(dirs)
	units := make([]*Unit, 0, len(dirs))
	seen := map[string]bool{}
	for _, uDir := range dirs {
		content, ok := contents[uDir]
		if !ok {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s unit %s has no content entry", r.Path(), uDir)}
		}
		u, err := DecodeUnit(unitJSONs[uDir], content)
		if err != nil {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s cannot decode %s/unit.json: %v", r.Path(), uDir, err)}
		}
		// The per-unit digest is recomputable from the canonical bytes
		// (unit.json || content, RSF §9.4) — the same hash the store
		// content-addresses by and the integrity block records. Filled
		// here because a source-only snapshot carries no integrity
		// block; the pull asserts the stored hash equals it.
		sum := sha256.Sum256(append(unitJSONs[uDir], content...))
		u.Digest = hex.EncodeToString(sum[:])
		if UnitDirName(u.Identity) != uDir {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s unit entry %s does not match its identity %s", r.Path(), uDir, UnitDirName(u.Identity))}
		}
		if seen[u.CanonicalIdentityForm] {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s carries a duplicate unit identity %s", r.Path(), u.CanonicalIdentityForm)}
		}
		seen[u.CanonicalIdentityForm] = true
		units = append(units, u)
	}
	for uDir := range contents {
		if _, ok := unitJSONs[uDir]; !ok {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s content entry %s has no unit.json entry", r.Path(), uDir)}
		}
	}

	// 3. Attachments: carried verbatim, keyed by their package IDs.
	var attachments []*Attachment
	for name, data := range entries {
		id, ok := strings.CutPrefix(name, "attachments/")
		if !ok || id == "" {
			continue
		}
		attachments = append(attachments, &Attachment{ID: id, Data: data})
	}
	sort.Slice(attachments, func(i, j int) bool { return attachments[i].ID < attachments[j].ID })

	// 4. The source fingerprint backs the idempotent pull.
	fp, err := SnapshotFingerprint(entries)
	if err != nil {
		return nil, &PackageError{msg: err.Error()}
	}
	return &LoadSnapshotResult{
		Package: &Package{Header: header, Units: units, Attachments: attachments},
		Entries: entries,
		Digest:  fp,
		Mode:    SnapshotSource,
	}, nil
}

// EmitSource projects a fully assembled Package onto the SOURCE entry
// set of the snapshot transport: header.json, units/ and attachments/ —
// the same deterministic bytes Emit produces, minus the derived
// aggregates (which the committed snapshot no longer carries, ADR-027).
// The returned files are sorted by name; the second return value is the
// source fingerprint of the emitted entries.
func EmitSource(pkg *Package) ([]EmittedFile, string, error) {
	files, err := Emit(pkg)
	if err != nil {
		return nil, "", err
	}
	src := make([]EmittedFile, 0, len(files))
	entryMap := map[string][]byte{}
	for _, f := range files {
		if isAggregateEntry(f.Name) {
			continue
		}
		src = append(src, f)
		entryMap[f.Name] = f.Data
	}
	fp, err := SnapshotFingerprint(entryMap)
	if err != nil {
		return nil, "", err
	}
	return src, fp, nil
}

// SnapshotFingerprint computes the deterministic source fingerprint of
// a snapshot entry set: SHA-256 over the sorted (name, bytes) sequence
// of every SOURCE entry (header.json, units/, attachments/) — the
// derived aggregates are excluded defensively. Identical source state
// produces an identical fingerprint on every device.
func SnapshotFingerprint(entries map[string][]byte) (string, error) {
	names := make([]string, 0, len(entries))
	for name := range entries {
		if isAggregateEntry(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		if _, err := h.Write([]byte(name)); err != nil {
			return "", err
		}
		h.Write([]byte{0})
		if _, err := h.Write(entries[name]); err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// isAggregateEntry reports whether an entry name is one of the derived
// aggregates — the entries the committed snapshot does not carry.
func isAggregateEntry(name string) bool {
	switch name {
	case "manifest.json", "declarations.json", "integrity.json":
		return true
	}
	return false
}

// fileExists reports whether path exists as a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
