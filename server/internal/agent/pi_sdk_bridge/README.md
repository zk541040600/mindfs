# Experimental Pi SDK bridge probe

This directory is a reversible Phase B prototype for evaluating whether a Node-based Pi SDK bridge adds useful MindFS capabilities beyond the current production `pi-rpc` path.

It is **not production wiring**:

- `agents.json` is unchanged.
- The existing `protocol: "pi-rpc"` default remains the production path.
- No Go runtime code imports this directory yet.
- Probes avoid real model calls and real external side effects.

## Probe script

```bash
node server/internal/agent/pi_sdk_bridge/probe.mjs capabilities --cwd /root/mindfs --agent-dir /root/.pi/agent --json
node server/internal/agent/pi_sdk_bridge/probe.mjs list-sessions --cwd /root/mindfs --agent-dir /root/.pi/agent --json
node server/internal/agent/pi_sdk_bridge/probe.mjs session-smoke --cwd /root/mindfs --json
node server/internal/agent/pi_sdk_bridge/probe.mjs extension-ui-smoke --json
node server/internal/agent/pi_sdk_bridge/probe.mjs runtime-replacement-smoke --cwd /root/mindfs --json
```

The script prints explicit JSON success/failure envelopes. Unknown commands, bad flags, SDK failures, and invalid JSONL input return `success: false` with a structured error object instead of silently exiting.

## What each probe validates

### `capabilities`

Loads Pi SDK resources through `DefaultResourceLoader` and reports safe metadata only:

- SDK availability/version.
- Support flags for sessions, fork/clone/import, extension UI, resources, steer/follow-up, and compaction.
- Skills, prompts, extensions, themes, and context file paths/counts.
- Slash command metadata from extension registrations, prompt templates, and skills.
- Model counts and safe model metadata.

Security stance:

- Project trust is forced false during the passive probe.
- Raw `AGENTS.md`/context file contents are never returned.
- Credential values are never returned.
- Extension commands are not executed by this probe.

### `list-sessions`

Uses `SessionManager.list` and then opens each returned session, where possible, to report:

- session path/id/name/cwd/timestamps/message count,
- entry count,
- leaf id,
- current branch length,
- tree root count/max depth,
- entry type counts.

Pass `--session-dir` to point at a deterministic test directory instead of the SDK default session directory for the cwd.

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

The probe binds a recording RPC-style UI context and returns the stable event schema plus canned responses. This proves a bridge can test extension UI contracts deterministically.

### `runtime-replacement-smoke`

Creates a temporary persistent SDK session and drives `AgentSessionRuntime` replacement APIs without model calls:

- initial session creation,
- `fork(entryId, { position: "at" })`,
- `newSession()`,
- subscription/extension rebinding through `runtime.setRebindSession(...)`.

The output records session files/ids, parent session linkage for fork, and rebinding counts.

## JSONL mode

`jsonl` mode is a minimal stdio contract sketch for future Go integration tests:

```bash
printf '%s\n' \
  '{"id":"1","type":"start_test_runtime","scenario":"extension-ui"}' \
  '{"id":"2","type":"prompt","message":"/ui-demo"}' \
  '{"type":"extension_ui_response","id":"select-1","value":"sdk-bridge"}' \
  '{"type":"extension_ui_response","id":"confirm-1","confirmed":true}' \
  | node server/internal/agent/pi_sdk_bridge/probe.mjs jsonl
```

It emits deterministic `extension_ui_request` events for UI contract mapping. The current JSONL path is intentionally a protocol sketch, not a production session runtime.

## Rollback

Delete `server/internal/agent/pi_sdk_bridge/`. No production MindFS behavior depends on it.

## Known limitations

- The prototype imports the locally installed Pi SDK by absolute path (`/root/node-v22.22.0-linux-x64/...`) to avoid adding MindFS package/dependency wiring during the spike.
- `extension-ui-smoke` and `runtime-replacement-smoke` intentionally avoid real provider calls, so they validate SDK/runtime/UI seams rather than prompt quality.
- Full MindFS WebSocket event/response mapping remains a separate Go `pi-rpc` or future bridge integration task.
