# Certified round 19 — Pi RPC lifecycle parity and shared diagnostic redaction

- Started: `2026-07-12T10:19:17,168011300+08:00`
- Fix verified: `2026-07-12T10:21:24,856266204+08:00`
- Previous record SHA-256: `a63162c76bdbbbf00e85e33b36ccaf49ba23073547d3545d1b2cadd7a51bd10e`
- Pre-fix audit log SHA-256: `c62ed7e8cb07c7291a33578c8e496a0a5ed4c8b25f7e215a94710fdc34b2cf0c` (`19-validation.log`)
- Authoritative post-fix validation SHA-256: `65007f6eb56242a4588a17dd4eb8b699b837859426ce61e7d50cb0d1f7a8c639` (`19-validation-v2.log`)

## Audit and reproduced finding

The retained Pi RPC rollback path had lifecycle parity for cwd, reader drain, startup retry, and closed-state eviction, but `19-validation.log` captured `server/internal/agent/pi/session.go` calling only `preview(scanner.Text())` for stderr. Unlike Pi SDK, bearer/API-key values were not redacted before journaling.

## Fix/optimization

Added the narrow shared security boundary `server/internal/agent/diagnostic/redact.go:1–24`. Both Pi SDK and Pi RPC retain their existing UTF-8-safe output bounds while calling the same credential redactor through their diagnostic-line pipeline. This removes duplicate regex ownership instead of copying another sanitizer.

Added `TestSanitizeDiagnosticLineRedactsSecretsAndBoundsOutput` to `server/internal/agent/pi/session_test.go`; the existing SDK integration regression continues to exercise the shared implementation.

## Verification

Race-enabled redaction, PWD, startup-retry, and CloseAll tests ran verbosely for both Pi packages; focused vet and `git diff --check` passed in `19-validation-v2.log`.

## Residual risk

Credential-pattern redaction cannot recognize arbitrary natural-language sensitive text. Both paths now cover bearer tokens, named secrets, and long token-like values and cap every emitted line.

Status: **DONE**. This record was written before certified round 20 started.
