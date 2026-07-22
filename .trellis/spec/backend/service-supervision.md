# Service Supervision

## Scenario: Single supervisor with persistent relay recovery

### 1. Scope / Trigger

- Trigger: changing `start.sh`, `stop.sh`, `restart.sh`, `status.sh`,
  `deploy/systemd/mindfs.service`, `docker-compose.yml`, or relay startup
  wiring.
- Problem prevented: systemd and Docker Compose both starting MindFS on the
  same host address, leaving one owner healthy while the other loops and
  reports a false outage.

### 2. Signatures

- `./start.sh`: start the canonical supervisor and enable boot startup when
  systemd is installed.
- `./stop.sh`: stop the canonical supervisor.
- `./restart.sh`: restart the canonical supervisor.
- `./status.sh`: report the canonical supervisor, MindFS CLI status, and the
  listener owner.
- `mindfs -bind-relay -addr <host:port>`: begin explicit account binding for
  an unbound running server.

### 3. Contracts

- A loaded `mindfs.service` unit is the canonical supervisor. Compose is a
  fallback only when that unit is not loaded.
- The systemd start path uses `systemctl enable --now mindfs.service`; the unit
  uses `Restart=always`.
- Exactly one supervisor may own the configured address. Before switching
  supervisors, stop the old owner and disable its automatic restart policy.
- Confirmed relay credentials live in the MindFS config directory as
  `credentials.json`, remain outside Git, and have mode `0600`.
- Normal relay manager startup loads valid credentials and reconnects
  automatically. `-bind-relay` is an explicit recovery action for missing or
  invalidated credentials, not a permanent systemd argument.
- The systemd unit must set the same `HOME` used during binding so restart can
  find the persisted credentials.

### 4. Validation & Error Matrix

| Condition | Required behavior |
|---|---|
| systemd unit is loaded | All management scripts select systemd |
| systemd unit is absent and Compose is available | Scripts use the matching Compose command |
| another process still owns the address after stop | `stop.sh` reports the listener and exits non-zero |
| relay credentials are absent | Local service stays healthy; relay status remains unbound until explicit binding |
| relay credentials are valid | Startup opens the connector and status becomes bound/connected |
| relay credentials become permanently invalid | Credentials are cleared and explicit rebind is required |

### 5. Good / Base / Bad Cases

- Good: systemd is enabled and active, the legacy container is stopped with
  restart policy `no`, one MindFS process listens, and relay reconnects after a
  real service restart.
- Base: a Compose-only host continues to start and stop through
  `docker compose` without a systemd unit.
- Bad: checking only for `docker-compose.yml` and starting Compose before
  checking a loaded systemd unit; both supervisors then compete for the same
  port.

### 6. Tests Required

- Run `bash -n start.sh stop.sh restart.sh status.sh`.
- On a systemd deployment, run `start.sh` and `status.sh`; assert systemd is
  selected and no container is started.
- Verify `systemctl is-enabled` and `systemctl is-active` are successful and a
  single listener owns the configured address.
- For relay recovery, assert redacted `/api/relay/status` fields are
  bound/connected before and after a real systemd restart, then check the
  public node `/health` status without logging credentials.
- Scan the staged diff for device tokens, pending binding codes, credential
  payloads, and private query strings.

### 7. Wrong vs Correct

#### Wrong

```bash
if [[ -f docker-compose.yml ]]; then
  docker compose up -d mindfs
elif command -v systemctl >/dev/null 2>&1; then
  systemctl start mindfs.service
fi
```

#### Correct

```bash
if command -v systemctl >/dev/null 2>&1 &&
  [[ "$(systemctl show mindfs.service -p LoadState --value 2>/dev/null || true)" == "loaded" ]]; then
  systemctl enable --now mindfs.service
elif [[ -f docker-compose.yml ]]; then
  docker compose up -d mindfs
fi
```
