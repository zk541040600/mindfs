# Certified round 11 — extension UI request, response, and cancellation routing

- Started: `2026-07-12T10:12:23,269339768+08:00`
- Completed: `2026-07-12T10:12:27,101375950+08:00`
- Previous record SHA-256: `3fd994eb7f52afdca3c0f0a43ca55935760c4b59db455f66ff16e1c01ba0e8be`
- Validation log SHA-256: `c6512207ad8cc60247a57d8abfbf3d88389507cbe01e6cc4062459f505874f40`

## Audit

Inspected request normalization, pending-dialog tracking, method-matched response, and cancellation at `server/internal/agent/pi_sdk_runtime/session.go:576–610,1468–1500,1633–1656`. Only blocking dialog methods enter the pending map; notify/status-style events remain non-blocking.

## Finding and action

No new defect was found. No source change was made.

## Verification

Demo coverage and the real SDK extension UI round trip both passed; exact output is in `11-validation.log`.

## Residual risk

If a browser disconnects while a dialog is pending, the runtime remains correctly blocked until cancellation/turn abort or UI reconnection responds. This is an explicit interaction contract, not transport loss.

Status: **DONE**. This record was written before certified round 12 started.
