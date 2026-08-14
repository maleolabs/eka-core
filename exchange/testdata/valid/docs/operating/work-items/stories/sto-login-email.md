---
namespace: eka-valid-fixture
type: sto
id: login-email
instance-version: 1
revision: 1
execution-state: in-progress
existence-state: active
dimension: operations
author: Engineering Architecture
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
    by: Engineering Architecture
  - date: 2026-08-05
    domain: execution-state
    from: "-"
    to: planned
    by: Engineering Architecture
  - date: 2026-08-05
    domain: execution-state
    from: planned
    to: todo
    by: Engineering Architecture
  - date: 2026-08-06
    domain: execution-state
    from: todo
    to: in-progress
    by: Engineering Architecture
---

# STO — Login via email

## Description

As a user, I can log in with my email address.

## Acceptance Criteria

- The login form accepts an email address.
- Sessions are created for authenticated users.
