# Controlled deployment: 20260712-143812-reliability

## Result

- Status: **PASS**
- Finished: `2026-07-12T14:41:31+08:00`
- PID: `4048012` -> `615049`
- Previous SHA-256: `95d74fe3a91307817e2bac4cdffa8ee6dfd8edf7a1ebfb6d295a4c6cfe5acfc3`
- Candidate/running SHA-256: `bb7f43896349b8f913750ec43789c98350200fdade0ab9b4fae2077437520d29`
- Rollback: not needed
- Backup: `/root/mindfs/.mindfs/deploy-backups/mindfs-20260712-143812-reliability-before-reliability`

## Pre-deployment invariants

- SQLite `PRAGMA quick_check`: `ok`
- Sessions: `32`
- Agent bindings: `34`
- Session JSONL files: `58`, each hashed before restart

## Post-deployment observations

- Service health passed and relay reconnected with HTTP `101 Switching Protocols`.
- SQLite remained healthy with 32 sessions and 34 bindings.
- One JSONL hash changed at shutdown. Structural inspection (without message content) showed one additional valid user exchange at `2026-07-12T06:41:30.484321008Z`.
- SQLite `sessions.updated_at` for that session remained `2026-07-12T05:37:44.366055627Z`, and `agent_ctx_seq` remained at the preceding exchange.

This was decisive evidence of a split write: durable JSONL append succeeded, then process shutdown occurred before the SQLite list projection advanced. The message was preserved, but incremental session listing could hide it after restart.
