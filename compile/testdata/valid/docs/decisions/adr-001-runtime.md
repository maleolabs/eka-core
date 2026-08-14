---
namespace: eka-compile-fixture
type: adr
id: 001-runtime
instance-version: 1
revision: 1
content-state: accepted
existence-state: active
dimension: decisions
author: Engineering Architecture
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on:
  - sto:login-email
amends: []
validates: []
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering Architecture
  - date: 2026-08-05
    domain: content-state
    from: proposed
    to: accepted
    by: Engineering Architecture
---

# ADR-001 — Knowledge runtime

## Context

The knowledge must be synced between the canonical store and repositories.

## Decision

The snapshot directory is the transport.

## Consequences

Sync is deterministic and idempotent.

## Alternatives Considered

Manual file copies were rejected for lack of integrity guarantees.
