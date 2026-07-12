# Certified round 16 — service shutdown, CloseAll, and child-process reaping

- Started: `2026-07-12T10:16:07,938452350+08:00`
- Corrected audit completed: `2026-07-12T10:16:52,930254569+08:00`
- Previous record SHA-256: `4c65b240614c09c2d851dbb4d0895614e2465eae4b9f626176d55db001aec3c0`
- Authoritative validation SHA-256: `a480ae4cc257236d5b9b4abef22e4b25c9e87d3d0a687082a68974e9963c16c9` (`16-validation-v2.log`)
- Superseded log: `16-validation.log` used the wrong Pool source range and was rejected before this record.

## Audit

Inspected Pool shutdown, runtime snapshot-close, session kill/fail-pending, reader drain, Wait, and unregister at `server/internal/agent/pool.go:374–405` and `server/internal/agent/pi_sdk_runtime/session.go:228–265,490–516,2185–2207`. Locks are released before slow child teardown.

## Finding and action

No new defect was found. No source change was made.

## Verification

Pool CloseAll behavior, post-close creation rejection, and real-bridge reap passed under race detection in `16-validation-v2.log`.

## Residual risk

No live systemd restart was performed because it would mutate production state. The process ownership contract is verified with real child processes in tests.

Status: **DONE**. This record was written before certified round 17 started.
