# eka-core/api — the public contract

`github.com/maleolabs/eka-core/v2/api` is the **versioned public contract**
of eka-core. Consumers (eka-mcp today, the steward and Atrium later)
import this package and treat `runtime/`, `store/`, `workspace/` and
`sync/` as private implementation details.

- **Version:** `api.ContractVersion` (`v1`).
- **Compatibility window:** within a major version the interface set is
  *additive only*. Removing or changing an existing method is a major
  bump.
- **Deprecation:** importing the internal packages from a consumer is
  deprecated. It stays valid through the v1 line so consumers can
  migrate; from **v2** an architecture test refuses a consumer that
  imports the internal packages (ADR-014 trigger, ADR-030 precedent).

## Usage

```go
rt, err := api.Ensure() // or api.Open() — never initializes
if err != nil { /* ... */ }
defer rt.Close()

// read
units, _ := rt.Knowledge().Search(api.SearchQuery{ProjectID: "proj"})
st, _ := rt.Workspace().Status()

// write
res, _ := rt.Authoring().Publish("sto:item-1", api.PublishOptions{})
rep, _ := rt.Authoring().Sync(repoPath, api.SyncOptions{Push: true})
```

The façade is backed by the existing internal implementation: it adds no
behavior and no state. Every method mirrors the concrete internal method
it wraps exactly.

## Migration from the direct-import coupling

| Internal (deprecated) | Public contract |
|---|---|
| `runtime.Ensure()` / `runtime.Open()` | `api.Ensure()` / `api.Open()` |
| `*runtime.Runtime` | `api.Runtime` |
| `rt.Workspace.Projects/Repos/FindRepo/LastSync/Status` | `rt.Workspace().…` |
| `rt.Knowledge.UnitsByProject/Search` | `rt.Knowledge().…` |
| `rt.Resolver.Resolve` | `rt.Resolver().Resolve` |
| `rt.Integrity.Verify/SchemaVersion` | `rt.Integrity().…` |
| `runtime.Authoring.X(rt, …)` | `rt.Authoring().X(…)` |
| `runtime.Project`, `runtime.SearchQuery`, `runtime.PublishOptions`, … | `api.Project`, `api.SearchQuery`, `api.PublishOptions`, … |
| `runtime.PublishError`, `runtime.TransitionRefusal`, … | `api.PublishError`, `api.TransitionRefusal`, … (`errors.As` unchanged) |
| `store.Ref`, `store.Store.UnitsByLine` | `rt.Resolver().ResolveLine(ns, type, id)` |
| `store.Store.RepointUnit` / `PutUnit` | `rt.Authoring().Relate(...)` / `rt.Authoring().Publish(...)` |

`api` is the only eka-core package a consumer needs for the runtime
surface; other eka-core contract packages (`conformance`, `exchange`,
`machine`, `contexts`, `codegraph`, `metadata`, `view`, `plugin`)
remain directly importable public contracts.

## Deferred from v1

Included here is the subset a consumer uses today (read, write,
publish/adopt/sync). Deliberately deferred until a consumer requires
them:

- Registry writes (`RegisterRepo`, `UnregisterRepo`,
  `UnregisterProject`) — drive registration through `Authoring().Sync`.
- Knowledge per-repo/object reads, counts, issue numbering
  (`Units`, `Object`, `Counts`, `NumberForLine`, …).
- Authoring `Compile`, `NewDraftBatch`, `PublishInline`, `NoteReply`,
  `ResolveNote`, `ResolveAllNotes`, `DraftsByProvenance`.
- Raw `store` access — not exposed: it violates the ADR-014 kernel
  boundary; the table above maps every current use to a runtime service.
