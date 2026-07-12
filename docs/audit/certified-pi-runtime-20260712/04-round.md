# Certified round 04 — queue-aware runtime settlement correlation

- Started: `2026-07-12T10:03:17,500317989+08:00`
- Completed: `2026-07-12T10:03:19,339879693+08:00`
- Previous record SHA-256: `5b34b254e40491d3dd3449a125ba2163af86586acd31a3e55730a51c325681c8`
- Validation log SHA-256: `7ddf715a7731057b27865bad70aa2275e21c6883edb1063aeaba3e8d653dfc23`

## Audit

Inspected ordered `queue_update` caching and the `runtime_settled` gate at `server/internal/agent/pi_sdk_runtime/session.go:917–1013`. A normal settlement is deferred while steering/follow-up entries remain queued; the explicit bridge idle-timeout reason bypasses the gate so a stuck queue cannot wait forever.

## Finding and action

No new defect was found in the repaired state. No source change was made.

## Verification

Race-enabled queued-follow-up and timeout-escape tests passed; exact output is in `04-validation.log`.

## Residual risk

Correctness relies on ordered SDK queue updates. If an external bridge omits them, the hard-idle timeout remains the observable escape rather than a silent successful completion.

Status: **DONE**. This record was written before certified round 05 started.
