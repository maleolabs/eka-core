# Contributing

Thank you for contributing to **eka-core**, the Go reference implementation of
the EKA Standard. These rules keep the repository safe, reviewable, and
traceable.

## Branching model

Two long-lived branches:

| Branch | Purpose |
|---|---|
| `main` | Stable / release. Only ever updated from `develop` via a pull request. |
| `develop` | Development. All work happens here or on branches cut from here. |

## Development workflow

1. **Always branch from `develop`.** All implementation work MUST be done on a
   new branch created from `develop` — `feature/*`, `fix/*`, `refactor/*`,
   `docs/*`, or `chore/*` — never directly on `main`.
2. **Optional worktree.** Heavy work MAY be done in a separate git worktree so
   the primary worktree stays isolated.
3. **Merge to `main` via PR from `develop`.** Merging to `main` MUST come from
   the `develop` branch through a GitHub pull request.

## Quality gate

Before opening a pull request, run and pass:

```sh
gofmt -l .        # must print nothing (all files formatted)
go vet ./...      # must pass with no findings
go test ./...     # must pass
```

The CI pipeline runs the same gate; a failing gate blocks merge.

## Design records

Architecture decisions, design records, and ADRs are Engineering Knowledge and
live in the EKA knowledge system — not in this repository. Record significant
decisions there, not as free-floating docs.

## Language and style

- Code and doc comments are written in English.
- Keep the public API stable within a semantic version; break it only across a
  major version bump.
