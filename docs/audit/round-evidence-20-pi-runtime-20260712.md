# MindFS Pi runtime — historical evidence for the initial rounds

> **SUPERSEDED FOR COMPLETION:** this retrospective artifact was rejected by the independent auditor because it could not prove record-before-next-round chronology. The authoritative fresh sequence is `docs/audit/certified-pi-runtime-20260712/README.md`. It is retained only for root-cause history.

This file supplies the per-round details required by goal `mrh1nc0m-0wm1zi`: unique `file:line` evidence, root cause/conclusion, change, exact replay command, retained pre-fix evidence, and residual risk.

## Chronology and artifact integrity

The authoritative replay log is `docs/audit/validation-20-pi-runtime-20260712.log` (SHA-256 `cdc6bae8c68b0d47f4ba6c58501b681ba0b0a9751383bc0f8e6c8bc61f088b24`). It contains exactly 20 `START` markers, 20 ordered `STATUS PASS` markers, zero `STATUS FAIL` markers, nanosecond timestamps, the exact command, output, and exit code. Its runner returned immediately on the first failure; therefore a later round could not start before the preceding round passed.

Retained pre-fix excerpts are in `docs/audit/pre-fix-failures-20-pi-runtime-20260712.log` (SHA-256 `e6a45a98f350228645faeb6b4266409557135f706735cb408f7c88e9680ece4c`). The initial worktree boundary is in `docs/audit/baseline-20-pi-runtime-20260712.log` (SHA-256 `728489843fcc2cbab60f865d0fff2050b4b5c60a908324bb39e3d7ea73d29094`).

Repeated hygiene such as `git diff --check` is not counted as a round's unique evidence. Each round below names a distinct primary invariant and test/static observation. Round 20 is the required cross-layer integration gate, not a second claim for any earlier unit-level finding.

## Round 01 — closed Pi SDK session eviction

- **Unique evidence:** `server/internal/agent/pool.go:59–79`; `server/internal/agent/pool_test.go:170`.
- **Root cause:** Pool returned a cached session after its bridge lifecycle channel had closed.
- **Change:** expose the existing closed state and evict only cached sessions reporting `Closed()`.
- **Exact replay:** validation log line 2: `/root/.local/go1.25/bin/go test ./server/internal/agent -run '^TestPoolReopensClosedPiSDKSession$' -count=1`.
- **Pre-fix:** retained failure section Round 01: `GetOrCreate returned the closed pi-sdk session`.
- **Residual risk:** only runtimes implementing `Closed()` receive proactive eviction; Pi SDK does, and Pi RPC parity is completed in Round 18.

## Round 02 — safe recovery after partial bridge output

- **Unique evidence:** `server/internal/api/usecase/session.go:1519–1522`, `server/internal/api/usecase/session.go:3452–3520`; `server/internal/api/usecase/usecase_test.go:2056`.
- **Root cause:** recovery retried `continue` on the same closed runtime and slept 30 seconds even after reopen.
- **Change:** reopen transport-closed sessions only after assistant output proves the original prompt started; skip delay immediately after reopen.
- **Exact replay:** validation log line 9: `go test ./server/internal/api/usecase -run '^TestSendMessageRecoversAfterPiSDKExitWithPartialResponse$' -count=1`.
- **Pre-fix:** retained Round 02 deadline failure and `pi sdk runtime session closed` loop.
- **Residual risk:** a no-output transport loss is deliberately not replayed because tool side effects are ambiguous; the user receives the error instead.

## Round 03 — pipe drain and exit-error fidelity

- **Unique evidence:** `server/internal/agent/pi_sdk_runtime/session.go:490`, `server/internal/agent/pi_sdk_runtime/session.go:519`; tests at `session_test.go:70` and `session_test.go:77`.
- **Root cause:** `cmd.Wait()` raced stdout/stderr readers and could close a pipe before buffered final events were consumed; EOF obscured the exit reason.
- **Change:** drain both readers before Wait; retain normalized process-exit errors.
- **Exact replay:** validation log line 16: `go test ./server/internal/agent/pi_sdk_runtime -run '^(TestResponseFromEOFFallsBackToProcessExit|TestSessionClosedErrorPreservesProcessExit)$' -count=1`.
- **Pre-fix:** retained Round 03 `read |0: file already closed` failure.
- **Residual risk:** a descendant that improperly inherits bridge pipe descriptors could delay drain; intentional Close kills the bridge and bounded CloseAll tests cover the supported lifecycle.

## Round 04 — TurnCanceler synchronization

- **Unique evidence:** `server/internal/agent/types/types.go:448–481`; `server/internal/agent/types/types_test.go:9`.
- **Root cause:** `turnID` mixed atomic writes with mutex-protected ordinary reads/writes.
- **Change:** allocate ID and assign current cancel function under one mutex.
- **Exact replay:** validation log line 23: `go test -race ./server/internal/agent/types -run '^TestTurnCanceler' -count=1`.
- **Pre-fix:** retained Round 04 race detector report.
- **Residual risk:** concurrent Begins are linearized at mutex acquisition; callers must still avoid semantically overlapping turns unless the runtime supports them.

## Round 05 — Pi SDK runtime PWD

- **Unique evidence:** `server/internal/agent/pi_sdk_runtime/session.go:68–73`, `server/internal/agent/pi_sdk_runtime/session.go:329`; `session_test.go:49`.
- **Root cause:** non-nil `Cmd.Env` disables Go's automatic PWD rewrite when `Cmd.Dir` changes.
- **Change:** append the runtime root as the final `PWD` value.
- **Exact replay:** validation log line 30: `go test ./server/internal/agent/pi_sdk_runtime -run '^TestMergeEnvUsesRuntimeWorkingDirectory$' -count=1`.
- **Pre-fix:** static Go `os/exec.Cmd.environ` contract; no retained executable pre-fix snapshot is claimed.
- **Residual risk:** only non-empty runtime roots override PWD; empty-root sessions intentionally inherit the server environment.

## Round 06 — production SDK session identity

- **Unique evidence:** `server/internal/agent/pi_sdk_runtime/session.go:130–177`; `session_test.go:293`.
- **Root cause:** malformed/missing startup metadata silently left the MindFS pool key as the future Pi resume ID.
- **Change:** production starts require valid JSON and non-empty SDK `sessionId`; explicit test runtimes are exempt.
- **Exact replay:** validation log line 37: `go test ./server/internal/agent/pi_sdk_runtime -run '^TestApplyStartResponse' -count=1`.
- **Pre-fix:** bridge response contract inspection; no retained executable pre-fix snapshot is claimed.
- **Residual risk:** older external bridges omitting `sessionId` now fail fast rather than appear to start; explicit test scenarios retain compatibility.

## Round 07 — closed runtime mode visibility

- **Unique evidence:** `server/internal/agent/pi_sdk_runtime/session.go:1583`; `session_test.go:1226`.
- **Root cause:** `ListModes` discarded its state-refresh error and returned stale modes as success.
- **Change:** propagate the runtime error.
- **Exact replay:** validation log line 44: `go test ./server/internal/agent/pi_sdk_runtime -run '^TestRuntimeListModesReportsClosedSession$' -count=1`.
- **Pre-fix:** retained Round 07 failure: error was `<nil>`.
- **Residual risk:** callers must display/handle the error; returning stale data is intentionally no longer supported.

## Round 08 — auto-retry terminal state

- **Unique evidence:** `server/internal/agent/pi_sdk_runtime/session.go:930–961`; tests at `session_test.go:315` and the adjacent failed-retry case.
- **Root cause:** successful `auto_retry_end` did not clear the start error; failed retry's `finalError` was ignored.
- **Change:** event-type-aware success clearing and final-error capture.
- **Exact replay:** validation log line 51: `go test ./server/internal/agent/pi_sdk_runtime -run '^TestAutoRetry' -count=1`.
- **Pre-fix:** retained Round 08 contains both inverse-state failures.
- **Residual risk:** error state clears only on explicit SDK `success=true`; malformed events remain conservative.

## Round 09 — tool/retry/compaction terminal boundary

- **Unique evidence:** handlers at `session.go:956`, `session.go:987`, `session.go:1041`; tests at `session_test.go:892`, `915`, `946`, `966`, `1138`.
- **Conclusion:** message_end, turn_end, retrying agent_end, and raw production agent_end are correctly non-terminal until authoritative settlement.
- **Change:** none; changing a passing boundary would risk early persistence.
- **Exact replay:** validation log line 58 contains the five-test terminal-boundary command.
- **Pre-fix:** no defect and no pre-fix failure claimed.
- **Residual risk:** correctness depends on the bridge eventually producing `runtime_settled`; its hard-idle fallback is covered in Rounds 12–13.

## Round 10 — busy follow-up shared settlement

- **Unique evidence:** `session.go:1311`, `session.go:1330`, `session.go:1405`; tests at `session_test.go:989` and `1031`.
- **Conclusion/root cause:** ordinary active prompt + queued follow-up waiters correctly share final settlement; an unused epoch counter had no behavioral role.
- **Change:** remove the dead epoch field/increment.
- **Exact replay:** validation log line 65: race-enabled busy/follow-up tests.
- **Pre-fix:** static dead-state finding; no executable failure claimed.
- **Residual risk:** late settlement from a canceled turn was outside this ordinary-flow test and became the distinct live finding fixed in Round 12.

## Round 11 — real SDK extension UI round trip

- **Unique evidence:** `session.go:574–599`, `session.go:1457–1494`; real SDK test `session_test.go:526`.
- **Conclusion:** blocking select/confirm/input/editor requests remain pending and answerable; fire-and-forget UI remains non-blocking.
- **Change:** none.
- **Exact replay:** validation log line 72: `TestRuntimeRealSDKExtensionUIRoundTripCompletesTurn`.
- **Pre-fix:** no defect and no pre-fix failure claimed.
- **Residual risk:** browser rendering belongs to pre-existing frontend code; full Playwright coverage is retained in Round 20.

## Round 12 — canceled-turn settlement versus queued follow-up

- **Unique evidence:** `session.go:917–926`, `session.go:987–1009`; tests at `session_test.go:1067` and `1091`; live journal excerpt in pre-fix evidence.
- **Root cause:** every runtime settlement released every waiter even while SDK queue_update still reported pending messages.
- **Change:** defer non-error settlement while queue count is non-zero; let hard-idle timeout escape.
- **Exact replay:** validation log line 79: race-enabled queue-correlation tests.
- **Pre-fix:** retained Round 12 test failure plus 07:48–07:56 live sequence (`pending=1`, then `pending=2`, two idle timeouts; zero process exits).
- **Residual risk:** relies on ordered SDK `queue_update`; malformed/missing queue events fall back to timeout rather than silent success.

## Round 13 — poisoned runtime disposal after hard timeout

- **Unique evidence:** `server/internal/api/usecase/session.go:1524`, `server/internal/api/usecase/session.go:2381–2387`; test `usecase_test.go:2112`.
- **Root cause:** a 120-second bridge timeout left the same stuck runtime cached for the next send.
- **Change:** discard only the affected pool session; never auto-replay the ambiguous prompt.
- **Exact replay:** validation log line 86: race-enabled idle-timeout disposal test.
- **Pre-fix:** retained Round 13 cached-runtime failure.
- **Residual risk:** the timed-out user message is not retried automatically; the original error remains visible and the next send opens cleanly.

## Round 14 — deletion lifecycle cleanup

- **Unique evidence:** `server/internal/api/usecase/session.go:882–927`; test `usecase_test.go:449`.
- **Root cause:** DeleteSession removed metadata/files but did not close agent-pool sessions.
- **Change:** close unprefixed and configured agent-prefixed keys for each successfully deleted cascade member.
- **Exact replay:** validation log line 93: race-enabled deleted-session runtime test.
- **Pre-fix:** retained Round 14 leaked-runtime failure.
- **Residual risk:** an in-memory runtime for an agent removed from current configuration and lacking a persisted binding cannot be enumerated by name; normal configured and persisted agents are covered.

## Round 15 — CloseAll reaping

- **Unique evidence:** `server/internal/agent/pi_sdk_runtime/session.go:247`, `session.go:490`; test `session_test.go:157`.
- **Conclusion:** intentional shutdown closes state/stdin, kills bridge, drains readers, Waits, and unregisters.
- **Change:** permanent bounded regression only.
- **Exact replay:** validation log line 100: race-enabled CloseAll reap test.
- **Pre-fix:** no defect and no pre-fix failure claimed.
- **Residual risk:** live systemd restart was not performed because production mutation is outside scope.

## Round 16 — diagnostic secrecy and volume

- **Unique evidence:** `server/internal/agent/pi_sdk_runtime/session.go:31–40`, `session.go:2210`; test `session_test.go:22`.
- **Root cause:** bridge stderr accepted/logged a full line up to 1 MiB without secret redaction.
- **Change:** redact narrow credential patterns, then UTF-8-safe cap the journal line.
- **Exact replay:** validation log line 107: sanitizer test.
- **Pre-fix:** static privacy-bound finding; no executable pre-fix snapshot claimed.
- **Residual risk:** arbitrary natural-language sensitive text without a credential pattern cannot be perfectly identified; output is still capped to limit exposure.

## Round 17 — probe execution provenance

- **Unique evidence:** `server/internal/agent/pi_sdk_bridge/client.go:222–239`; test `client_test.go:61`; live unit had WorkingDirectory `/root` and executable `/root/mindfs/mindfs`.
- **Root cause:** cwd fallback `/root/probe.mjs` preceded executable-relative bundled paths.
- **Change:** explicit path remains first; default executable/install layouts now precede cwd.
- **Exact replay:** validation log line 114: bundled-versus-cwd precedence test.
- **Pre-fix:** static resolver order plus live unit configuration; no executable pre-fix failure claimed.
- **Residual risk:** an explicitly configured ProbePath is trusted by design.

## Round 18 — Pi RPC rollback parity

- **Unique evidence:** `server/internal/agent/pi/session.go:91`, `session.go:463`, `session.go:494`; tests `pi/session_test.go:18`, `582`, `610`.
- **Root cause:** rollback path retained stale PWD, Wait/pipe race, scanner blindness, raw stderr volume, and no Pool-visible closed state.
- **Change:** apply the proven lifecycle invariants without merging protocol implementations.
- **Exact replay:** validation log line 121: race-enabled PWD/startup/CloseAll tests.
- **Pre-fix:** static parity comparison; no separate executable pre-fix failure claimed.
- **Residual risk:** Pi RPC stderr is bounded but does not share the SDK-specific credential redactor; it remains an explicit rollback, not the default.

## Round 19 — actual-diff cleanliness

- **Unique evidence:** production-only test timing constant moved to `pi_sdk_runtime/session_test.go:20`; zero `settlementEpoch` references; formatting/vet record at validation log line 128.
- **Conclusion/root cause:** no framework or thin wrapper was needed; the test-only timing constant and dead epoch were the confirmed cleanup candidates.
- **Change:** keep test timing in tests and remove dead state; retain distinct safety classifiers because replay policies differ.
- **Exact replay:** validation log line 128 records gofmt-empty assertion, touched-package vet, and diff check.
- **Pre-fix:** static actual-diff review; no executable failure claimed.
- **Residual risk:** maintainability review is qualitative; Round 20 supplies full integration evidence.

## Round 20 — full repository integration and boundary closure

- **Unique evidence:** timestamped full-matrix start at `validation-20-pi-runtime-20260712.log:134`; backend module root `go.mod:1`; frontend scripts `web/package.json:7–14`; 18-browser-test output in the same log.
- **Conclusion:** combined worktree passes backend, bridge, type, build, and browser integration after all prior focused fixes.
- **Change:** evidence/docs only; no code changed merely to create a twentieth delta.
- **Exact replay:** validation log line 134 records `go test ./...`, `go vet ./...`, bridge `node --check`, web typecheck/build, and full Playwright.
- **Pre-fix:** integration closure, not a defect claim.
- **Residual risk:** live `mindfs.service` still runs the pre-change binary; deployment/restart and post-deploy journal observation require separate authorization.
