---
namespace: eka-domain-valid
type: adr
id: 001-login-serialization
instance-version: 1
revision: 1
content-state: accepted
existence-state: active
dimension: decisions
domain: Architecture
author: Engineering
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on:
  - req:001-auth
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
  - date: 2026-08-05
    domain: content-state
    from: proposed
    to: accepted
    by: Engineering
---

# ADR-001 — Login serialization

## Context

Login data must be serialized across services.

## Decision

Serialize login identity as a structured token.

## Consequences

Tokens are verifiable without a round-trip.

## Alternatives Considered

Opaque session handles were rejected due to verification cost.
