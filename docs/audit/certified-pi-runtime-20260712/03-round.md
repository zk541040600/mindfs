# Certified round 03 — request correlation and pending-command cleanup

- Started: `2026-07-12T10:02:36,073731106+08:00`
- Completed: `2026-07-12T10:02:38,270668433+08:00`
- Previous record SHA-256: `f170950e4a68aabfa0baec166ea8e1f8100bcad862014849706edd5d9c0b7e4f`
- Validation log SHA-256: `45461eeb3c9881c1530f2f6a642b27ddaeff3f83370311a35ec461235a43869c`

## Audit

Inspected request registration, timeout deletion, response delivery, process failure fan-out, and Close at `server/internal/agent/pi_sdk_runtime/session.go:349–453,526–539,2185–2207`. Each request uses a buffered one-result channel; timeout/close removes its map entry; process failure atomically detaches the whole map before non-blocking notification.

## Finding and action

No new defect was found. No source change was made. Pending extension UI is also cleared when transport ownership ends.

## Verification

`TestRuntimeCloseCleansPendingRequests` passed; exact output is in `03-validation.log`.

## Residual risk

The protocol assumes request IDs are unique. Internally generated IDs are monotonic; extension UI IDs originate from the bridge contract and duplicate IDs would be rejected/overwritten by that boundary.

Status: **DONE**. This record was written before certified round 04 started.
