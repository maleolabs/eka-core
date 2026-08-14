---
namespace: eka-valid-fixture
type: adr
id: 001-exchange
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

# ADR-001 — Exchange serialization

## Context

The repository must be exchangeable between EKA installations.

## Decision

The RSF v1.0 projection is the official serialization.

## Consequences

Export and import tooling can rely on one deterministic format.

## Alternatives Considered

Custom JSON schemas were rejected for lack of a specification.
