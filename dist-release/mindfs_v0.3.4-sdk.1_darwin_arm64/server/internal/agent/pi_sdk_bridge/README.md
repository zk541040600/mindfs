# Pi SDK auxiliary bridge

This directory contains the production auxiliary Pi SDK bridge used by MindFS for SDK-backed features that are safer and more valuable outside the interactive `pi-rpc` streaming runtime.

## Runtime boundary

MindFS deliberately keeps two Pi layers:

- **Interactive runtime:** `agents.json` keeps Pi on `protocol: "pi-rpc"` with `pi --mode rpc --no-session`. Normal chat, slash commands, tool events, extension UI, cancellation, retry, model selection, and thinking-level behavior stay on this stable path.
- **SDK auxiliary bridge:** this Node bridge is invoked by Go for bounded SDK-backed capabilities: safe external session metadata, cache/status discovery, explicit refresh, deterministic bridge probes, and explicit `safe_transcript` import.

This is the completed product shape for the current integration. The SDK bridge does not replace the `pi-rpc` runtime unless a future product decision explicitly changes that boundary.

## SDK module resolution

`probe.mjs` loads the Pi SDK lazily and returns a structured JSON error if it cannot be resolved. Resolution order:

1. `MINDFS_PI_SDK_MODULE`
2. `PI_SDK_MODULE_PATH`
3. normal Node module resolution for `@earendil-works/pi-coding-agent/dist/index.js`
4. global npm root fallback from `npm root -g`

The current Docker/host deployment uses the global Pi installation discovered through `npm root -g`; no host-specific absolute path is embedded in the bridge source.

## Bridge commands

```bash
node server/internal/agent/pi_sdk_bridge/probe.mjs capabilities --cwd /root/mindfs --agent-dir /root/.pi/agent --json
node server/internal/agent/pi_sdk_bridge/probe.mjs list-sessions --cwd /root/mindfs --agent-dir /root/.pi/agent --json
node server/internal/agent/pi_sdk_bridge/probe.mjs import-session --cwd /root/mindfs --session-id <id> --json
node server/internal/agent/pi_sdk_bridge/probe.mjs session-smoke --cwd /root/mindfs --json
node server/internal/agent/pi_sdk_bridge/probe.mjs extension-ui-smoke --json
node server/internal/agent/pi_sdk_bridge/probe.mjs runtime-replacement-smoke --cwd /root/mindfs --json
```

The script prints explicit JSON success/failure envelopes. Unknown commands, bad flags, SDK resolution failures, SDK runtime failures, and invalid JSONL input return `success: false` with a structured error object instead of silently exiting.

## Production features

### Safe session metadata

`list-sessions` uses `SessionManager.list` and opens each returned session where possible. MindFS only consumes safe summary metadata such as:

- session id/path/name/cwd/timestamps/message count,
- tree entry counts and leaf/current-branch metadata,
- entry type counts.

It does not return transcript previews, raw context file contents, raw tool blobs, credential values, auth headers, or extension internals.

### SDK bridge status/cache

The Go bridge wraps session metadata discovery in a 60s cache and exposes a read-only status endpoint:

```text
GET /api/agents/pi/sdk-status
```

The status endpoint reports cached bridge health and does not trigger extension commands or transcript reads.

### Explicit refresh

External session listing accepts explicit refresh:

```text
GET /api/sessions/external?root=ge&agent=pi&limit=5&refresh=true
```

Default listing may use the cache; refresh bypasses a fresh cache while still failing closed if the SDK subprocess fails.

### Safe transcript import

`import-session` implements explicit safe transcript import for Pi sessions. MindFS calls this only when the import request mode is `safe_transcript`.

Safety rules:

- only visible `user` / `assistant` text exchanges are converted to MindFS exchanges,
- assistant role maps to MindFS `agent`,
- unsupported roles, non-message entries, tool calls/results, extension internal payloads, binary-looking content, and context internals are skipped with safe warning codes,
- tokens, API keys, private keys, auth headers, and high-entropy secret-like strings are redacted before data leaves the bridge,
- message and byte limits are enforced,
- no safe content returns a structured failure instead of creating an empty import.

The web UI adds a Pi-only confirmation gate and labels the footer action as `安全导入` so users explicitly opt into safe transcript import.

## Deterministic bridge probes

### `capabilities`

Loads Pi SDK resources through `DefaultResourceLoader` and reports safe metadata only:

- SDK availability/version,
- support flags for sessions, fork/clone/import, extension UI, resources, steer/follow-up, and compaction,
- skills, prompts, extensions, themes, and context file paths/counts,
- slash command metadata from extension registrations, prompt templates, and skills,
- model counts and safe model metadata.

Project trust is forced false during passive capability probing. Raw `AGENTS.md`/context file contents and credential values are never returned. Extension commands are not executed by this probe.

### `session-smoke`

Creates a temporary SDK session directory, writes a user/assistant message pair through `SessionManager`, lists it, reopens it, and reports tree/context metadata. The temporary directory is deleted unless `--session-dir` is supplied.

### `extension-ui-smoke`

Builds an in-memory SDK session with an inline extension factory. The extension registers `/ui-demo` and exercises MindFS-relevant UI methods without invoking an LLM provider:

- `notify`
- `setStatus`
- `setWidget`
- `setTitle`
- `setEditorText`
- `select`
- `confirm`
- `input`
- `editor`

The probe binds a recording RPC-style UI context and returns the stable event schema plus canned responses. This validates the SDK/runtime/UI seam deterministically while production UI remains served by `pi-rpc`.

### `runtime-replacement-smoke`

Creates a temporary persistent SDK session and drives `AgentSessionRuntime` replacement APIs without model calls:

- initial session creation,
- `fork(entryId, { position: "at" })`,
- `newSession()`,
- subscription/extension rebinding through `runtime.setRebindSession(...)`.

This remains a capability probe, not the production chat runtime.

## JSONL mode

`jsonl` mode is a deterministic stdio contract for Go integration tests:

```bash
printf '%s\n' \
  '{"id":"1","type":"start_test_runtime","scenario":"extension-ui"}' \
  '{"id":"2","type":"prompt","message":"/ui-demo"}' \
  '{"type":"extension_ui_response","id":"select-1","value":"sdk-bridge"}' \
  '{"type":"extension_ui_response","id":"confirm-1","confirmed":true}' \
  | node server/internal/agent/pi_sdk_bridge/probe.mjs jsonl
```

It emits deterministic `extension_ui_request` events for UI contract mapping.

## Operational notes

- Keep SDK subprocess failures fail-closed; do not degrade the `pi-rpc` runtime.
- Do not add transcript previews to passive list/status endpoints.
- Do not expose credential values, raw context files, raw tool results, extension internals, auth headers/tokens/API keys, or environment variables.
- Rebuild MindFS with the current release version ldflag so `/api/app/update` does not report a false update for Docker deployments.
