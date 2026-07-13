# Hosted Agent Configuration Contracts

## Scenario: Remote metadata refresh must not gain local process authority

### 1. Scope / Trigger

- Trigger: `server/app` fetches `/api/agents` from `relayBaseURL` and passes the decoded result to `agent.MergeHostedConfig` before updating the agent pool and prober.
- A hosted response crosses a network trust boundary. Its `Definition` fields can otherwise reach `exec.Command`, agent protocols, environment injection, configured shells, and backup sources.

### 2. Signatures

- `func fetchHostedAgentConfig(ctx context.Context, endpoint string, localConfig agent.Config) (agent.Config, error)`
- `func MergeHostedConfig(hosted Config, local Config) Config`
- `func (p *Pool) UpdateConfig(cfg Config) Config`
- `func (p *Prober) UpdateConfig(ctx context.Context, cfg *Config)`

### 3. Contracts

- The effective executable configuration originates only from `localConfig`: its Agent definitions, command, args, env, protocol, working-directory template, lifecycle commands, backup defaults, shells, and relay URL are preserved.
- A hosted Agent never creates a new executable Agent entry. Only an Agent already present locally is considered.
- The only hosted field currently eligible to fill a local definition is `Brief`, and only when the local `Brief` is empty. A local nonempty brief remains authoritative.
- This boundary applies before `Pool.UpdateConfig` and `Prober.UpdateConfig`; a refresh therefore cannot cause an installed remotely named command to be probed or launched.

### 4. Validation & Error Matrix

| Condition | Result |
| --- | --- |
| Hosted Agent name not locally configured | Ignored |
| Hosted Agent matches local Agent with local brief | All local fields, including brief, remain unchanged |
| Hosted Agent matches local Agent with empty local brief | Hosted brief is copied; all executable fields remain local |
| Hosted response changes command, args, env, protocol, cwd, shell, or relay URL | Ignored |
| Hosted request fails or JSON is invalid | Existing pool configuration remains active; refresh logs an error |

### 5. Good / Base / Bad Cases

- Good: a local `pi` command stays `local-pi`; a hosted response may supply only its missing display brief.
- Base: a local `codex` keeps its configured command and runtime environment across periodic hosted refreshes.
- Bad: merge a hosted definition as the base configuration and use local fields only as sparse overrides. Missing local `command` or `args` then silently gives a network response authority to start local processes.

### 6. Tests Required

- `server/internal/agent/pool_test.go`: hosted command, args, env, protocol, cwd, lifecycle commands, shells, relay URL, and hosted-only Agent do not alter the effective local execution configuration.
- The same test must prove a missing local brief can still receive the hosted display value.
- Run `/root/.local/go1.25/bin/go test ./server/internal/agent ./server/app -count=1` and `/root/.local/go1.25/bin/go test -race ./server/internal/agent -count=1`.

### 7. Wrong vs Correct

#### Wrong

```go
return mergeConfigs(hosted, local)
```

The remote definition supplies the base command and local configuration only replaces fields it happens to define.

#### Correct

```go
merged := Config{
    Agents:       append([]Definition(nil), local.Agents...),
    Shells:       append([]Shell(nil), local.Shells...),
    RelayBaseURL: local.RelayBaseURL,
}
// Match only existing local agents and fill Brief only when it is empty.
```

The fetch can refresh presentation metadata, but it cannot add or modify process authority.
