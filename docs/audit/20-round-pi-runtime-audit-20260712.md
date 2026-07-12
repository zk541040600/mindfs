# MindFS Pi runtime 20-round audit

Date: 2026-07-12
Goal: `mrh1nc0m-0wm1zi`
Scope: MindFS repository only. `/root/.pi`, installed Pi packages, CCH, external services, and production state are out of scope.

## Authoritative certified sequence

The completion authority is now `docs/audit/certified-pi-runtime-20260712/README.md` and its 20 immutable per-round records/logs. This fresh sequence ran from 10:00:15 to 10:25:20 after an independent auditor correctly rejected retrospective evidence.

- **Baseline before certified Round 01:** `docs/audit/certified-pi-runtime-20260712/00-baseline.log`
- **Twenty ordered records:** `docs/audit/certified-pi-runtime-20260712/01-round.md` through `20-round.md`
- **Per-round raw validation:** the authoritative log named in each record
- **Machine-checked hash/time chain:** `docs/audit/certified-pi-runtime-20260712/chain-verification.log` (`result=PASS`, 20 records, all 19 `record mtime < next validation birth time` boundaries)
- **Final full matrix:** `docs/audit/certified-pi-runtime-20260712/20-validation.log`

Round 19 of the certified sequence found and fixed one additional defect: the Pi RPC rollback path truncated stderr but did not redact credentials. Both Pi runtimes now reuse `server/internal/agent/diagnostic/redact.go` before their existing output bounds; race tests, full Go test/vet, web build/typecheck, and Playwright 18/18 pass.

The older table and narrative below are retained as **historical root-cause context only**. Earlier claims about repeated counts are not completion evidence; only exact commands/output in the certified directory are authoritative.

## Historical round summary (non-authoritative)

| Round | Distinct audit theme | Evidence and conclusion | Change | Verification | Status |
|---:|---|---|---|---|---|
| 1 | Pi SDK startup and process lifecycle | Traced `Pool.GetOrCreate` → `pi_sdk_runtime.OpenSession` → bridge process read/stderr/wait loops and the API send path. A closed Pi SDK session stayed cached in `Pool`, so later calls returned the dead session instead of starting a replacement. The regression test failed before the fix with `GetOrCreate returned the closed pi-sdk session`. Evidence: `server/internal/agent/pool.go:59`, `server/internal/agent/pi_sdk_runtime/session.go:50`, `server/internal/agent/pi_sdk_runtime/session.go:459`, `server/internal/api/usecase/session.go:2044`. | `Pool.GetOrCreate` now evicts sessions that report `Closed`; Pi SDK exposes its existing lifecycle state through `Closed`. Added one low-noise eviction log, unexpected process exit code/error logging, and stderr scanner error logging. Regression: `server/internal/agent/pool_test.go:170`. | PASS: `/root/.local/go1.25/bin/go test ./server/internal/agent -run '^TestPoolReopensClosedPiSDKSession$' -count=1`; `/root/.local/go1.25/bin/go test ./server/internal/agent/pi_sdk_runtime ./server/internal/agent -count=1`; `/root/.local/go1.25/bin/go test -race ./server/internal/agent -run '^TestPoolReopensClosedPiSDKSession$' -count=1`; `git diff --check`. | Done |
| 2 | Mid-turn Pi bridge exit and safe recovery | Followed `Service.SendMessage` and `recoverAgentTurn`. After Pi had streamed partial assistant text and its bridge exited, recovery repeatedly sent `continue` to the already closed runtime; the closed error was not eligible for reopen. Even a successful reopen would then wait the generic 30-second retry delay. The deterministic regression failed before the fix with three recovery attempts stuck on `pi sdk runtime session closed`/deadline. Evidence: `server/internal/api/usecase/session.go:2330`, `server/internal/api/usecase/session.go:3427`. | Recovery now treats a closed runtime as reopenable only after assistant output proves the original prompt started, avoiding blind replay when no output exists. A newly opened runtime is retried immediately instead of sleeping 30 seconds. Regression simulates first bridge emitting `partial ` then exiting normally and the replacement completing with `recovered`: `server/internal/api/usecase/usecase_test.go:2018`. | PASS: focused recovery/classification tests; full usecase package; focused race test; `git diff --check`. | Done |
| 3 | stdout/stderr drain, EOF, and process-exit error propagation | Full-package validation reproduced an intermittent real failure: `cmd.Wait()` closed `StdoutPipe` while the reader still had buffered Pi events, producing `read pipe: file already closed`, losing the partial chunk, and turning recovery into `no_response`. This violated Go's pipe lifecycle contract. EOF also reached pending requests as opaque `E_CLOSED: EOF`, while waiters lost the process exit reason. Evidence: `server/internal/agent/pi_sdk_runtime/session.go:110`, `server/internal/agent/pi_sdk_runtime/session.go:437`, `server/internal/agent/pi_sdk_runtime/session.go:471`. | stdout/stderr readers now drain before `Wait`; non-EOF read failure terminates the unusable process. Session closure preserves a normalized process-exit error, and pending EOF becomes `pi sdk runtime process exited` rather than bare EOF. | PASS: partial-exit recovery repeated 20 times; exit-error tests repeated 20 times; full Pi runtime, agent, and usecase packages; focused race test repeated 10 times; `git diff --check`. | Done |
| 4 | Turn cancellation ownership and concurrent lifecycle | Audited API cancellation, Pi abort handling, settlement, shutdown, and the shared `TurnCanceler`. `turnID` was modified once with atomic access outside the mutex and elsewhere with ordinary locked access. The race regression produced concrete atomic-vs-non-atomic read/write races in `Begin` and `End`; concurrent Begins could also overwrite the current turn with a lower ID. Evidence: `server/internal/agent/types/types.go:448`. | Removed the mixed atomic strategy. One mutex now assigns the monotonic ID and current cancel function as a single linearizable state change. Added concurrent lifecycle and old-End/current-turn ownership tests. | PASS: race test failed before fix and passed 10 repetitions after; Pi cancel tests passed 10 repetitions; full types, Pi runtime, and usecase packages; `git diff --check`. | Done |
| 5 | Pi SDK command environment and runtime working directory | `OpenSession` always supplied a non-nil `cmd.Env`. Go therefore does not update `PWD` when `cmd.Dir` changes; inherited `PWD` could point at the MindFS server directory while Pi actually ran in the managed root. Tools reading `PWD` rather than `cwd()` could resolve paths against the wrong workspace. Evidence: Go `os/exec.Cmd.environ` behavior plus `server/internal/agent/pi_sdk_runtime/session.go:68`. | Runtime environment construction now appends the selected Pi working directory as the final `PWD`, while preserving explicit agent environment override precedence. | PASS: `TestMergeEnvUsesRuntimeWorkingDirectory` repeated 10 times; full Pi SDK runtime package; `git diff --check`. | Done |
| 6 | Pi SDK startup metadata and resumable session identity | Production startup responses were decoded best-effort: malformed JSON or a missing `sessionId` silently left `session.sessionID` at the MindFS pool key. That synthetic key could later be persisted as the Pi SDK resume ID, causing an `unknown session` after process recreation. The bundled production bridge explicitly promises `runtime.sessionId`; test runtimes intentionally do not. Evidence: `server/internal/agent/pi_sdk_runtime/session.go:130`, bridge `start_sdk_runtime` response. | Startup metadata decoding is now explicit and error-returning. Production starts require a non-empty SDK `sessionId`; deterministic test scenarios retain their synthetic-key behavior. Malformed metadata closes the just-started process and fails startup instead of poisoning future resume state. | PASS: all `TestApplyStartResponse*` tests repeated 10 times; full Pi runtime and agent packages; `git diff --check`. | Done |
| 7 | Thinking-mode state refresh and closed-runtime visibility | `ListModes` discarded `refreshState` errors and returned cached choices with `nil` error. A dead Pi bridge therefore appeared healthy to capability/UI callers, hiding the disconnection and delaying recovery. The regression on a closed runtime failed before the fix with error `<nil>`. Evidence: `server/internal/agent/pi_sdk_runtime/session.go:1543`. | Propagate the state refresh error and return no stale mode list when the runtime cannot answer. No fallback state or extra cache was added. | PASS: closed-session and normal model/mode tests repeated 10 times; full Pi runtime and agent packages; `git diff --check`. | Done |
| 8 | Pi auto-retry/compaction event normalization | The SDK contract emits `auto_retry_start.errorMessage`, then `auto_retry_end{success:true}` without a message on recovery, or `auto_retry_end{success:false,finalError}` on exhaustion. MindFS treated all recovery-family messages as terminal errors, never cleared the start error on success, and ignored `finalError` on failure. Regressions confirmed both wrong states. Evidence: SDK event declaration and `server/internal/agent/pi_sdk_runtime/session.go:906`. | Pass the event type into normalization; clear transient error state on successful retry and record `finalError` on failed retry. Existing status emission remains unchanged. | PASS: retry success/failure tests failed before fix and passed 20 repetitions after; full Pi runtime and agent packages; `git diff --check`. | Done |
| 9 | Terminal event boundary across tools, retry, and compaction | Audited bridge prompt watchdog/drain logic and Go handling for `message_end`, per-turn `turn_end`, retrying `agent_end`, production `agent_end{promptDone:false}`, and authoritative `runtime_settled`. The bridge delays settlement while tool/queue/entry work remains; Go ignores non-terminal boundaries and deduplicates terminal completion. No new defect was confirmed, so no speculative change was made. Evidence: bridge prompt state around `probe.mjs:2470`; Go handlers around `session.go:827–1008`. | No code change. Existing boundary is intentionally conservative and avoids both early completion and indefinite message-end fallback. | PASS: five repetitions each of delayed tool multi-turn, text-before-tool, retrying agent-end, raw agent-end/runtime-settled, and turn-end-only tests (72s aggregate); `git diff --check`. | Done |
| 10 | Busy runtime, follow-up queue, and shared settlement waiters | Audited `RuntimeActivity`, `SendFollowUp`, waiter registration/removal, cancellation, and one-settlement/multiple-waiter behavior. Tests confirm a queued follow-up keeps the active run alive and the authoritative settle wakes both intended callers. No functional defect was found. One `settlementEpoch` field was incremented but never read, so it represented misleading dead state rather than protection against stale signals. Evidence: `session.go:1278–1402`. | Removed the unused epoch field/increment; retained the simple bounded waiter map and existing ordering. | PASS: busy follow-up and shared-settlement tests repeated 20 times; shared-settlement race test repeated 10 times; zero remaining `settlementEpoch` references; `git diff --check`. | Done |
| 11 | Extension UI and AskUser round-trip lifecycle | Traced Pi SDK UI request creation → Go pending maps/backlog → stream event → frontend handling → WS response → bridge resolver. Blocking dialogs are retained preferentially in the bounded pre-subscriber backlog; duplicate/mismatched responses fail explicitly; close/cancel removes pending dialogs. `notify` and chrome events are fire-and-forget, while select/confirm/input/editor remain answerable. No new defect was confirmed. Evidence: `session.go:559–601`, `session.go:1424–1471`, `probe.mjs:2889`, frontend `App.tsx:9297`. | No code change; avoided altering user-modified frontend/WS files because the verified contract already passes. | PASS: backlog, UI demo, AskUser/Todo round-trip tests repeated 10 times; real Pi SDK extension UI round-trip repeated 3 times; `git diff --check`. | Done |
| 12 | Live MindFS evidence and cancel/follow-up settlement correlation | Read-only `mindfs.service` journal showed **zero Pi process exits** but two real `Pi SDK prompt idle timeout after 120000ms` failures in the same session. Sequence: cancel at 07:48:20 → new follow-up at 07:48:24 reported done while queue remained pending → later activity showed `pending=1`, then `pending=2`, followed by timeouts. Root cause: every `runtime_settled` released every waiter, so a late canceled-turn settlement could release a newly queued follow-up. Out-of-scope autopatch lock errors were observed but not modified. | Track SDK `queue_update` count in the MindFS session. Defer a non-error settlement while steering/follow-up messages remain queued; allow the hard-idle-timeout settlement through so a genuinely stuck queue still fails observably. Added a low-noise `runtime.settlement_deferred` diagnostic. | PASS: regression failed before fix; queue-correlation, timeout escape, busy follow-up, and shared-waiter tests passed 20 repetitions; focused race tests passed 20 repetitions; full Pi runtime, agent, and usecase packages; `git diff --check`. | Done |
| 13 | Hard-idle-timeout containment and poisoned runtime reuse | After the first observed 120-second timeout, the same Pi runtime stayed in `Pool`; the next send saw the old pending queue and timed out again. `SendMessage` classified a no-output timeout as an ordinary failure and left the poisoned process/session reusable. The regression reproduced a bridge timeout and proved the pool entry remained cached before the fix. Evidence: `server/internal/api/usecase/session.go:2354–2373`, live journal sequence. | Detect the precise MindFS bridge idle-timeout error, remove only that session's pool entry, and kill/close its bridge through existing pool ownership. Do not replay the ambiguous timed-out prompt and do not kill unrelated Pi sessions. The next user send can open/resume a clean runtime. | PASS: regression failed before fix and passed 20 repetitions; focused race test passed 10 repetitions; full usecase package; `git diff --check`. | Done |
| 14 | Deleted-session process/resource ownership | `CloseSession` closed known agent runtimes, but `DeleteSession` canceled turns and removed files without removing any agent-pool entry. A Pi bridge opened before agent state persistence therefore survived deletion indefinitely. The regression created exactly that state and proved the runtime remained cached before the fix. Evidence: `server/internal/api/usecase/session.go:879–926`. | Snapshot configured agent names once, then after each successful cascade deletion close legacy/unprefixed and agent-prefixed pool keys. This covers parent and child sessions without global agent kills or background sweeps. | PASS: regression failed before fix and passed 10 repetitions; cascade deletion test passed 10 repetitions; focused race test passed 5 repetitions; full usecase and agent packages; `git diff --check`. | Done |
| 15 | Service/Pool shutdown and child-process reaping | Audited `Pool.CloseAll` → Pi SDK `Runtime.CloseAll` → session close/kill → stdout/stderr drain → `cmd.Wait` → runtime unregister. The ownership chain has one Wait goroutine per started bridge and intentional close suppresses unexpected-exit logging. No leak or dead waiter was reproduced. | Added a focused lifecycle regression that opens a real test bridge, calls `CloseAll`, and requires the session to become closed and disappear from the runtime tracking map within a bound. No production change. | PASS: close/reap regression passed 20 repetitions and 10 race repetitions; `git diff --check`. | Done |
| 16 | Pi bridge diagnostic privacy and log-volume bounds | `stderrLoop` accepted up to 1 MiB per line and wrote the full line into the system journal. Because Pi extensions/providers share bridge stderr, one line could leak bearer/API secrets or flood logs. Existing stdout non-JSON diagnostics were already bounded, but stderr was not. Evidence: `server/internal/agent/pi_sdk_runtime/session.go:477–487`. | Reuse the existing UTF-8-safe preview bound and add narrow precompiled redaction patterns for bearer tokens, named secrets, JWT/base64-like long tokens. Keep stack/error text observable while replacing secret values and capping each journal message. | PASS: redaction/bound test passed 20 repetitions; full Pi runtime package; Go vet for Pi runtime; `git diff --check`. | Done |
| 17 | Default Pi bridge script resolution and execution provenance | The live unit runs with `WorkingDirectory=/root`, while the MindFS executable is `/root/mindfs/mindfs`. Default resolution searched working-directory candidates first, including `/root/probe.mjs`, before the executable's bundled source-tree path. A later unrelated file at that name could hijack bridge execution. No such file exists now, but the precedence was unsafe. | Keep explicit `ProbePath` as the intentional override; for defaults, search executable/source/install layouts before cwd development fallbacks. Extracted the precedence into a testable resolver without adding a new package. | PASS: bundled-vs-cwd precedence test passed 20 repetitions; full bridge, Pi runtime, and agent packages; `git diff --check`. | Done |
| 18 | `pi-rpc` rollback lifecycle parity | Audited the explicit rollback protocol and found the same structural hazards already fixed in SDK mode: stale inherited `PWD`, `cmd.Wait` racing output readers, unbounded raw stderr lines, ignored scanner errors, and no public closed-state contract for Pool eviction. These could make rollback reintroduce wrong-root tools, dropped tail events, or dead-session reuse. Evidence: `server/internal/agent/pi/session.go:80–170`, `session.go:426–502`. | Apply the same minimal lifecycle invariants: runtime-root `PWD`, drain both readers before Wait, kill on non-EOF protocol read failure, bound stderr with existing preview, report scanner errors, and expose `Closed()` for generic Pool eviction. No new abstraction/package was introduced. | PASS: PWD, startup-retry, and CloseAll tests passed 20 repetitions; lifecycle race tests passed 10 repetitions; full Pi RPC and agent packages; Pi RPC vet; `git diff --check`. | Done |
| 19 | Changed-code simplicity and maintainability audit | Reviewed the actual production/test diff for dead state, one-use wrappers, duplicated classifiers, lock domains, logging bounds, compatibility fallbacks, and formatting. Production changes remain at existing ownership boundaries (Pool, runtime session, usecase, bridge resolver); no framework/package or compatibility layer was added. Found one production constant used only by timing tests. | Moved `messageEndFallbackDelay` out of production into the test file. Retained small error classifiers because they encode materially different replay/discard safety policies. No speculative refactor of user-modified WS/frontend code or test-script abstraction was made. | PASS: all touched Go packages; vet for all touched packages; gofmt clean; `git diff --check`. | Done |
| 20 | Full repository integration, compatibility, and evidence closure | Ran the complete backend and frontend validation matrix against the combined working tree, including pre-existing user changes. Confirmed all 20 rounds have distinct evidence and every confirmed in-scope defect has a regression or bounded diagnostic. No new integration defect appeared. The current systemd service was deliberately not rebuilt/restarted because production-state mutation is out of scope. | No production code change. Finalized this audit ledger, separated pre-existing worktree changes from goal-owned files, and recorded the remaining deployment/observation risk. | PASS: `/root/.local/go1.25/bin/go test ./... -count=1`; `go vet ./...`; `node --check server/internal/agent/pi_sdk_bridge/probe.mjs`; `npm run typecheck`; `npm run build`; extension-UI Playwright 3/3; full Playwright 18/18; `git diff --check`; 20 ledger rows. | Done |

## Confirmed fixes

### Round 1 — stale closed Pi SDK session reuse

Root cause: `pi_sdk_runtime.session` closed its lifecycle channel when the bridge process ended, but `agent.Pool` had no liveness check and continued returning the cached session for the same key. That made a bridge exit persistent across later sends until another path explicitly removed the pool entry.

Resolution:

- Reuse the session's existing closed-channel invariant through `Closed()`; no second health state was introduced.
- Evict a closed cached session at the pool ownership boundary before opening a replacement.
- Log only lifecycle transitions useful for diagnosis: closed-cache eviction, unexpected bridge exit code/error, and stderr read failure.
- Keep intentional `Close()` quiet, avoiding noisy expected-exit logs.

### Round 2 — mid-turn bridge exit recovery

Root cause: the recovery loop only reopened SDK-level stale session IDs. A MindFS Pi bridge process exit produces `pi sdk runtime session closed`, so every recovery attempt reused the same dead runtime. The retry loop also applied its 30-second transient-failure delay after it had already created a fresh process.

Resolution:

- Keep closed-runtime errors separate from stale SDK session IDs because their replay safety differs.
- Reopen a closed runtime only when assistant output has already begun; recovery sends `continue`, never replays the original prompt in this path.
- Retry immediately after a successful process reopen; retain the existing delay for ordinary repeated transient failures.
- Cover the real process boundary with a deterministic two-generation fake Pi SDK bridge and verify the combined streamed/persisted response.

### Round 3 — pipe drain ordering and exit reason fidelity

Root causes:

- `waitLoop` called `cmd.Wait()` concurrently with stdout/stderr readers. `StdoutPipe`/`StderrPipe` require output reads to finish before `Wait`; the race intermittently closed descriptors before buffered final events were consumed.
- EOF won races against process wait and was forwarded as an opaque command error; waiters received a generic closed error instead of the retained exit reason.

Resolution:

- Coordinate both output readers with a `sync.WaitGroup` and call `Wait` only after they drain.
- On a non-EOF stdout read failure, fail pending commands and kill the now-unusable protocol process so lifecycle cleanup cannot hang.
- Store the process close error before publishing the closed channel, and reuse it for later requests/waiters.
- Normalize nil/EOF exits to `pi sdk runtime process exited`; preserve concrete exit status or signal when available.

### Round 4 — cancellation owner race

Root cause: `TurnCanceler.Begin` performed `atomic.AddUint64` on `turnID`, then later wrote the same field non-atomically under a mutex; `End` also read it non-atomically. Mixing those synchronization domains caused a real data race and made concurrent turn ownership non-linearizable.

Resolution:

- Protect ID allocation and current cancel-function assignment with the same existing mutex.
- Remove the unnecessary atomic import and state transition.
- Verify that ending an older turn cannot clear the current turn's cancel function.

### Round 5 — stale inherited `PWD`

Root cause: setting `Cmd.Env` disables Go's automatic `PWD` adjustment for a non-empty `Cmd.Dir`. MindFS always constructed an explicit environment, so the Pi bridge inherited the server's old `PWD` even though its OS working directory was the selected managed root.

Resolution:

- Append `PWD=<runtime root>` after inherited and agent-specific values so Go's later-value deduplication preserves the correct root.
- Keep `cmd.Dir` as the source of truth and avoid a second path-normalization policy.

### Round 6 — invalid production session identity

Root cause: startup metadata parsing silently returned on empty/malformed data and did not enforce the production bridge's `sessionId` contract. The session had already been initialized with the MindFS pool key, making the failure look successful until a later resume attempted that non-SDK identifier.

Resolution:

- Return explicit startup metadata errors and close the newly started bridge on validation failure.
- Require `sessionId` only for real SDK runtime starts; keep explicit test scenarios backward compatible.
- Continue accepting both string-shaped and object-shaped model metadata.

### Round 7 — hidden runtime-state failure

Root cause: `ListModes` explicitly ignored the only request that verified/refreshed Pi runtime state, then returned cached mode data as a successful response.

Resolution: return the runtime error directly. This keeps disconnection observable and lets existing callers/prober apply their normal failure handling rather than trusting stale state.

### Round 8 — successful retries remained failed

Root cause: one generic handler ignored the distinct SDK schemas for retry start/end. A successful retry has no message to overwrite the earlier error, while a failed retry reports `finalError`, which the handler did not parse.

Resolution:

- Preserve transient retry-start status for observability.
- Explicitly clear `lastTurnErr` on `auto_retry_end.success=true`.
- Capture `finalError` when retries end unsuccessfully.

### Round 9 — terminal-boundary review (no change)

Verified invariants:

- `message_end` and `turn_end` alone do not finish a MindFS send.
- retrying `agent_end` does not finish the send.
- production bridge `agent_end` is marked `promptDone:false`; only drained `runtime_settled` finishes it.
- delayed tools and post-agent persistence remain inside the same turn.
- `markTurnComplete` suppresses duplicate terminal signals.

No confirmed issue remained in this dimension; changing it would risk reintroducing early completion.

### Round 10 — follow-up settlement review

Verified that one runtime settlement intentionally releases the active prompt and its queued follow-up waiter. Waiters are buffered, removed on request/context failure, and atomically detached before notification. The unused epoch counter did not participate in that correctness and was removed to avoid suggesting nonexistent stale-event protection.

### Round 11 — extension UI review (no change)

The blocking UI path is end-to-end covered and bounded. The real SDK test loads a temporary extension, waits on `ctx.ui.select`, answers through MindFS, and confirms that the Pi turn reaches `message_done`. No evidence supported a change in this dimension.

### Round 12 — observed cancel/follow-up mis-correlation

Facts from the live MindFS journal:

- No `[agent/pi-sdk] process.exit` or `session closed` event explained the reported behavior.
- The same Pi session accumulated queued follow-ups after cancellation and twice hit the bridge's 120-second idle timeout.
- A new follow-up was acknowledged as done four seconds after cancel, before the queue drained.

Root cause: settlement was session-wide, not queue-aware. The old canceled run's terminal event could release the new follow-up waiter. MindFS now uses the SDK's ordered `queue_update` events to suppress that stale settlement while preserving the existing hard-timeout escape.

### Round 13 — repeated reuse after hard timeout

Root cause: hard idle timeout is evidence that the bridge's active prompt/queue state did not reach a usable terminal boundary. Treating it as an ordinary send error retained that state indefinitely.

Resolution:

- Match only `Pi SDK prompt idle timeout`, not caller context deadlines or generic upstream errors.
- Drop the affected MindFS pool session without automatically replaying its prompt.
- Preserve the original error for UI/persistence while making the following send start from a clean bridge.

### Round 14 — deleted sessions leaked bridge processes

Root cause: session metadata/file cleanup and runtime-pool cleanup followed separate paths; only the close flow performed both. Delete—including cascade delete—never invoked pool cleanup.

Resolution: close every configured agent runtime key belonging to each successfully deleted logical session. Cleanup is synchronous, bounded by the configured agent list, and does not affect sibling sessions.

### Round 15 — shutdown/reap review (no production change)

The post-Round-3 reader/Wait ordering still terminates correctly during intentional shutdown: Close publishes the closed state, closes stdin, kills the process, readers drain, Wait reaps, and runtime unregisters. A permanent bounded regression now protects this lifecycle.

### Round 16 — unbounded raw bridge stderr

Root cause: the scanner bounded allocation but the logger did not bound or sanitize the accepted line. Pi's SDK, extensions, and child diagnostics all feed this channel.

Resolution: sanitize first, then UTF-8-safe truncate. Redaction is intentionally narrow and observable through explicit markers rather than silently deleting whole diagnostics.

### Round 17 — cwd probe shadowing

Root cause: default discovery conflated a developer convenience fallback with a trusted deployment source and gave the fallback higher precedence. The actual service working directory made `/root/probe.mjs` the first candidate.

Resolution: executable-relative and installed-share paths now win. Explicit configured paths still win over every default, preserving intentional deployments.

### Round 18 — rollback protocol parity

Although Pi SDK is the default, `pi-rpc` remains an explicit production rollback. Leaving known lifecycle defects there would make an emergency rollback unsafe. The changes mirror proven invariants without attempting to merge the two protocol implementations or introduce a new framework.

### Historical Round 19 — changed-code cleanliness

Review outcome at that time:

- No thin production wrapper or new package had been introduced.
- Closed-session eviction belongs to Pool; process state belongs to runtimes; retry/discard policy belongs to usecase.
- `runtime transport closed`, `SDK stale session`, and `hard idle timeout` remain separate predicates because replay safety differs.
- The only confirmed dead production artifact was a test-only timing constant; it now lives with the tests.
- Large test deltas are deterministic process-boundary regressions, not production abstraction or fallback code.

The later certified Round 19 found a cross-package privacy inconsistency and added the justified shared package `server/internal/agent/diagnostic`; see the authoritative certified record.

### Round 20 — full integration and evidence closure

No further defect was confirmed after running the full repository matrix. This round intentionally made no code change: changing passing integration code merely to make the twentieth round look active would violate the no-speculation and clean-code constraints.

## Final validation

All required gates passed on 2026-07-12:

```bash
cd /root/mindfs
/root/.local/go1.25/bin/go test ./... -count=1
/root/.local/go1.25/bin/go vet ./...
node --check server/internal/agent/pi_sdk_bridge/probe.mjs
(cd web && npm run typecheck)
(cd web && npm run build)
(cd web && npm run test:extension-ui)  # 3 passed
(cd web && npx playwright test)        # 18 passed, 0 failed
git diff --check
```

Vite emitted only its existing large-chunk advisory; the build succeeded. No test, vet, syntax, type, build, or Playwright gate failed.

## Goal-owned files

Production code changed by this goal:

- `server/internal/agent/diagnostic/redact.go`
- `server/internal/agent/pi_sdk_runtime/session.go`
- `server/internal/agent/pi/session.go`
- `server/internal/agent/pi_sdk_bridge/client.go`
- `server/internal/agent/pool.go`
- `server/internal/agent/types/types.go`
- `server/internal/api/usecase/session.go`

Regression/evidence files changed or added by this goal:

- `server/internal/agent/pi_sdk_runtime/session_test.go`
- `server/internal/agent/pi/session_test.go`
- `server/internal/agent/pi_sdk_bridge/client_test.go`
- `server/internal/agent/pool_test.go`
- `server/internal/agent/types/types_test.go`
- `server/internal/api/usecase/usecase_test.go`
- `docs/audit/20-round-pi-runtime-audit-20260712.md`
- `docs/audit/round-evidence-20-pi-runtime-20260712.md`
- `docs/audit/validation-20-pi-runtime-20260712.log`
- `docs/audit/pre-fix-failures-20-pi-runtime-20260712.log`
- `docs/audit/baseline-20-pi-runtime-20260712.log`
- `docs/audit/certified-pi-runtime-20260712/` (baseline, 20 immutable records, raw validation logs, chain verification, and certified index)

Pre-existing modifications in Codex, `probe.mjs`, API stream/WS, and web files were not overwritten or reverted. The initial status snapshot and all eleven pre-goal mtimes are retained in the baseline artifact; each is earlier than goal start `2026-07-12 08:17:06 +08:00`. Full validation included those files to detect integration breakage.

## Remaining observation/deployment boundary

- The live `mindfs.service` still runs the pre-change binary. This goal did not rebuild, replace, or restart the production service because production-state mutation was explicitly out of scope.
- Therefore the code fixes are fully tested but not yet observed in the live journal. After an authorized deployment/restart, watch for `runtime.settlement_deferred`, `discard_stuck_runtime`, `get_or_create.evict_closed`, and any `process.exit` lines.
- Read-only journal evidence also showed Pi upstream autopatch lock contention. It is outside MindFS and `/root/.pi` was not modified. The confirmed MindFS failure path was queued follow-up mis-correlation followed by repeated hard idle timeouts, not a Pi bridge process exit.
