// Package runtime implements the EKA Runtime Kernel: the internal API
// of the EKA Knowledge Runtime (milestone EKA v0.2.0, milestone 5).
//
// The Runtime is the single entry point every consumer — the CLI
// (cmd/), and future consumers such as the Context Engine, MCP or
// Atrium — talks to. It aggregates the workspace (workspace/) and the
// canonical store (store/) behind documented, domain-shaped services:
//
//	Workspace   workspace registry, last-sync log, aggregated status
//	Knowledge   Engineering-Knowledge reads (units, object, search,
//	            counts) — NOT CRUD
//	Resolver    identity resolution (canonical form, line)
//	Relations   relationship traversal (from/to/upstream/downstream)
//	Timeline    instance-line history (change logs + hashes)
//	Snapshot    verified package reads
//	Integrity   store integrity verification + schema version
//
// and the Authoring API (authoring.go) — the stateless compiler,
// validation and sync gateway that turns authoring representations
// (Markdown is one adapter behind it, in conformance/) into Canonical
// Knowledge Objects and runtime state.
//
// Boundary contract (architecture):
//
//   - Consumers communicate ONLY through these services. Direct
//     access to workspace/ or store/ from outside this package (and
//     the internal packages it wraps: workspace, sync, store) is not
//     valid architecture — SQLite is a private implementation detail
//     of the Runtime Kernel.
//   - The Runtime API never understands Markdown: authoring input
//     reaches the kernel only through the Authoring API.
//   - The Authoring API never exposes database details: it produces
//     only CKOs (Compile), reports (Validate) or runtime state (Sync).
//
// All services are concrete types with documented method contracts —
// there are deliberately no interface types (no consumer needs
// polymorphism), and no HTTP/RPC/gRPC/REST: this milestone establishes
// the in-process API only.
package runtime

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
)

// Runtime is one opened EKA Runtime: the workspace plus its canonical
// store, with every service wired. Create it with Ensure (initializes
// when missing) or Open (read-only probe; see Open); close it with
// Close.
type Runtime struct {
	// dir is the absolute workspace root (available even when the
	// workspace is not initialized — the detached state of Open).
	dir string
	// ws is the workspace handle; nil when the workspace is not
	// initialized (Open on a missing workspace.json).
	ws *workspace.Workspace
	// st is the canonical store handle — a private implementation
	// detail of the kernel.
	st *store.Store

	// Workspace is the workspace registry and status service.
	Workspace *WorkspaceService
	// Knowledge is the Engineering-Knowledge read service.
	Knowledge *KnowledgeService
	// Resolver is the identity resolution service.
	Resolver *ResolverService
	// Relations is the relationship traversal service.
	Relations *RelationsService
	// Timeline is the instance-line history service.
	Timeline *TimelineService
	// Snapshot is the verified package read service.
	Snapshot *SnapshotService
	// Integrity is the store integrity verification service.
	Integrity *IntegrityService
}

// Ensure opens the EKA Runtime at the workspace root (workspace.Ensure:
// $EKA_HOME when set, else ~/.eka), initializing the workspace when
// missing — mkdir, workspace.json, eka.db — and wires every service.
// It is idempotent: repeated calls return equivalent runtimes.
func Ensure() (*Runtime, error) {
	ws, err := workspace.Ensure()
	if err != nil {
		return nil, err
	}
	return newRuntime(ws, ws.Store()), nil
}

// Open is the read-style entry point of the Runtime: it opens an
// EXISTING workspace and never initializes one. When workspace.json is
// missing the Runtime is returned in the detached state (Exists() ==
// false, Path() resolves the home directory, Close is a no-op) so
// read-only commands can report the uninitialized workspace
// informatively — the `eka status` informational case. Any service
// call on a detached Runtime errors.
//
// Parity note: workspace.Open is the idempotent creation alias of
// workspace.Ensure, so a literal parity would make Open initialize the
// workspace — which no read-only command may do ("eka status" never
// creates the workspace, a pinned CLI contract). The Runtime's Open is
// therefore the non-creating counterpart: it fails when the workspace
// cannot be probed, and reports "not initialized" through Exists
// instead of creating.
func Open() (*Runtime, error) {
	dir, err := workspace.HomeDir()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, "workspace.json")); err != nil {
		if os.IsNotExist(err) {
			// Detached: the workspace has never been initialized. The
			// services are wired and every store/registry-touching
			// call errors deterministically (requireWorkspace/
			// requireStore); only Exists, Path, Close, HomeDir and
			// Snapshot.Read (a pure disk read, no store access) are
			// meaningful in this state.
			return wireServices(&Runtime{dir: dir}), nil
		}
		return nil, fmt.Errorf("runtime: cannot access %s: %w", dir, err)
	}
	return Ensure()
}

// newRuntime wires every service around one opened workspace.
func newRuntime(ws *workspace.Workspace, st *store.Store) *Runtime {
	return wireServices(&Runtime{dir: ws.Path(), ws: ws, st: st})
}

// wireServices attaches every service to the Runtime. It runs for the
// opened and the detached state alike, so consumers can always call
// the services (the detached ones error via requireWorkspace).
func wireServices(rt *Runtime) *Runtime {
	rt.Workspace = &WorkspaceService{rt: rt}
	rt.Knowledge = &KnowledgeService{rt: rt}
	rt.Resolver = &ResolverService{rt: rt}
	rt.Relations = &RelationsService{rt: rt}
	rt.Timeline = &TimelineService{rt: rt}
	rt.Snapshot = &SnapshotService{rt: rt}
	rt.Integrity = &IntegrityService{rt: rt}
	return rt
}

// Close closes the canonical store (eka.db). Closing a detached
// Runtime (Open on a missing workspace) is a no-op. Close is
// idempotent; the store is closed exactly once.
func (r *Runtime) Close() error {
	if r.ws == nil {
		return nil
	}
	return r.ws.Close()
}

// Path returns the absolute workspace root — also on a detached
// Runtime (Open on a missing workspace), where it is the home
// directory that would hold the workspace.
func (r *Runtime) Path() string { return r.dir }

// Exists reports whether the workspace is initialized — whether
// workspace.json exists. The informational gate of `eka status`: a
// detached Runtime (Open on a missing workspace) reports false and its
// services must not be called.
func (r *Runtime) Exists() bool { return r.ws != nil }

// HomeDir resolves the workspace root: $EKA_HOME when set (must be
// absolute), else <user home>/.eka. It wraps workspace.HomeDir — the
// single shared resolution used by every Runtime entry point.
func HomeDir() (string, error) {
	return workspace.HomeDir()
}

// requireWorkspace returns the workspace handle, or errors on a
// detached Runtime (Open on a missing workspace). Every service method
// that touches the workspace or the store goes through it, so a
// consumer can never panic on an uninitialized workspace.
func (r *Runtime) requireWorkspace() (*workspace.Workspace, error) {
	if r.ws == nil {
		return nil, fmt.Errorf("runtime: the EKA workspace at %s is not initialized; run 'eka project register' to create it", r.dir)
	}
	return r.ws, nil
}

// requireStore returns the canonical store handle, or errors on a
// detached Runtime.
func (r *Runtime) requireStore() (*store.Store, error) {
	if r.st == nil {
		return nil, fmt.Errorf("runtime: the EKA workspace at %s is not initialized; run 'eka project register' to create it", r.dir)
	}
	return r.st, nil
}
