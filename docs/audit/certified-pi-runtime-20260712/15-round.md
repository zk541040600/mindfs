# Certified round 15 — DeleteSession cascade and agent-pool resource ownership

- Started: `2026-07-12T10:15:12,482657122+08:00`
- Corrected audit completed: `2026-07-12T10:15:34,566436874+08:00`
- Previous record SHA-256: `28b8c68e0da0ecfc5898d34e305c6c2cb3fed1874a4a978057ee02463ea1ce03`
- Authoritative validation SHA-256: `be8b983a6fa7d5dc62145e2c37e3921a6b60dbb8c123a5cbb7d96d500ab384d7` (`15-validation-v2.log`)
- Superseded log: `15-validation.log` named a nonexistent cascade test and was rejected before this record.

## Audit

Inspected child-first cascade derivation and successful-delete cleanup at `server/internal/api/usecase/session.go:882–970`. Active turns are canceled for all targets first; after each manager deletion, legacy and configured agent-prefixed pool keys are closed before file/watcher cleanup.

## Finding and action

No new defect was found. No source change was made.

## Verification

The sub-session tree deletion and unpersisted Pi SDK runtime cleanup tests passed under race detection in `15-validation-v2.log`.

## Residual risk

An in-memory runtime for an agent removed from configuration and never persisted cannot be enumerated by name. Configured and persisted session owners are covered without adding a global pool scan API.

Status: **DONE**. This record was written before certified round 16 started.
