# Certified round 14 — hard idle timeout containment and poisoned-runtime disposal

- Started: `2026-07-12T10:14:33,169773864+08:00`
- Completed: `2026-07-12T10:14:35,821369514+08:00`
- Previous record SHA-256: `bcaa16a1e312a7a4b82784a355e46e2ed897cece677692765abb45a5cf675e53`
- Validation log SHA-256: `8198fce3eac7be3678a2fc571f663869f2b52f380e0c71e8b51e0730321df7cd`

## Audit

Inspected the exact timeout classifier and no-response disposal branch at `server/internal/api/usecase/session.go:1519–1525,2377–2387`. Only `Pi SDK prompt idle timeout` discards this session's cached runtime; unrelated errors are preserved and no ambiguous prompt is replayed.

## Finding and action

No new defect was found. No source change was made.

## Verification

The race-enabled poisoned-runtime disposal regression passed; exact output is in `14-validation.log`.

## Residual risk

The timed-out message is intentionally not auto-replayed. Users see the original error; their next send opens a clean runtime without risking duplicated tools.

Status: **DONE**. This record was written before certified round 15 started.
