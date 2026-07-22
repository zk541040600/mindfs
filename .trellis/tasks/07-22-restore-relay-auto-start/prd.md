# 修复 MindFS 外网中继自动恢复

## Goal

Restore public relay access for the `localhost.localdomain` MindFS node and
make the recovered connection survive service and host restarts without
starting competing MindFS supervisors.

## Confirmed Facts

- The live MindFS HTTP service is healthy under the enabled
  `mindfs.service` systemd unit at `10.23.50.137:7331`.
- A legacy Docker Compose container uses host networking, targets the same
  address, and is stuck in a restart loop because systemd already owns the
  port.
- The live relay status is unbound and disconnected, and
  `/root/.config/mindfs/credentials.json` is absent.
- A confirmed relay bind persists credentials in that file; normal manager
  startup automatically reconnects whenever valid credentials exist.
- `start.sh`, `stop.sh`, and `status.sh` currently prefer Compose merely
  because `docker-compose.yml` exists, even when a systemd unit is loaded.
- After a real service restart, Pi is installed and command discovery works,
  but model discovery fails because the SDK bridge reads the model registry
  from the session instead of the model runtime owned by current Pi services.

## Requirements

- Treat a loaded systemd unit as the canonical supervisor; use Compose only
  as a fallback when the unit is not installed.
- Starting through the repository script must enable boot startup as well as
  start the service.
- Restart and status behavior must select/report the same canonical
  supervisor so repository scripts do not recreate the dual-owner state.
- Disable and stop the legacy Docker container on this host without deleting
  MindFS data.
- Start a new relay bind, complete account confirmation, and persist the new
  credentials outside Git with private file permissions.
- Verify automatic recovery with a real systemd restart and public relay
  health check.
- Restore Pi model discovery after restart so the Agent selector exposes Pi
  models and modes, and cover the real SDK model-list path with a regression
  test.
- Commit only task-owned changes, archive the Trellis task, and push the
  resulting commits to `origin` without including unrelated worktree edits or
  relay secrets.

## Acceptance Criteria

- [x] `mindfs.service` is enabled and active, and exactly one process listens
      on `10.23.50.137:7331`.
- [x] The legacy `mindfs` Docker container is not running and has no automatic
      restart policy.
- [x] `/api/relay/status` reports bound and connected with a non-empty node ID
      after binding and again after a real systemd restart.
- [x] The public relay node health endpoint is reachable (an authentication
      challenge is acceptable evidence that the tunnel is live).
- [x] The versioned systemd unit uses an automatic restart policy, and
      repository management scripts prefer a loaded systemd unit and pass
      syntax/integration checks.
- [x] No credential, device token, pending bind code, or private URL query is
      added to Git.
- [x] Pi reports non-empty models and modes after a real service restart, and
      selecting it starts a usable Pi-backed session.
- [ ] The Trellis task is archived and all task-owned commits are present on
      `origin`.

## Out of Scope

- Changing relay authentication or E2EE protocols.
- Rebinding or deleting other devices shown in the relay account.
- Committing runtime credentials or host-specific secret material.

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
