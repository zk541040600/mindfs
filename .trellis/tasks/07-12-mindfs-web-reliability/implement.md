# Implementation plan

## 1. Lock in failing regressions

- [x] Add HTTP session response tests for explicit active/inactive `pending`.
- [x] Strengthen the Pi cancellation persistence test to read the user exchange while the fake runtime is still blocked.
- [x] Add WSHandler message-job shutdown/drain concurrency tests.
- [x] Add Playwright coverage for a thrown `session.message` WebSocket send.
- [x] Add a restart-store regression that simulates a durable JSONL append with stale SQLite metadata.

## 2. Backend contract and persistence

- [x] Add `pending` to full session GET/sync responses using `StreamHub.IsSessionReplying`.
- [x] Write chat user exchange before the long Agent operation.
- [x] Snapshot pre-turn exchanges and use that snapshot for initial/stale-runtime prompt construction.
- [x] Remove the old end-of-turn duplicate user append and preserve assistant/aux sequence numbering.
- [x] Persist optional request IDs and reject/replay duplicate completed or interrupted browser requests without automatic side-effect replay.
- [x] Reconcile SQLite `updated_at` from newer valid JSONL exchanges before list/count time filters after restart.

## 3. Shutdown lifecycle

- [x] Track external `session.message` goroutines and queued continuations in `WSHandler`.
- [x] Reject new external message jobs once shutdown starts while allowing already accepted queue drain.
- [x] In `app.Start`, mark message shutdown, close/cancel Agent runtimes, await tracked jobs with a bound, then shut down the HTTP server.

## 4. Browser transport

- [x] Centralize tracked request send cleanup for message/slash requests.
- [x] Convert send exceptions/races to `false`, remove unsent retry state, and kick connection recovery.
- [x] Add a visible-page periodic probe using the existing ping/pong state machine and a mobile-safe timeout.
- [x] Add `pending?: boolean` to the web session contract.
- [x] Reconcile authoritative `pending:false` with request-ID-guarded cleanup of stale local stream state.

## 5. Focused verification

- [x] `go test ./server/internal/api ./server/internal/api/usecase ./server/app -count=1`
- [x] Race-test lifecycle/request/restart-metadata cases, including 5 repetitions of the accepted queued shutdown drain and metadata reconciliation paths.
- [x] `cd web && npx playwright test tests/app-pending-state.spec.ts` (`12/12`).
- [x] `cd web && npm run typecheck && npm run build`.
- [x] `node --check server/internal/agent/pi_sdk_bridge/probe.mjs`.
- [x] `git diff --check`.

## 6. Full verification and live restart

- [x] `go test ./... -count=1`.
- [x] `go vet ./...`.
- [x] Full Playwright suite (`21/21`).
- [x] Build versioned candidate binaries without replacing the running binary.
- [x] Snapshot running binary hash, service PID, DB integrity/counts, binding counts, all session JSONL hashes, and fresh journal evidence.
- [x] Install candidates with rollback copies and run two controlled service restarts.
- [x] Verify process/hash/health, relay HTTP 101, session/binding invariants, authoritative pending, production WebSocket ping/pong, and metadata reconciliation.
- [x] Confirm all 32 session metadata timestamps match durable JSONL and all 58 JSONL hashes remain unchanged across the final restart.

## 7. Finish

- [x] Perform full-scope code/spec review and record the restart projection invariant in `.trellis/spec/backend/database-guidelines.md`.
- [x] Record validation evidence, deployment SHA/PIDs, rollback paths, and residual agent-configuration risks in the task.
- [x] Present the required one-shot commit plan for confirmation.
- [x] Commit only user-approved files; do not include unrelated or operational artifacts.
- [ ] Validate task metadata and archive the task; record the completed session afterward.
