# Certified round 17 — probe path provenance and cwd shadowing prevention

- Started: `2026-07-12T10:17:33,911565802+08:00`
- Corrected audit completed: `2026-07-12T10:17:53,948071950+08:00`
- Previous record SHA-256: `353c96d47d0a4f95199f8eed4bf3f059f33517ca9e60e846a57cdbc1b745c0a0`
- Authoritative validation SHA-256: `fd525fe8b61fd72633d0dcdc6e94541d359fb1a863c1f4c60ab2d0726ef0ce54` (`17-validation-v2.log`)
- Superseded log: `17-validation.log` reported `[no tests to run]` and was rejected before this record.

## Audit

Inspected explicit and default resolution at `server/internal/agent/pi_sdk_bridge/client.go:218–270`. Explicit ProbePath is validated directly. Defaults concatenate executable/install candidates before cwd/development candidates, preventing a service WorkingDirectory file from shadowing the bundled bridge.

## Finding and action

No new defect was found. No source change was made.

## Verification

Verbose output proves both executable-precedence and installed-share-layout tests ran and passed in `17-validation-v2.log`.

## Residual risk

An explicit ProbePath is trusted by design. Administrators who configure it own its provenance and permissions.

Status: **DONE**. This record was written before certified round 18 started.
