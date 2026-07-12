# Certified round 02 — Pi SDK pipe drain and process wait ownership

- Started: `2026-07-12T10:01:48,179886988+08:00`
- Completed: `2026-07-12T10:01:48,824991515+08:00`
- Previous record SHA-256: `3ce93ae104e5575911d8080a12ec5b1bebc609661a2377c08e720dde3cebf235`
- Validation log SHA-256: `d2965bfe5f85d60905521d21d961b080ccde6f68c1656151ac36227c5296fb10`

## Audit

Inspected startup goroutine ownership and `readLoop`/`stderrLoop`/`waitLoop` at `server/internal/agent/pi_sdk_runtime/session.go:118–130,456–523`. Both readers are registered before Wait; EOF lets Wait own the terminal error, while a non-EOF stdout failure fails requests and kills an unusable protocol process.

## Finding and action

No new defect was found. No source change was made. The current ordering preserves buffered tail events and one process reaper.

## Verification

Focused EOF and exit-reason tests passed; exact output is in `02-validation.log`.

## Residual risk

A descendant that incorrectly inherits bridge pipe descriptors could delay reader completion. Supported bridge children do not inherit these protocol descriptors, and intentional Close kills the bridge.

Status: **DONE**. This record was written before certified round 03 started.
