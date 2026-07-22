# Design: MindFS relay automatic recovery

## Boundary

Keep the existing relay persistence and reconnect implementation unchanged.
The fix is at the deployment-management boundary plus one live binding
operation:

1. systemd owns the host process and boot startup;
2. repository management scripts choose systemd whenever its unit is loaded;
3. Docker Compose remains a fallback deployment for hosts without the unit;
4. the relay manager loads its existing private credential store and reconnects
   automatically after startup.

## Runtime Flow

1. Run `mindfs -bind-relay` against the already-running local service.
2. The local relay manager generates a pending code and polls the configured
   relay until the user confirms the browser binding.
3. The confirmed device token, node ID, and connector endpoint are written to
   `/root/.config/mindfs/credentials.json` with mode `0600`.
4. On every later systemd start, `relay.Manager.Start` loads those credentials
   and starts the reconnecting relay session automatically.

## Supervisor Selection

- `start.sh`: loaded systemd unit -> `systemctl enable --now`; otherwise use
  modern/legacy Compose.
- `stop.sh`: loaded systemd unit -> stop it; otherwise stop Compose.
- `restart.sh`: loaded systemd unit -> restart it regardless of current active
  state; otherwise use Compose/native fallback.
- `status.sh`: loaded systemd unit -> report systemd; otherwise report Compose.
- the versioned systemd unit uses `Restart=always`, matching the live unit and
  restoring the process after both failure and unexpected clean exit.

The current host's legacy container is separately changed to restart policy
`no` and stopped. This operational state is intentionally not represented as a
secret or generated runtime file in Git.

## Pi SDK recovery integrity

The Pi JSONL runtime is assembled from a long-lived `services` object and a
session created from those services. Current Pi exposes `modelRuntime` on the
services object and no longer exposes `modelRegistry` on the session. Model
discovery and model selection therefore use the service-owned runtime, with the
legacy service registry accepted as a compatibility fallback. The session
continues to own the active model and thinking level. The existing real-SDK
integration test calls `ListModels` so SDK shape drift fails before deployment.

## Compatibility and Security

- Compose-only hosts keep their existing fallback path.
- Relay credentials stay outside the repository and are never printed in
  validation output.
- A real systemd restart proves that persistence, process ownership, and relay
  reconnect work together.
- A live Pi refresh plus a real selected session proves Agent capability
  recovery is not masked by the independent command-discovery path.
- Rollback: restore the four shell scripts from Git and re-enable the prior
  Compose deployment only after disabling systemd, so the port still has one
  owner.
