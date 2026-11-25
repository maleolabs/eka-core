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
//
// The write is atomic with respect to I/O failures: the container is
// built in a temporary sibling file in the destination's parent
// directory (same filesystem, so the publish rename cannot degrade into a
// copy) and only renamed onto path after the ZIP has been finalized and
// closed successfully. A failure at any point removes the temporary file
// and leaves path in its prior state — either absent, or still holding
// the previously complete package. A partial container is therefore never
// observable at the destination. Determinism is unaffected: the entry
// set, order, compression method and zero timestamps are unchanged.
func writeZIP(path string, entries []entry) error {
	if err := checkAllEntryNames(entries); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("cannot create output directory: %w", err)
	}
	f, err := os.CreateTemp(parent, ".eka-pkg-*")
	if err != nil {
		return fmt.Errorf("cannot create temporary package file: %w", err)
	}
	tmp := f.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmp)
		}
	}()

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
			_ = zw.Close() // best effort: the temporary file is discarded
			_ = f.Close()
			return fmt.Errorf("cannot create zip entry %s: %w", e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			_ = zw.Close() // best effort: the temporary file is discarded
			_ = f.Close()
			return fmt.Errorf("cannot write zip entry %s: %w", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return fmt.Errorf("cannot finalize zip container: %w", err)
	}
	// Close before publishing: an error surfaced only at close (e.g. a
	// deferred write failure) must not publish a truncated container.
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot finalize package file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("cannot move package file into place: %w", err)
	}
	done = true
	return nil
}

// writeDir emits the entries as a directory layout rooted at dir (the
// directory is the package root; the same logical structure as the ZIP
// container, RSF §4.4.2). Every entry name is verified against the
// entry-name contract before anything is created, so no entry can escape
// the package root and a refused entry leaves no partial layout behind.
//
// The write is atomic with respect to I/O failures: the layout is built
// completely in a temporary sibling directory in the destination's parent
// directory (same filesystem) and published only after every entry has
// been written. A failure at any point removes the temporary directory and
// leaves dir in its prior state — either absent, or still holding the
// previously complete package — so a half-populated package root is never
// observable at the destination.
func writeDir(dir string, entries []entry) error {
	if err := checkAllEntryNames(entries); err != nil {
		return err
	}
	// filepath.Dir drops a trailing separator, which would make a trailing
	// slash path ("out/pkg/") its own parent and stage inside the
	// destination. The cleaned form has the same name without the
	// separator, so its parent is the real parent directory.
	parent := filepath.Dir(filepath.Clean(dir))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("cannot create package directory: %w", err)
	}
	stage, err := os.MkdirTemp(parent, ".eka-pkg-*")
	if err != nil {
		return fmt.Errorf("cannot create temporary package directory: %w", err)
	}
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(stage)
		}
	}()
	for _, e := range entries {
		path := filepath.Join(stage, filepath.FromSlash(e.name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("cannot create package directory %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, e.data, 0o644); err != nil {
			return fmt.Errorf("cannot write %s: %w", e.name, err)
		}
	}

	// Publish. Replacing a non-empty directory is not one atomic rename on
	// POSIX, so this is the deliberate two-rename swap already used by
	// sync/push.go: move any existing package aside to a temporary backup,
	// move the staged layout into place, then drop the backup. If the
	// second rename fails the backup is restored, so a failed publish
	// leaves the previous complete package in place.
	backup := ""
	if _, err := os.Lstat(dir); err == nil {
		backup = stage + ".old"
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("cannot clear stale backup directory: %w", err)
		}
		if err := os.Rename(dir, backup); err != nil {
			return fmt.Errorf("cannot move current package directory aside: %w", err)
		}
	}
	if err := os.Rename(stage, dir); err != nil {
		// Restore the previous layout (best effort), then report.
		if backup != "" {
			_ = os.Rename(backup, dir)
		}
		return fmt.Errorf("cannot move package directory into place: %w", err)
	}
	done = true
	if backup != "" {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("cannot remove replaced package directory: %w", err)
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
