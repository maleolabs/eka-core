---
namespace: eka-domain-invalid
type: plan
id: mismatch-1
instance-version: 1
revision: 1
content-state: approved
planning-state: approved
existence-state: active
phase: mvp
dimension: planning
domain: Execution
author: Engineering
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on: []
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
  - date: 2026-08-05
    domain: content-state
    from: "-"
    to: draft
    by: Engineering
  - date: 2026-08-05
    domain: content-state
    from: draft
    to: review
    by: Engineering
  - date: 2026-08-05
    domain: content-state
    from: review
    to: approved
    by: Engineering
  - date: 2026-08-05
    domain: planning-state
    from: "-"
    to: draft
    by: Engineering
  - date: 2026-08-05
    domain: planning-state
    from: draft
    to: approved
    by: Engineering
  - date: 2026-08-05
    domain: phase
    from: "-"
    to: mvp
    by: Engineering
---

# Plan Mismatch 1

## Objective

Demonstrate a declared domain mismatch.

## Scope

R11.

## Out of Scope

Nothing.
