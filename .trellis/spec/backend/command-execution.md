# Command Execution Contracts

## Scenario: Stable long-shell identity within one command session

### 1. Scope / Trigger

- Trigger: `commandexec.StartInSession` creates or reuses a persistent interactive shell for a root/session pair.
- A caller may omit `Options.Shell` to select the configured default, then later explicitly select that same shell. These are equivalent selections, not separate terminals.

### 2. Signatures

- `func StartInSession(ctx context.Context, opts Options) (Process, error)`
- `func ResolveConfiguredShell(shells []ShellSpec, requestedShell ...string) (ShellSpec, bool)`
- Internal identity helper: `func resolvedSessionShell(opts Options) string`

### 3. Contracts

- The long-shell key is `(rootID, sessionKey, resolvedShellCommand)`, where `resolvedShellCommand` is the canonical command path returned by `ResolveConfiguredShell`.
- An omitted shell selection and an explicit request for the configured default must use the same key and therefore preserve shell state such as working directory and exported variables.
- If no configured shell resolves, process creation retains existing failure behavior (`no configured shell found`); key normalization must not bypass the configured-shell allowlist.
- `CloseSession(rootID, sessionKey)` must still terminate every shell variant belonging to that logical session.

### 4. Validation & Error Matrix

| Condition | Result |
| --- | --- |
| `RootID` or `Session` absent | Fall back to one-shot `Start`; no long-shell cache entry |
| Configured default omitted, then explicitly requested | One persistent shell is reused |
| Requested shell is not configured or cannot resolve | Start fails; no usable process is created |
| Same root/session with a different configured shell | Separate long shell, intentionally |
| Session closed | All cached shells with that root/session prefix are killed and removed |

### 5. Good / Base / Bad Cases

- Good: `cd subdir` using the implicit default `sh`, followed by `pwd` with `Shell: "sh"`, prints `subdir`.
- Base: consecutive commands both omit `Shell` and preserve state in the configured default terminal.
- Bad: use the raw request strings `""` and `"sh"` as cache keys. They create two terminals even when both resolve to the same executable, losing session state and wasting a process.

### 6. Tests Required

- `server/internal/commandexec/runner_test.go`: run `cd` with the implicit configured default, then `pwd` with its explicit command name; assert the second command observes the changed directory.
- Keep the existing close-session test to ensure canonicalization does not narrow cleanup.
- Run `/root/.local/go1.25/bin/go test ./server/internal/commandexec -count=1` and `/root/.local/go1.25/bin/go test -race ./server/internal/commandexec -count=1`.

### 7. Wrong vs Correct

#### Wrong

```go
return defaultLongShells.start(ctx, longShellKey(rootID, sessionKey, opts.Shell), opts)
```

The UI's omitted default and explicit default shell have different raw strings, despite selecting the same executable.

#### Correct

```go
shellKey := resolvedSessionShell(opts)
return defaultLongShells.start(ctx, longShellKey(rootID, sessionKey, shellKey), opts)
```

The cache identity follows the resolved shell selected by the allowlist, so equivalent selections reuse one terminal.
