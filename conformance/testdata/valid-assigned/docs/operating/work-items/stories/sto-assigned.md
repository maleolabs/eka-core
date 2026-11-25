---
namespace: eka-valid-fixture
type: sto
id: assigned
instance-version: 1
revision: 1
execution-state: done
existence-state: active
dimension: requirements
author: Engineering Architecture
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on: []
assigned-to:
  - mbr:alice
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
  - date: 2026-08-05
    domain: execution-state
    from: todo
    to: in-progress
    by: Engineering Architecture
  - date: 2026-08-05
    domain: execution-state
    from: in-progress
    to: in-review
    by: Engineering Architecture
  - date: 2026-08-05
    domain: execution-state
    from: in-review
    to: done
    by: Engineering Architecture
---

# Assigned

## Description

A done work item carrying an assigned-to relationship.

## Acceptance Criteria

- AC1.
