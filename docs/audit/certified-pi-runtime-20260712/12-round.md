# Certified round 12 — AskUser/Todo mapping and pending-question lifecycle

- Started: `2026-07-12T10:13:01,290192658+08:00`
- Completed: `2026-07-12T10:13:03,367375395+08:00`
- Previous record SHA-256: `7959f6b8700c836dfaaad20c781e8c93bb180cfde4a1c752bf8a523d416a1139`
- Validation log SHA-256: `d23604b3579a89be79ce216cfafb9f4c701bb6b5476bbb6870446cce3448cbb2`

## Audit

Inspected tool start/update/end mapping and answer routing at `server/internal/agent/pi_sdk_runtime/session.go:1141–1215,1229–1259,1437–1465`. AskUser enters pending state at start and leaves after successful answer or tool end; Todo emits normalized updates without becoming a blocking question.

## Finding and action

No new defect was found. No source change was made.

## Verification

The combined AskUser answer round trip and Todo update test passed; exact output is in `12-validation.log`.

## Residual risk

A malformed bridge event with an empty toolCallId cannot be answered meaningfully. It remains protocol-invalid rather than receiving a fabricated identifier.

Status: **DONE**. This record was written before certified round 13 started.
