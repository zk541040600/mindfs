# Implementation Plan

1. Update the versioned systemd unit and `start.sh`, `stop.sh`, `restart.sh`,
   and `status.sh` so the service restarts automatically, a loaded systemd unit
   consistently takes precedence over Compose, and `start.sh` enables boot
   startup.
2. Run shell syntax checks and inspect the exact diff for unrelated changes or
   secrets.
3. Disable the legacy Docker container restart policy and stop the container;
   verify systemd remains the sole listener.
4. Start relay binding and complete browser confirmation; verify the private
   credential file and connected relay status without printing secrets.
5. Restart systemd and verify local health, relay bound/connected state, public
   node health, and single process ownership after recovery.
6. Repair Pi SDK model-registry ownership, add a real-SDK model-list regression,
   redeploy, and verify Pi models, modes, and a selected session after restart.
7. Run Trellis quality checks and the relevant repository validation commands.
8. Commit only task-owned implementation/planning files, archive the task,
   commit the archive/journal delta, and push `main` to `origin`.

## Validation

- `bash -n start.sh stop.sh restart.sh status.sh`
- `git diff --check -- start.sh stop.sh restart.sh status.sh deploy/systemd/mindfs.service`
- `systemctl is-enabled mindfs.service`
- `systemctl is-active mindfs.service`
- listener/process ownership inspection for `10.23.50.137:7331`
- redacted `/api/relay/status` assertions before and after restart
- public relay `/health` HTTP status check without exposing credentials
- redacted `/api/agents?refresh_agent=pi` assertions and a real Pi session
- final staged-diff secret scan and `git status --short`

## Risk and Rollback Points

- Binding requires one external browser confirmation; keep the local polling
  process alive until confirmation or expiry.
- Restarting systemd briefly interrupts active MindFS sessions; perform it only
  after binding is connected and use journal/status evidence after restart.
- Do not remove volumes or credential files when stopping the legacy container.
- Preserve the pre-existing edits in `.pi/extensions/trellis/index.ts` and
  `.trellis/scripts/common/active_task.py` throughout staging and commits.

## Execution Evidence

- `bash -n` passed for all four management scripts.
- Full `go test ./... -count=1` and `go vet ./...` passed.
- The legacy container is exited with restart policy `no`; systemd is enabled,
  active, and the sole owner of `10.23.50.137:7331`.
- Relay credentials were persisted with mode `0600` and were not added to Git.
- After a real systemd restart, the same node identity returned to bound and
  connected state; the public health endpoint returned HTTP 401, proving the
  tunnel is reachable and authentication-protected.
- Pi SDK 0.80.10 model ownership was repaired at the service runtime boundary.
  The real-SDK `ListModels` regression passes, and a post-restart live refresh
  reports 28 models, 7 modes, a selected current model, and no capability
  errors while the relay remains connected.
- Final validation passed: full `go test ./... -count=1`, `go vet ./...`, web
  TypeScript checking, Node/Shell syntax checks, and all three Pi Agent selector
  Playwright cases (expand models, select GPT 5.6 with max mode, recover a stale
  Pi row).
