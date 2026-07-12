# Runtime and code evidence

Date: 2026-07-12
Task: `07-12-mindfs-web-reliability`

## Observed runtime facts

- The live service is `/root/mindfs/mindfs -foreground -addr 10.23.50.137:7331`, started by `mindfs.service`.
- The deployed binary SHA-256 matches the staged `cancel-pending-fix` binary and reports `v0.4.0-57-g37ff996-dirty-cancel-pending-fix`.
- The relay control WebSocket remains connected. Its public health endpoint returns `401`, which current code classifies as `public_health.auth_required` rather than a reconnect failure.
- Browser WebSockets repeatedly connect and close with normal/EOF close codes. Several backend `session.done` events occurred while the browser had no live WebSocket, so completion replay is a required correctness path.
- Screenshots at 11:03 and 11:35 show the UI stuck at `已发送，等待响应...` with user messages already visible.
- SQLite `PRAGMA quick_check` for `/root/.mindfs/sessions/session-list.db` is `ok`; the database contains 32 sessions and 34 agent bindings.
- The session JSONL corresponding to the restart incident still contains the 10:54, 10:58, and 11:00 user exchanges. The reported loss is not wholesale database corruption, although an in-flight persistence window still exists.
- Controlled service restarts occurred at 10:54, 11:28, and 12:03. The journal contains user-only turns after cancellation/timeouts, confirming that the UI can remain pending even when durable history has settled.

## Confirmed contract defects

### 1. Restart cannot authoritatively clear browser pending state

`web/src/App.tsx` reads an optional `fullSession.pending` field in `restoreActiveSession` and clears local pending when the server returns `false`. `server/internal/api/http.go:sessionResponse` does not emit `pending`, and the sync endpoint explicitly passes no pending user. After a server restart the in-memory `StreamHub` is empty, but the browser retains its local pending refs and receives no terminal replay from the new process. Without an explicit `pending:false`, the stale UI can remain stuck.

### 2. Chat input is persisted after the long-running Agent call

`server/internal/api/usecase/session.go:SendMessage` currently calls `manager.AddExchangeForAgent(..., "user", ...)` only after the Agent send/recovery path settles. A process exit before that point can lose an already accepted input. Prompt construction must continue to use the history before the current user exchange after moving the write earlier.

### 3. Shutdown does not wait for message goroutines

`server/app/server.go` cancels/closes the agent pool and calls `server.Shutdown`, but `WSHandler` launches `session.message` work in detached goroutines. HTTP shutdown does not wait for those jobs (and WebSockets are hijacked connections). The process may exit before cancellation persistence completes. Existing queued messages are also only in `StreamHub` memory and need to be drained through the normal persistence path during controlled shutdown.

### 4. Failed browser sends can bypass rollback

`SessionService.sendMessage` inserts a pending message and directly returns `sendWSMessage`. If the socket closes between checks, `sendWSMessage` can return `false` while the pending map still retains the request. If WebSocket/E2EE send throws, the promise rejects and `App.tsx` never reaches its `!sent` rollback branch. This produces either a stuck optimistic UI or a later ghost resend.

### 5. OPEN sockets are not periodically probed

The reconnect watchdog handles missing, closing, and timed-out connecting sockets. It does not probe an OPEN socket unless focus/pageshow/online/visibility fires. A half-open mobile/VPN/relay path can therefore remain apparently connected until lower-layer timeouts close it.

## Existing work that must be preserved

The dirty worktree already contains request-scoped stream/terminal IDs, stale callback rejection, replay generation isolation, Pi runtime cancellation/settlement fixes, and Playwright regressions. These changes are the starting point, not disposable work. New fixes must preserve request identity and terminal replay semantics.

## Validation implications

- Add a backend response contract regression for explicit `pending:true/false`.
- Strengthen the existing cancellation test to prove the user exchange is durable before turn settlement.
- Add shutdown job-tracker concurrency tests, including queued continuation admitted during drain and new external work rejected.
- Add a browser regression where WebSocket send throws and the UI reports failure/clears pending.
- Run focused Go/Playwright tests, race tests for lifecycle code, full Go test/vet, frontend typecheck/build, full Playwright, and `git diff --check`.
- Perform an authorized controlled restart only after taking DB/session/binding snapshots; verify counts and latest-session content metadata before/after, service health, relay connection, and browser/API pending reconciliation.

## Implementation and controlled-restart evidence

All planned backend and browser changes were implemented and validated on 2026-07-12. Full verification passed:

- `go test ./... -count=1`
- `go vet ./...`
- `go test -race ./server/internal/session -run TestManagerReconcilesExchangeAppendedBeforeMetadataUpdate -count=5`
- focused API/usecase/app lifecycle and request-ID tests, including 5 race repetitions of accepted queued shutdown drain
- `npm run typecheck`, `npm run build`, and full Playwright `21/21`
- Pi bridge `node --check`, `git diff --check`, and Trellis context validation

The first controlled deployment (`20260712-143812-reliability`) changed PID `4048012` to `615049`; candidate and running SHA-256 both equaled `bb7f43896349b8f913750ec43789c98350200fdade0ab9b4fae2077437520d29`. Relay reconnection returned HTTP 101 and SQLite remained healthy with 32 sessions and 34 bindings.

That restart produced decisive split-write evidence: one session JSONL gained a valid user exchange at the exact shutdown time while its SQLite `updated_at` and Agent context cursor remained at the prior exchange. The message was preserved, but incremental session listing could treat it as stale indefinitely. A failing-first regression reproduced `List(AfterTime)` returning zero sessions.

The follow-up fix treats JSONL as the exchange authority and reconciles stale SQLite timestamps on first manager open without modifying JSONL or advancing Agent context. The second controlled deployment (`20260712-152338-reconcile`) changed PID `615049` to `823162`; candidate and running SHA-256 both equaled `25ad935fa8f703b7ceca2b09ce717e0f022ad107964f66f2f9defb550ae159f0`. Production evidence then showed:

- relay HTTP 101;
- fresh Chromium page HTTP 200 with one WebSocket;
- visible-page stale probe sent `ping` and received `pong`;
- no page errors or visible connection error;
- session API returned `pending:false`, 8 exchanges, and replying count 0;
- `[session/store] metadata.reconciled root=root sessions=2`;
- the observed stale DB timestamp advanced from `2026-07-12T05:37:44.366055627Z` to the JSONL exchange time `2026-07-12T06:41:30.484321008Z`;
- all 32 session metadata rows matched their latest valid JSONL timestamps;
- all 58 JSONL hashes were unchanged across the second restart and no invalid JSONL records were found.

Detailed deployment facts and rollback paths are in `live-deploy-20260712-143812-reliability.md` and `live-deploy-20260712-152338-reconcile.md`.

## Remaining runtime risks

Gemini still reports a missing API key and Augment reports a `session/new` initialization error after service start. These are existing agent-specific configuration/runtime issues; Pi/Web transport, persistence, and pending-state acceptance evidence is green. The service remained active, the relay remained connected, and no task-related panic/fatal error appeared.
