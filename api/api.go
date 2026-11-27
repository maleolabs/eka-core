// Package api is the PUBLIC, versioned contract of eka-core: the single
// import a consumer (eka-mcp today, the steward and Atrium later) needs
// to drive the EKA Knowledge Runtime. It is the ADR-014 trigger — a
// consumer now requires interfaces — with ADR-030 (the plugin contract
// relocated to eka-core/plugin) as the precedent.
//
// Consumers import this package and treat the internal packages
// (runtime/, store/, workspace/, sync/) as private implementation
// details. The Runtime contract below is a thin façade: every method
// delegates to the existing internal implementation unchanged, so the
// package adds no behavior and no state.
//
// # Compatibility window
//
// The contract is versioned by ContractVersion. Within a major version
// the interface set is ADDITIVE ONLY: a consumer's use of an existing
// method keeps compiling. Removing or changing an existing method is a
// major bump. The pre-v2 coupling — a consumer importing runtime/ or
// store/ directly — is DEPRECATED: it stays valid through the v1 line
// so consumers can migrate, and an architecture test refuses it from
// v2 on (ADR-014, ADR-030). The migration mapping from the internal
// surface to this one is documented in the README of this package.
//
// The façade holds the concrete runtime; the interfaces exist so a
// consumer depends on a stable surface, not on the kernel's internal
// types. No import cycle: api imports runtime/, never the reverse.
package api

import (
	"github.com/maleolabs/eka-core/v2/exchange"
	"github.com/maleolabs/eka-core/v2/runtime"
)

// ContractVersion is the machine-readable version of this package's
// contract. Consumers pin against it; a major change to the interface
// set bumps it deterministically.
const ContractVersion = "v1"

// Runtime is the public entry point of the EKA Knowledge Runtime: one
// opened workspace (the registry + the canonical store) with every
// service exposed. Obtain it with Open or Ensure and release it with
// Close.
type Runtime interface {
	// Path returns the absolute workspace root — also on a detached
	// Runtime (Open on a missing workspace).
	Path() string
	// Exists reports whether the workspace is initialized. On a
	// detached Runtime every service call errors; only Path, Exists,
	// Close and Snapshot reads are meaningful.
	Exists() bool
	// Close closes the canonical store. Idempotent; a no-op on a
	// detached Runtime.
	Close() error

	// Workspace is the registry, sync-log and status read service.
	Workspace() Workspace
	// Knowledge is the Engineering-Knowledge read service.
	Knowledge() Knowledge
	// Resolver is the identity resolution service.
	Resolver() Resolver
	// Integrity is the store integrity verification service.
	Integrity() Integrity
	// Authoring is the compiler/validation/publish/sync write service.
	Authoring() Authoring
	// Contexts is the deterministic Context Engine read service.
	Contexts() Contexts
}

// Open opens the EKA Runtime read-only: it never initializes a
// workspace, and a missing workspace yields a detached Runtime whose
// service calls report the uninitialized state. Mirror of
// runtime.Open.
func Open() (Runtime, error) {
	rt, err := runtime.Open()
	if err != nil {
		return nil, err
	}
	return newRuntimeFacade(rt), nil
}

// Ensure opens (initializing when missing) the EKA Runtime at the
// workspace root and wires every service. Idempotent. Mirror of
// runtime.Ensure.
func Ensure() (Runtime, error) {
	rt, err := runtime.Ensure()
	if err != nil {
		return nil, err
	}
	return newRuntimeFacade(rt), nil
}

// HomeDir resolves the workspace root: $EKA_HOME when set (must be
// absolute), else <user home>/.eka. Mirror of runtime.HomeDir.
func HomeDir() (string, error) {
	return runtime.HomeDir()
}

// KnowledgeUnit is the canonical knowledge object every read service
// returns. It is re-exported so a consumer needs only this package to
// name the type (the underlying type is exchange.Unit).
type KnowledgeUnit = exchange.Unit

// newRuntimeFacade wires the service façades around one concrete
// runtime.
func newRuntimeFacade(rt *runtime.Runtime) *runtimeFacade {
	return &runtimeFacade{
		rt:    rt,
		ws:    workspaceFacade{rt},
		know:  knowledgeFacade{rt},
		res:   resolverFacade{rt},
		integ: integrityFacade{rt},
		auth:  authoringFacade{rt},
		ctx:   contextsFacade{rt},
	}
}

// runtimeFacade is the api.Runtime backed by the internal runtime.
type runtimeFacade struct {
	rt    *runtime.Runtime
	ws    Workspace
	know  Knowledge
	res   Resolver
	integ Integrity
	auth  Authoring
	ctx   Contexts
}

func (f *runtimeFacade) Path() string         { return f.rt.Path() }
func (f *runtimeFacade) Exists() bool         { return f.rt.Exists() }
func (f *runtimeFacade) Close() error         { return f.rt.Close() }
func (f *runtimeFacade) Workspace() Workspace { return f.ws }
func (f *runtimeFacade) Knowledge() Knowledge { return f.know }
func (f *runtimeFacade) Resolver() Resolver   { return f.res }
func (f *runtimeFacade) Integrity() Integrity { return f.integ }
func (f *runtimeFacade) Authoring() Authoring { return f.auth }
func (f *runtimeFacade) Contexts() Contexts   { return f.ctx }
