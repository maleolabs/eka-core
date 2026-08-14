# eka-core

**eka-core** is the Go library implementing the
[EKA Standard](https://github.com/maleolabs/eka-standard). It provides the
Reference Implementation of the Engineering Knowledge Architecture (EKA): the
conformance validator, the exchange pipeline, the Knowledge Compiler, the
Knowledge Runtime Kernel (with its SQLite canonical store and synchronization
engine), the projection engine, the machine interface, and the Context Engine.

The normative standard text lives in the
[`eka-standard`](https://github.com/maleolabs/eka-standard) repository; this
repository is the executable implementation. **eka-core implements EKA
Standard 1.0.**

Module path: `github.com/maleolabs/eka-core`

## Requirements

- Go 1.24 or newer.
- The SQLite canonical store uses `modernc.org/sqlite` (pure Go, no cgo).

## Installation

```sh
go get github.com/maleolabs/eka-core@v1.0.0
```

eka-core is a library: consumers import its packages directly (the CLI,
`eka-mcp`, and future SDKs all build on it). It ships no executable of its own.

## Package overview

| Package | Role |
|---|---|
| [`conformance`](conformance) | The official EKA conformance validator: scans the `<root>/docs` knowledge tree, classifies authoring files, and applies rules R0–R13 to produce a deterministic, sorted `Report`. Also owns the Engineering Domain ontology (the canonical token→domain mapping shared by `exchange/` and `view/`). |
| [`exchange`](exchange) | The Exchange pipeline: `Export` (repository → RSF package) and `Import` (RSF package → repository), plus package read/verify (`LoadPackage`, `LoadSnapshot`) and the RSF object model (`Package`, `Unit`, `Header`, `Manifest`, `Integrity`). |
| [`compile`](compile) | The Knowledge Compiler: the canonical gateway from authoring representations into Canonical Knowledge Objects (CKOs). Validates, then assembles the RSF package in memory; never writes to disk. |
| [`metadata`](metadata) | The repository identity metadata (`eka.yaml`): parse, marshal, and nearest-file resolution of the `project` / `name` / `namespace` identity triple. |
| [`store`](store) | The local SQLite canonical store (`eka.db`): immutable, content-addressed Engineering Knowledge Objects, mutable references, attachments, and the sync log. A private implementation detail of the Runtime Kernel. |
| [`workspace`](workspace) | The EKA Workspace: the project/repository registry, the canonical store handle, and `workspace.json` metadata. Default root `$EKA_HOME` or `~/.eka`. |
| [`sync`](sync) | The synchronization engine: bidirectional transport (pull/push) between a registered repository and the canonical store, with forward-only re-seed guards and namespace reconciliation. |
| [`runtime`](runtime) | The Runtime Kernel: the single entry point every consumer talks to. Aggregates `workspace/` and `store/` behind domain-shaped services (`Workspace`, `Knowledge`, `Resolver`, `Relations`, `Timeline`, `Snapshot`, `Integrity`) plus the `Authoring` API. |
| [`view`](view) | The projection engine: the closed set of named views (five Engineering Domains, `ticket`, `board`, `document`, `containers`) over the Knowledge Graph. |
| [`machine`](machine) | The machine interface: deterministic canonical JSON (`eka-cko-v2`) of CKOs and collections, for `eka get`, MCP, and other machine consumers. |
| [`contexts`](contexts) | The Context Engine: deterministic construction of the Context Object around one knowledge subject at three depths (`local`, `dependency`, `engineering`). |

## API guide

The key public entry points and types, by package.

### conformance — validation and the domain ontology

```go
report, err := conformance.Validate(root)   // (*Report, error)
```

- `Validate(root)` scans the `<root>/docs` knowledge tree and applies rules
  R0–R13. When `docs/` is absent, validation is skipped with a clean PASS
  (docs-in-repo is legacy authoring; EKA v2 keeps knowledge in the workspace).
- `Report` carries `FilesScanned`, `Artifacts`, `Skipped`, and `Results`;
  `Report.Pass()`, `Report.ErrorCount()`, `Report.WarningCount()`, and
  `Report.SortedResults()` give the verdict. `Result` records a single finding
  (`File`, `Rule`, `Severity`, `Message`); `Severity` is `error` or `warning`
  (warnings never block a pass).
- `Scan(root)` returns the classified `[]Artifact` without rule evaluation.
- `ScanFile(path)` classifies one authoring file (draft lifecycle).
- `ValidateCKO(u CKOArtifact, opts)` validates one canonical unit with the
  location-shaped rules (R2, R6 dimension==folder) skipped — the
  authoring-publish path.
- Domain ontology (single source of truth for `exchange/` and `view/`):
  `Domain` (`Discovery`, `Architecture`, `Planning`, `Execution`,
  `Operations`), `DomainForToken`, `DomainForDimension`, `Stratum`,
  `StrataAbove`, `ParseDomain`, `IsDomain`, `DomainNames`.

### exchange — the Exchange pipeline

```go
result, err := exchange.Export(root, opts)        // (*Result, error)
iresult, err := exchange.Import(pkgPath, opts)    // (*ImportResult, error)
pkg, err := exchange.LoadPackage(path)            // (*Package, error)
```

- `Export` runs validation, resolves scope, selects units, detects external
  references, assembles the object model, projects to the RSF, computes
  integrity, and emits atomically (single-file `.ekapkg` ZIP or directory
  layout). It refuses a repository with blocking violations and never leaves a
  partial package behind.
- `Import` verifies package integrity, runs phases 1–8 against the package,
  commits atomically (phase 9), then re-validates with rollback (phase 10).
- `Options` / `ImportOptions` configure the two pipelines; typed errors
  (`ValidationError`, `ContentError`, `ImportValidationError`,
  `RelationshipError`, `ConflictError`, `PackageError`) carry the refusal
  class for the CLI's exit-code mapping.
- `Package`, `Unit`, `Header`, `Manifest`, `Integrity`, `Declarations` are the
  RSF object model; `Unit` is the Canonical Knowledge Object of the Runtime
  store.

### runtime — the Runtime Kernel

```go
rt, err := runtime.Ensure()      // open (and initialize if needed) the workspace
rt, err := runtime.Open()        // read-only probe; never initializes
defer rt.Close()
```

`Runtime` exposes concrete services — no interface types:

- `Workspace` — registry + status: `RegisterRepo`, `FindRepo`, `Projects`,
  `Repos`, `Status`, `LastSync`.
- `Knowledge` — Engineering-Knowledge reads (not CRUD): `UnitsByProject`,
  `Units`, `Object`, `Search`, `Counts`, and issue numbers
  (`NumberForLine`, `LineByNumber`).
- `Resolver` — identity resolution: `Resolve` (canonical or qualified line
  form), `ResolveLine`.
- `Relations` — relationship traversal (from/to/upstream/downstream).
- `Timeline` — instance-line history (change logs + hashes).
- `Snapshot` — verified package reads.
- `Integrity` — store integrity verification + schema version.

The `Authoring` API is the stateless gateway from authoring representations
into CKOs and runtime state:

```go
report, err := runtime.Authoring.Validate(root)
result, err := runtime.Authoring.Compile(root)
sresult, err := runtime.Authoring.Sync(rt, repoPath, opts)
```

### machine — deterministic canonical JSON

```go
doc, err := machine.NewDocument(unit)      // (*Document, error)
data, err := machine.MarshalUnit(unit)     // ([]byte, error)
col, err := machine.NewCollection(domain, units)
```

- `Document` is the canonical machine projection of one CKO (schema
  `eka-cko-v2`); `Marshal` / `MarshalCompact` emit the deterministic JSON.
- `Collection`, `ContainerCollection`, and `Pagination` project domain queries
  and the `containers` query; `Page`, `FilterActive`, `FilterContainer` apply
  retrieval options.

### metadata — repository identity

```go
m, err := metadata.Parse(data)        // (Metadata, error)
dir := metadata.Find(path)            // nearest eka.yaml walk-up
```

`Metadata` holds `Version`, `Project`, `Name`, `Namespace`; `Parse` is strict
(unknown/duplicate keys refused, `version` must be 1, identifiers validated).

### compile, sync, view, contexts

- `compile.Compile(root)` returns `*compile.Result` (`Package`, `CKOs`,
  `Validation`); a failing gate is a `*compile.ValidationError`.
- `sync.Run(ws, repoPath, opts)` runs one pull/push against the workspace and
  returns a deterministic `*sync.Report`.
- `view.Build(name, graph, target)` constructs a named projection; `view`
  also exposes `Projections()`, `Aliases()`, and `HelpList()`.
- `contexts.New(rt)` wires the Context Engine; `engine.Build(subject,
  projectID, depth, opts)` constructs the Context Object.

## Versioning

- **Semantic versioning**, tag-driven. The version *is* the git tag — there is
  no version file. `scripts/bump.sh` computes and pushes the next tag:

  ```sh
  ./scripts/bump.sh patch   # 1.0.0 -> 1.0.1
  ./scripts/bump.sh minor   # 1.0.0 -> 1.1.0
  ./scripts/bump.sh major   # 1.0.0 -> 2.0.0
  ```

- **Standard version:** this library implements **EKA Standard 1.0**. The
  standard's own version axis (two-component `major.minor`) is independent of
  this library's semver — see the `eka-standard` README.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Design records and ADRs live in the EKA
knowledge system, not in this repository.

## License

Apache License 2.0.
