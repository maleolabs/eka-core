// Package workspace implements the EKA Workspace: the local canonical
// storage of the EKA Knowledge Runtime (milestone EKA v0.2.0). The
// workspace holds the project/repository registry, the canonical
// object store (eka.db) and workspace metadata (workspace.json).
//
// Layout:
//
//	<workspace>/
//	  workspace.json   metadata: schema_version, id, created
//	  eka.db           SQLite canonical store (store package)
//
// The default workspace root is $EKA_HOME when set (absolute), else
// ~/.eka (os.UserHomeDir; %USERPROFILE%/.eka on Windows). All commands
// use workspace.Ensure: initialization is idempotent, so read-only
// commands and mutating commands share one entry point.
//
// Security: the workspace directory is created with mode 0700 on unix,
// so the canonical store is not world-readable.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/maleolabs/eka-core/store"
)

// schemaVersion is the workspace.json schema version.
const schemaVersion = 1

// workspaceFile is the on-disk shape of workspace.json.
type workspaceFile struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Created       string `json:"created"`
}

// Workspace is one opened EKA workspace: the root directory plus the
// canonical store. It is the single handle every runtime command works
// against.
type Workspace struct {
	// Dir is the absolute workspace root.
	Dir string

	st      *store.Store
	id      string
	created string
}

// Store returns the persistence handle (the opened canonical store,
// eka.db). It is internal to the Runtime Kernel — packages workspace,
// sync, store, and the runtime package — and must not be used by
// external consumers: they communicate with the workspace only through
// the runtime services.
func (w *Workspace) Store() *store.Store { return w.st }

// HomeDir resolves the workspace root: $EKA_HOME when set (must be
// absolute), else <user home>/.eka. It errors (exit code 2 class) when
// neither EKA_HOME nor a user home directory is available.
func HomeDir() (string, error) {
	if env := os.Getenv("EKA_HOME"); env != "" {
		if !filepath.IsAbs(env) {
			return "", fmt.Errorf("workspace: EKA_HOME must be an absolute path, got %q", env)
		}
		return filepath.Clean(env), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("workspace: cannot resolve the workspace root: neither EKA_HOME nor the user home directory is available")
	}
	return filepath.Join(home, ".eka"), nil
}

// Ensure opens the workspace at HomeDir, creating it when missing:
// mkdir -p (0700 on unix), workspace.json written when absent (with a
// deterministic id derived from the absolute path and the current
// date), and eka.db opened/created. It is idempotent: repeated calls
// return an equivalent workspace.
func Ensure() (*Workspace, error) {
	dir, err := HomeDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace: cannot create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace: cannot secure %s: %w", dir, err)
	}

	meta, err := ensureMetaFile(dir)
	if err != nil {
		return nil, err
	}

	db, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	return &Workspace{Dir: dir, st: db, id: meta.ID, created: meta.Created}, nil
}

// Open is the read-style alias of Ensure: workspace initialization is
// idempotent and deterministic, so every command (read-only or
// mutating) uses Ensure. Open exists for callers that want the name to
// document intent.
func Open() (*Workspace, error) {
	return Ensure()
}

// Path returns the absolute workspace root.
func (w *Workspace) Path() string { return w.Dir }

// Meta returns the workspace metadata: schema version, id, created
// date.
func (w *Workspace) Meta() (sv int, id, created string) {
	return schemaVersion, w.id, w.created
}

// Close closes the canonical store.
func (w *Workspace) Close() error {
	return w.st.Close()
}

// ensureMetaFile creates workspace.json when missing and reads it
// back. The id is deterministic: "eka-" + hex(SHA-256(absolute
// path))[0:12]; created is the current date YYYY-MM-DD.
func ensureMetaFile(dir string) (*workspaceFile, error) {
	path := filepath.Join(dir, "workspace.json")
	data, err := os.ReadFile(path)
	if err == nil {
		var meta workspaceFile
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("workspace: %s is not valid workspace.json: %w", path, err)
		}
		if meta.SchemaVersion != schemaVersion || meta.ID == "" || meta.Created == "" {
			return nil, fmt.Errorf("workspace: %s is missing required metadata (schema_version %d, id, created)", path, schemaVersion)
		}
		return &meta, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("workspace: cannot read %s: %w", path, err)
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("workspace: cannot resolve %s: %w", dir, err)
	}
	sum := sha256.Sum256([]byte(abs))
	meta := &workspaceFile{
		SchemaVersion: schemaVersion,
		ID:            "eka-" + hex.EncodeToString(sum[:])[:12],
		Created:       time.Now().Format("2006-01-02"),
	}
	data, err = json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("workspace: cannot encode workspace.json: %w", err)
	}
	// 0600: the workspace metadata is private to the user.
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("workspace: cannot write %s: %w", path, err)
	}
	return meta, nil
}
