# Design: MindFS web reliability and restart safety

## Data flow and failure boundary

```text
Browser click
  -> SessionService pending retry map
  -> WebSocket session.message / request_id
  -> WSHandler accepted job + StreamHub pending state
  -> usecase SendMessage
  -> session JSONL + binding SQLite
  -> Agent runtime / stream events
  -> StreamHub terminal replay
  -> browser request-scoped pending cleanup
```

A controlled restart cuts this flow at four boundaries: browser socket, in-memory `StreamHub`, detached message goroutine, and Agent runtime. The durable session manager is the authority that survives the cut.

## 1. Browser transport

Add one internal `sendPendingMessage` boundary in `SessionService` for request messages that participate in reconnect retry. It owns insertion/removal in `pendingMessages`, catches serialization/E2EE/WebSocket exceptions, removes only requests that were not sent, asks the connection loop to recover, and returns `false` to existing UI rollback code.

Extend the existing reconnect watchdog with a visible-page application-level ping interval. Reuse the existing single active probe and timeout; do not create a second heartbeat state machine. Any incoming message proves liveness and clears the probe. Hidden pages skip periodic OPEN-socket probes; focus/pageshow continue to probe immediately.

## 2. Authoritative pending response

Extend full session JSON with `pending: boolean`. `HTTPHandler` reads `StreamHub.IsSessionReplying(sessionKey)` for both GET and sync responses. `pendingUser` remains a separate optional exchange projection; sub-sessions can be pending without a user exchange, so `pending` must not be inferred only from `pendingUser != nil`.

The field is additive and backward compatible. The web `Session` type gains `pending?: boolean`; existing fallback behavior remains for older servers.

## 3. Write-ahead chat input

In `SendMessage`, snapshot the exchange history before the current turn, then append the user exchange before opening/sending to the Agent runtime. Build the Agent prompt from the snapshot, not from the now-mutated live session, so the current input appears exactly once. Persist the assistant exchange and aux data after settlement as before.

This deliberately preserves interrupted user input but does not synthesize or automatically replay an Agent answer. A failed/cancelled turn remains a user-only exchange, matching current behavior.

## 4. Controlled shutdown drain

`WSHandler` owns message-job accounting because it creates both external message goroutines and queued continuations. Use a mutex-protected counter plus an idle channel:

- external work increments only before shutdown begins;
- shutdown marks the handler closed to new external message jobs;
- queued continuations already accepted in `StreamHub` may still increment while a parent job is draining;
- the idle channel closes only when the complete accepted chain reaches zero;
- app shutdown cancels/closes Agent runtimes, then waits with a fixed timeout before shutting down the server.

Because queued continuations run after the pool is cancelled, they fail quickly but pass through write-ahead persistence and terminal error cleanup, preserving input without replaying side effects.

## 5. Restart metadata reconciliation

Session JSONL is the durable exchange authority; SQLite `sessions.updated_at` is a list/search projection. The append and metadata update cannot be atomic across the file and SQLite, so a process can exit after a valid exchange is appended but before the projection advances. On the first Session Manager database open after restart:

- scan valid exchange timestamps under the manager lock;
- advance only metadata timestamps that lag durable history;
- never move timestamps backward or rewrite JSONL;
- leave `agent_ctx_seq` unchanged for a user-only interrupted turn;
- persist the repaired timestamp before applying `AfterTime` list filters;
- emit an identifier-only repair count for operational evidence.

This converts an observed split-write window from permanent list invisibility into a one-time, idempotent projection repair.

## 6. Compatibility and safety

- Existing JSONL entries without request IDs continue to load.
- Request-scoped stream and terminal checks remain unchanged.
- No persistent automatic retry of interrupted prompts is introduced.
- Logs contain identifiers and lifecycle states only, never message content or credentials.
- Shutdown waiting is bounded; timeout is logged and process shutdown continues.

## Rollback

- Code rollback: restore the pre-task binary from `.mindfs/deploy-backups/` and restart `mindfs.service`.
- Data rollback is not expected: changes are additive JSON/API behavior and normal append-only exchanges. Before live restart, copy the running binary and record SQLite counts plus selected JSONL metadata.
- If heartbeat causes mobile churn, revert only the periodic OPEN-socket branch while retaining send failure handling and restart persistence fixes.
