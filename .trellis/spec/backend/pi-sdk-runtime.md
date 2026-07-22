# Pi SDK Runtime Contracts

## Scenario: Model discovery follows the service-owned SDK runtime

### 1. Scope / Trigger

- Trigger: `server/internal/agent/pi_sdk_bridge/probe.mjs` creates a production
  Pi JSONL runtime with `createAgentSessionServices` and
  `createAgentSessionFromServices`.
- Pi SDK upgrades can move model ownership without breaking session creation or
  command discovery. MindFS must keep model discovery and selection attached to
  the same service-owned model source used to create the session.

### 2. Signatures

- JSONL start request: `{"type":"start_sdk_runtime","model"?:string,"mode"?:string,"sessionId"?:string}`
- JSONL model request: `{"type":"get_available_models","id"?:string}`
- JSONL selection request: `{"type":"set_model","provider":string,"modelId":string,"id"?:string}`
- Current SDK: `services.modelRuntime.getAvailable()` and
  `services.modelRuntime.getModel(provider, modelId)`.
- Compatibility SDK: `services.modelRegistry.getAvailable()` and
  `services.modelRegistry.find(provider, modelId)`.

### 3. Contracts

- Resolve the model source once from `services.modelRuntime`, falling back to
  `services.modelRegistry` for older supported Pi SDKs.
- Do not read `session.modelRegistry`; current Pi sessions do not own or expose
  that object.
- Filter the returned list through `filterModelsByPiEnabledModels` before
  returning it or validating a selection.
- The session owns only active state such as `session.model` and
  `session.thinkingLevel`.

### 4. Validation & Error Matrix

| Condition | Result |
| --- | --- |
| Neither service model source exists | Start fails with `E_SDK_LOAD` |
| Model reference is not `provider/modelId` | Start fails with `E_PARAM` |
| Requested model is absent | Start or selection fails with `E_PARAM` |
| Model discovery fails | `/api/agents` reports `models_error`; it must not claim a usable model list |
| Model discovery succeeds | Pi exposes models and modes and remains selectable |

### 5. Good / Base / Bad Cases

- Good: current Pi uses `services.modelRuntime`; models and selection both use
  that same object.
- Base: an older SDK exposes `services.modelRegistry`; the compatibility
  fallback continues to list and resolve models.
- Bad: command discovery succeeds but models are read from
  `session.modelRegistry`; Pi appears installed yet the Agent menu is empty.

### 6. Tests Required

- The real-SDK integration test must call `ListModels` after opening the Pi
  runtime and fail on SDK object-shape drift.
- Deterministic runtime-control tests must still cover list, select, and mode
  behavior without external provider calls.
- Live upgrade validation must refresh `/api/agents?refresh_agent=pi` and assert
  `available=true`, non-empty models/modes, and empty model/mode errors.

### 7. Wrong vs Correct

#### Wrong

```javascript
await session.modelRegistry.getAvailable();
```

#### Correct

```javascript
const modelRuntime = services.modelRuntime ?? services.modelRegistry;
await modelRuntime.getAvailable();
```

Model capability ownership stays with the service layer that constructed the
session, while active model state stays on the session.
