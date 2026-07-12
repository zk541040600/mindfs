# Certified round 18 — stderr secret redaction and diagnostic volume bounds

- Started: `2026-07-12T10:18:26,742237048+08:00`
- Completed: `2026-07-12T10:18:27,168332314+08:00`
- Previous record SHA-256: `974ef9a58656a265dc61a291d3f51b513a4bc6124ba2b5309a23d1db6a30a455`
- Validation log SHA-256: `fd3863660a8981d6c0110c374a0341a2fa7df1e35185fe1988bc5147ea85d561`

## Audit

Inspected precompiled patterns, scanner capacity, redaction order, and UTF-8-safe preview at `server/internal/agent/pi_sdk_runtime/session.go:31–38,476–487,2210–2235`. Redaction occurs before the 500-byte preview, so secrets beyond the final display bound cannot shift an unredacted prefix into output.

## Finding and action

No new defect was found. No source change was made.

## Verification

Verbose output proves the dedicated redaction and bound test ran and passed in `18-validation.log`.

## Residual risk

No heuristic can identify arbitrary natural-language secrets. Recognized credential syntax and long token-like values are redacted, and every line is bounded to limit unknown exposure.

Status: **DONE**. This record was written before certified round 19 started.
