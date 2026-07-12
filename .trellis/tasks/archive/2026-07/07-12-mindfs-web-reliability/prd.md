# Stabilize MindFS web connectivity, restart persistence, and interaction responsiveness

## Goal

Make the MindFS browser reliably recover from mobile/relay disconnects and controlled service restarts without silently dropping accepted user input, leaving stale `pending` UI, or ignoring user actions.

## Requirements

1. Browser connection recovery must detect half-open WebSockets as well as CLOSED/CONNECTING sockets, while avoiding background-page reconnect churn.
2. Message and slash-command send failures must resolve to an explicit `false` result, remove unsent retry state, trigger connection recovery, and allow the UI to show an observable error instead of remaining optimistic forever.
3. Full session responses must include an authoritative `pending` boolean. A fresh server process with no active turn must return `pending:false`, allowing the browser to clear stale pre-restart state.
4. A chat user exchange must be durably appended before the long-running Agent turn begins. Prompt construction must not duplicate that just-written exchange.
5. Controlled shutdown must reject newly submitted message jobs, cancel runtime work, wait for accepted jobs to finish their persistence path within a bound, and drain already accepted queued turns through that same path.
6. Existing request-ID isolation, stale terminal rejection, stream replay, cancellation semantics, session binding persistence, and backward-compatible JSON history loading must remain intact.
7. Do not automatically replay an ambiguous Agent turn after restart; preserve the user input and surface interruption/error state rather than risking duplicate tool side effects.
8. Do not expose credentials, prompts, or private session content in new logs or test evidence.
9. Treat session JSONL as the durable exchange authority and SQLite session metadata as a repairable projection: after an interrupted append, the next manager open must advance stale `updated_at` metadata without rewriting history or advancing Agent context for a user-only turn.

## Acceptance Criteria

- [x] A backend regression proves full session payloads return `pending:true` for an active `StreamHub` turn and `pending:false` after it is cleared or after a fresh process state.
- [x] A use-case regression proves the current user exchange is readable from the session manager while the Agent call is still blocked, before cancellation/terminal settlement.
- [x] Existing successful, failed, cancelled, recovered, queued, and request-scoped turn tests still pass without duplicated user exchanges or prompt history.
- [x] Shutdown lifecycle tests prove new external message work is rejected after shutdown begins, accepted work is awaited, and an already accepted queued continuation remains part of the drain.
- [x] A Playwright regression proves a thrown WebSocket send produces a visible send failure and removes the optimistic pending indicator instead of throwing out of the handler.
- [x] Visible-page heartbeat/probe logic detects a stale OPEN connection and reconnects within a bounded interval; hidden pages do not perform periodic probe churn.
- [x] A restart-storage regression proves JSONL exchanges newer than SQLite metadata are reconciled before `List(AfterTime)` filtering and persisted back to the metadata projection.
- [x] Focused Go tests, relevant race tests, `go test ./...`, `go vet ./...`, bridge syntax check, web typecheck/build, full Playwright, and `git diff --check` pass.
- [x] Controlled live restarts preserve session/binding counts and JSONL hashes, return healthy service status, reconnect the relay, reconcile stale metadata, and clear stale browser pending state.
- [x] Task evidence records exact commands, results, remaining risks, and rollback information before archive.

## Constraints

- Preserve all pre-existing dirty worktree changes; do not reset or overwrite them.
- Keep changes at existing ownership boundaries (`SessionService`, HTTP session contract, usecase persistence, `WSHandler` lifecycle, app shutdown).
- No external relay/account mutation and no automatic replay of potentially side-effecting interrupted prompts.
- Service restart is authorized by the user's request, but only after build/tests and a reversible binary/data snapshot.
