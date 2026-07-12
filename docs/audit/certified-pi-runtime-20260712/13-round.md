# Certified round 13 — tool event ordering, content, and location mapping

- Started: `2026-07-12T10:13:33,838376029+08:00`
- Corrected audit completed: `2026-07-12T10:14:03,116316830+08:00`
- Previous record SHA-256: `a1b0db1c00cf49aabfa68728d029a7e2e90cb336fccd044f7593d81ea1a47eb4`
- Authoritative validation SHA-256: `6fc54b9671d249dd500fa9c7863b732d38f134e2fc2fe53c26aab71af2b0b1fe` (`13-validation-v2.log`)
- Superseded log: `13-validation.log` named one nonexistent test and was rejected before this record.

## Audit

Inspected tool start/update/end normalization, result content, patch paths, and location deduplication at `server/internal/agent/pi_sdk_runtime/session.go:1141–1227,1934–1995,2014–2072`. Event order is preserved by the single stdout reader; tool turns do not terminate the containing prompt.

## Finding and action

No new defect was found. No source change was made.

## Verification

Exact tool mapping, multi-turn completion, and delayed-tool ordering tests passed in `13-validation-v2.log`.

## Residual risk

Location extraction recognizes structured paths and standard unified-diff headers. Nonstandard free-form tool output remains content-only rather than guessing a path.

Status: **DONE**. This record was written before certified round 14 started.
