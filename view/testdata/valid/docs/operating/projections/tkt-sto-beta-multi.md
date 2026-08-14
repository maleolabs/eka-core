---
namespace: eka-view-fixture
type: tkt
id: sto-beta-multi
instance-version: 1
revision: 1
author: Engineering Architecture
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from:
  - ctr:wave-1
  - sto:beta
  - ts:gamma
depends-on: []
---

> Generated — State Projection. Do NOT edit state here; refresh on read.

# Ticket — Beta (multi-reference)

## Commands

- run the sto-beta tests.

## Projected Status

Projected from the owner work item.

<!-- Fixture role: a ticket with TWO work item references in
     derives-from — exercises first-resolvable-wins in ticketTargets
     (sto:beta must win deterministically, not ts:gamma). -->
