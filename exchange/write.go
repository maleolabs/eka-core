package exchange

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file implements the package writer (RSF §13.1 step 8): the
// deterministic emission of the assembled entries as either a single-file
// ZIP container (.ekapkg) or the equivalent directory layout. Both
// projections carry the same logical structure (RSF §4.4.2) and are
// byte-deterministic for identical entry sets.
//
// ZIP encoding (reference implementation choice, documented):
//   - entries sorted by name (already guaranteed by assemble);
//   - zero modification timestamps: FileHeader.Modified is left at
//     time.Time{} so archive/zip writes zero MS-DOS date/time fields
//     (read back as 1980-01-01 00:00:00 UTC — constant, never variable);
//   - Deflate compression (deterministic for identical input bytes and
//     Go's fixed implementation).

// checkEntryName verifies one package entry name against the writer's
// entry-name contract: every entry must stay inside the package root.
// Rejected: empty names, "." and "..", any name containing ".." (path
// traversal / zip-slip), names starting with "/" (absolute paths), and
// names containing "\" or ":" (Windows path separators / drive letters /
// NTFS alternate streams). The identity charset guard (load.go) runs
// before any write, so names reaching the writer are already safe in
// normal operation; this is defense in depth against future entry
// producers (e.g. attachment IDs, extensions).
func checkEntryName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("entry name must not be empty")
	case name == "." || name == ".." || strings.HasPrefix(name, "/") ||
		strings.Contains(name, "..") || strings.Contains(name, "\\") ||
		strings.Contains(name, ":"):
		return fmt.Errorf("entry name %q is not allowed (must not contain '..', start with '/', or contain '\\' or ':')", name)
	}
	return nil
}

// writeZIP emits the entries as a single-file ZIP container at path.
// The parent directory is created when missing. Every entry name is
// verified before the container is created, so a refused entry leaves
// nothing behind.
func writeZIP(path string, entries []entry) error {
	if err := checkAllEntryNames(entries); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create output directory: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cannot create package file: %w", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for _, e := range entries {
		hdr := &zip.FileHeader{
			Name:   e.name,
			Method: zip.Deflate,
		}
		// Zero timestamp: see the package comment. Set both the modern
		// and legacy fields to zero explicitly.
		hdr.Modified = time.Time{}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return fmt.Errorf("cannot create zip entry %s: %w", e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			return fmt.Errorf("cannot write zip entry %s: %w", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("cannot finalize zip container: %w", err)
	}
	return nil
}

// writeDir emits the entries as a directory layout rooted at dir (the
// directory is the package root; the same logical structure as the ZIP
// container, RSF §4.4.2). Every entry name is verified against the
// entry-name contract before anything is created, so no entry can escape
// the package root and a refused entry leaves no partial layout behind.
func writeDir(dir string, entries []entry) error {
	if err := checkAllEntryNames(entries); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create package directory: %w", err)
	}
	for _, e := range entries {
		path := filepath.Join(dir, filepath.FromSlash(e.name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("cannot create package directory %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, e.data, 0o644); err != nil {
			return fmt.Errorf("cannot write %s: %w", e.name, err)
		}
	}
	return nil
}

// checkAllEntryNames verifies every entry name of one write run up front,
// before any directory, file or zip entry is created (defense in depth;
// see checkEntryName).
func checkAllEntryNames(entries []entry) error {
	for _, e := range entries {
		if err := checkEntryName(e.name); err != nil {
			return fmt.Errorf("refusing to write package entry: %w", err)
		}
	}
	return nil
}
