package exchange

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file implements the package reader side of the import pipeline
// (RSF §13.2 step 1): opening a package — single-file ZIP container
// (.ekapkg) or the equivalent directory layout — and reading its entries
// deterministically. Both projections carry the same logical structure
// (RSF §4.4.2); the reader treats them identically from here on.
//
// Reading policy:
//   - every entry name passes the writer's entry-name contract
//     (checkEntryName: no "..", no absolute paths, no "\" or ":") — the
//     zip-slip defense of the exporter is mirrored on the read side;
//   - entries are stored sorted by name, so every consumer iterates in
//     deterministic order;
//   - duplicate entry names are rejected (a zip container may carry them;
//     the logical structure cannot);
//   - a file that is neither a ZIP container nor a directory layout is
//     rejected with a deterministic message.

// PackageReader is one opened package: the complete entry set read into
// memory, keyed by logical entry name.
type PackageReader struct {
	// entries maps logical entry name -> raw bytes.
	entries map[string][]byte
	// names is the sorted entry-name list.
	names []string
	// path is the package path as given.
	path string
}

// OpenPackage opens a package at path: a ZIP container or a directory
// layout. The package type is detected from the filesystem (a directory is
// a directory layout; anything else is tried as a ZIP container).
func OpenPackage(path string) (*PackageReader, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open package %s: %w", path, err)
	}
	r := &PackageReader{path: path}
	if info.IsDir() {
		if err := r.readDir(path); err != nil {
			return nil, err
		}
	} else {
		if err := r.readZIP(path); err != nil {
			return nil, err
		}
	}
	if _, ok := r.entries["header.json"]; !ok {
		return nil, fmt.Errorf("package %s has no header.json entry: not an RSF package", path)
	}
	return r, nil
}

// Path returns the package path as given.
func (r *PackageReader) Path() string { return r.path }

// Entries returns the sorted entry names.
func (r *PackageReader) Entries() []string { return r.names }

// Entry returns the raw bytes of one entry; ok is false when missing.
func (r *PackageReader) Entry(name string) ([]byte, bool) {
	data, ok := r.entries[name]
	return data, ok
}

// Len returns the number of entries.
func (r *PackageReader) Len() int { return len(r.entries) }

// readZIP loads a single-file container. Entries are re-sorted by name so
// the archive's physical order never influences processing order.
func (r *PackageReader) readZIP(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("package %s is neither a ZIP container nor a directory layout: %v", path, err)
	}
	defer zr.Close()
	seen := map[string]bool{}
	for _, f := range zr.File {
		if err := checkEntryName(f.Name); err != nil {
			return fmt.Errorf("package %s contains an invalid entry: %w", path, err)
		}
		if seen[f.Name] {
			return fmt.Errorf("package %s contains duplicate entry %s", path, f.Name)
		}
		seen[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("package %s: cannot read entry %s: %w", path, f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("package %s: cannot read entry %s: %w", path, f.Name, err)
		}
		r.add(f.Name, data)
	}
	return nil
}

// readDir loads a directory-layout package: every file under the root
// becomes an entry named by its root-relative path with forward slashes.
func (r *PackageReader) readDir(dir string) error {
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Symlinks are never followed: a symlinked entry could read
		// content from outside the package root (defense in depth —
		// the digest check would catch the mismatch anyway, but a
		// package entry must always be a regular file).
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if err := checkEntryName(name); err != nil {
			return fmt.Errorf("package %s contains an invalid entry: %w", dir, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("package %s: cannot read entry %s: %w", dir, name, err)
		}
		r.add(name, data)
		return nil
	})
	if err != nil {
		return fmt.Errorf("cannot read package directory %s: %w", dir, err)
	}
	return nil
}

// add stores one entry and keeps the name list sorted.
func (r *PackageReader) add(name string, data []byte) {
	if r.entries == nil {
		r.entries = map[string][]byte{}
	}
	r.entries[name] = data
	r.names = append(r.names, name)
	sort.Strings(r.names)
}

// packageEntries extracts the sorted entry list of a reader (convenience
// for deserialize.go).
func (r *PackageReader) sortedEntries() []string {
	return r.names
}

// isKnownEntry reports whether name is one of the six logical element
// locations of the concrete v1 projection (header/manifest/declarations/
// integrity at the root, units/ and attachments/ trees). Used by the
// unknown-entry rejection (RSF §9.5 reject-by-default): a package entry
// that maps to no logical element is invalid.
func isKnownEntry(name string) bool {
	switch name {
	case "header.json", "manifest.json", "declarations.json", "integrity.json":
		return true
	}
	if strings.HasPrefix(name, "units/") || strings.HasPrefix(name, "attachments/") {
		return true
	}
	return false
}
