# View Fixture

Convention document used by view integration tests.

The fixture is a conformant repository (asserted by `TestFixtureConforms`)
with artifacts across all five Engineering Domains in namespace
`eka-view-fixture`. Every artifact carries the state fields owned by its
type (validation.md Rule 4) with change-logs ending at the current
values (Rule 7).

## Execution

Two execution containers:

- `ctr:wave-0` — completed; holds `sto:legacy` via `tkt-sto-legacy`.
- `ctr:wave-1` — active; holds `sto:alpha`, `sto:beta`, `ts:gamma`,
  `bug:delta`, `ch:epsilon` via their tickets.

Tickets (all `tkt-`, under `docs/operating/projections/`):

| Ticket | derives-from | Fixture role |
|---|---|---|
| tkt-sto-alpha | ctr:wave-1, sto:alpha | regular ticket |
| tkt-sto-alpha-dup | ctr:wave-1, sto:alpha | second ticket referencing the already-covered `sto:alpha` — exercises dedup-by-identity in `WorkItemsForContainer` (the work item must appear once) |
| tkt-sto-beta | ctr:wave-1, sto:beta | regular ticket |
| tkt-sto-beta-multi | ctr:wave-1, sto:beta, ts:gamma | TWO work item references — exercises first-resolvable-wins in `ticketTargets` (`sto:beta` must win deterministically) |
| tkt-ts-gamma | ctr:wave-1, ts:gamma | regular ticket |
| tkt-bug-delta | ctr:wave-1, bug:delta | regular ticket |
| tkt-ch-epsilon | ctr:wave-1, ch:epsilon | regular ticket |
| tkt-unresolved | ctr:wave-1 | no work item reference — projects "unresolved" |
| tkt-sto-legacy | ctr:wave-0, sto:legacy | ticket of the completed container |

Work items (`sto-`, `ts-`, `bug-`, `ch-` under
`docs/operating/work-items/`) own the six execution-state values
planned/todo/in-progress/in-review/done plus `sto:legacy` (done) in the
completed container.

## Planning

Under `docs/planning/` (dimension `planning`):

| Artifact | content-state | planning-state | phase |
|---|---|---|---|
| scp:wave-2 (scp-wave-2-v1.md, versioned filename) | approved | — | mvp |
| epc:auth | review | — | — |
| plan:roadmap-2026 (plan-roadmap-2026-v1.md, versioned filename) | approved | approved | release |
| trc:spec-trace | draft | — | — |

Exercises the planning projection: phase context on scp-/plan-, the
planning-state value on plan-, and the draft/review/approved content
variants.

## Architecture

Under `docs/decisions/` (dimension `decisions`),
`docs/architecture/` (dimension `architecture`),
`docs/specifications/` (dimension `specifications`),
`docs/standards/` (dimension `standards`) and
`docs/vocabulary/` (dimension `vocabulary`):

| Artifact | content-state | Fixture role |
|---|---|---|
| adr:001-login-serialization | accepted | ADR variant value |
| adr:002-session-encoding | superseded | superseded ADR, replaced by adr:003 via `supersedes` (Rule 9) |
| adr:003-token-format | accepted | replacement of adr:002 |
| dec:001-api-shape | accepted | decision variant value |
| arc:system-architecture | approved | — |
| spec:auth-flow | draft | — |
| std:gofmt | review | — |
| gls:domain-terms | amended | — |

Exercises the architecture projection: the Decisions group merges adr-
and dec- (both content-state variants), plus the arc-/spec-/std-/gls-
groups.

## Discovery

Under `docs/intent/` (dimension `intent`),
`docs/requirements/` (dimension `requirements`) and
`docs/research/` (dimension `research`):

| Artifact | content-state |
|---|---|
| vis:product-vision | draft |
| str:go-to-market | review |
| req:onboarding | approved |
| fnd:market-research | approved |

## Operations

Under `docs/operations/` (dimension `operations`) and
`docs/records/` (dimension `records`):

| Artifact | content-state |
|---|---|
| run:deploy | approved |
| rel:release-1 | review |
