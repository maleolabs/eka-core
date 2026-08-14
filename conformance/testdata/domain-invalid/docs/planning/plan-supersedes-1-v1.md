---
namespace: eka-domain-invalid
type: plan
id: supersedes-1
instance-version: 1
revision: 1
content-state: approved
planning-state: approved
existence-state: active
phase: mvp
dimension: planning
author: Engineering
created: 2026-08-05
updated: 2026-08-05
supersedes:
  - adr:001-a
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

# Plan Supersedes 1

## Objective

Demonstrate upward supersession.

## Scope

R12.

## Out of Scope

Nothing.
