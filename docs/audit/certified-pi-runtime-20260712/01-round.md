# Certified round 01 — Pool liveness and closed-session ownership

- Started: `2026-07-12T10:00:42,903083847+08:00`
- Completed: `2026-07-12T10:00:46,897314970+08:00`
- Previous record SHA-256: `2032245a038ef7987eb8089688872bf35421437c30b2225609cadb30627e9adb` (`00-baseline.log`)
- Validation log SHA-256: `356f911083c49d841428ee084f5f763a0232e9bf6b3b4967709625431be0e99e`

## Audit

Inspected `server/internal/agent/pool.go:59–90` and both Pi runtime `Closed()` implementations. Pool checks liveness under its mutex, removes a closed cached entry, then performs slow process creation outside the lock. Duplicate concurrent creation is still resolved by the existing second check.

## Finding and action

No new defect was found in the repaired state. No source change was made. Returning runtimes that do not expose `Closed()` remains intentional compatibility behavior; both MindFS Pi protocols now expose it.

## Verification

`/root/.local/go1.25/bin/go test ./server/internal/agent -run '^TestPoolReopensClosedPiSDKSession$' -count=1` → PASS.

## Residual risk

A future runtime that owns a subprocess but omits `Closed()` will not receive proactive eviction. Such a runtime must either implement the optional lifecycle contract or own its stale-session recovery.

Status: **DONE**. This record was written before certified round 02 started.
