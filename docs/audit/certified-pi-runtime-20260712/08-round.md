# Certified round 08 — runtime cwd, PWD, and environment override order

- Started: `2026-07-12T10:08:59,828510836+08:00`
- Corrected audit completed: `2026-07-12T10:09:29,057640280+08:00`
- Previous record SHA-256: `89b97fafe3db0d3d1c99e77123be118807b91230f95fcfb9cd3cc40c375a05aa`
- Authoritative validation SHA-256: `e12e354a6cea0b64f863f880fb840cec942193c9e06a353550163d489e92eb3b` (`08-validation-v2.log`)
- Superseded log: `08-validation.log` did not include the complete `cmd.Dir/cmd.Env` source range and was rejected before this record.

## Audit

Inspected bridge `--cwd`, `Cmd.Dir`, and environment construction at `server/internal/agent/pi_sdk_runtime/session.go:64–92,329–342`. Agent overrides are appended after inherited environment, and runtime-root `PWD` is appended last to match `Cmd.Dir`.

## Finding and action

No new defect was found. No source change was made. The first evidence excerpt was incomplete, so the round remained open for a corrected audit.

## Verification

`TestMergeEnvUsesRuntimeWorkingDirectory` passed in `08-validation-v2.log`.

## Residual risk

An empty runtime root deliberately leaves inherited PWD unchanged. A caller that needs relative-path isolation must provide RootPath.

Status: **DONE**. This record was written before certified round 09 started.
