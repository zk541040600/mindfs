# Certified round 06 — partial-output recovery and replay safety

- Started: `2026-07-12T10:06:48,668273352+08:00`
- Completed: `2026-07-12T10:06:51,441306269+08:00`
- Previous record SHA-256: `f9faa48db227e68121ccf96089578fc8c9da49dbdf09ceb588bc0d1f844c2d75`
- Validation log SHA-256: `e5c66a0d2d4811839750b644f04aeeabdb7edabf7cc2d834e9462a72e718642d`

## Audit

Inspected transport classification and the recovery loop at `server/internal/api/usecase/session.go:1519–1525,3452–3525`. A closed transport is reopened for `continue` only after assistant output proves the original prompt began; stale SDK IDs follow their existing reopen path. A fresh process retries immediately, while ordinary transient failures retain delay/backoff.

## Finding and action

No new defect was found. No source change was made.

## Verification

The race-enabled two-generation bridge regression completed the partial response as `partial recovered`; exact output is in `06-validation.log`.

## Residual risk

No-output failures are intentionally not replayed because an accepted prompt may already have executed side-effecting tools. This favors correctness over transparent retry.

Status: **DONE**. This record was written before certified round 07 started.
