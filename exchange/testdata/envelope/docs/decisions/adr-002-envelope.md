---
namespace: eka-envelope-fixture
type: adr
id: 002-envelope
instance-version: 1
revision: 1
content-state: accepted
existence-state: active
dimension: decisions
author: Marij
created: 2026-11-18
updated: 2026-11-18
provenance: inferred
sourcePromptHash: 8d5e2f1c4b7a9036e1f2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8
confidence: 0.0
sourceCommitSha: 9f2c1ab
captureMeta:
  classifier: keyword-heuristic v1
  dedupeKey: dcc691003c1ed7b6149b70d2aba3692ae1d3faf827bab6a650d2c2f965275371
  title: decide the adr 035 envelope
validUntil: 2027-06-30
lastConfirmed: 2026-11-18
change-log:
  - date: 2026-11-18
    domain: existence-state
    from: "-"
    to: active
    by: Marij
  - date: 2026-11-18
    domain: content-state
    from: proposed
    to: accepted
    by: Marij
---

# ADR 002 — Envelope

## Context

Capture metadata is only useful if it survives the package boundary.

## Decision

Carry the whole envelope.

## Alternatives Considered

Rebuilding the claims from the content on every import; rejected because it
re-invents the title and the prompt hash from data that does not carry them.

## Consequences

Export and import tooling must not gain or lose a field.