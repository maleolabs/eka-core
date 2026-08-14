---
namespace: eka-domain-valid
type: sto
id: login-email
instance-version: 1
revision: 1
execution-state: in-progress
existence-state: active
dimension: requirements
author: Engineering
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from:
  - ctr:gelombang-1
depends-on: []
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
  - date: 2026-08-05
    domain: execution-state
    from: "-"
    to: planned
    by: Engineering
  - date: 2026-08-05
    domain: execution-state
    from: planned
    to: todo
    by: Engineering
  - date: 2026-08-05
    domain: execution-state
    from: todo
    to: in-progress
    by: Engineering
---

# Story — Login by Email

## Description

As a user I can log in with my email.

## Acceptance Criteria

- Successful login returns a token.
