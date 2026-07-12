# Certified round 10 — auto-retry recovery and non-terminal retry boundaries

- Started: `2026-07-12T10:11:09,023558394+08:00`
- Corrected audit completed: `2026-07-12T10:11:50,955647861+08:00`
- Previous record SHA-256: `48620065f3660f3b774532578d1d58b266b82fbcec6e1b0411011fa8ede7606a`
- Authoritative validation SHA-256: `9eecae3dddf4247776f6188bb83aa5ae63ffd17dd6144fc03ca6aba199d9510f` (`10-validation-v2.log`)
- Superseded log: `10-validation.log` did not exercise the exact `willRetry` and `turn_end` tests and was rejected before this record.

## Audit

Inspected event dispatch and recovery/terminal handlers at `server/internal/agent/pi_sdk_runtime/session.go:635–653,930–984,1041–1063`. Successful retry clears transient error; exhausted retry retains final error; retrying agent_end and model-level turn_end do not complete the whole prompt.

## Finding and action

No new defect was found. No source change was made. Generic compaction events share recovery-message normalization but are not treated as prompt completion.

## Verification

Two auto-retry outcome tests plus retrying agent_end and turn_end boundary tests passed in `10-validation-v2.log`.

## Residual risk

Malformed retry events without `success` or error text remain conservative: they cannot prove success and therefore do not clear a prior error.

Status: **DONE**. This record was written before certified round 11 started.
