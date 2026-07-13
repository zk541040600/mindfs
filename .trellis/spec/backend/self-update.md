# Self-Update Contracts

## Scenario: Windows detached updater preserves the installed bridge layout

### 1. Scope / Trigger

- Trigger: an authenticated and manifest-verified update is installed with restart on Windows, so startReplacementProcess invokes PowerShell after the old process exits.
- The package contains the pi_sdk_bridge runtime under server/internal/agent/pi_sdk_bridge; the detached process must copy it to the same layout used by normal installation.

### 2. Signatures

- func (s *Service) restartInstalledBinary(pkgDir string) error
- func startReplacementProcess(currentPID int, exe string, args []string, stdout, stderr io.Writer, pkgDir, dstBin, dstAgents, dstWeb string) error
- func windowsReplacementScript(currentPID int, exe string, args []string, pkgDir, dstBin, dstAgents, dstWeb string) string

### 3. Contracts

- The normal installer and the Windows detached updater both derive the bridge destination from the installation layout.
- In the detached script, $shareDir is Split-Path -Parent $dstAgents. It resolves to <prefix>/share/mindfs for installed layouts and the executable directory for portable layouts.
- $dstBridge is $shareDir/server\internal\agent\pi_sdk_bridge; it must be declared before the bridge copy branch runs.
- Script arguments and all paths remain PowerShell single-quoted with psQuote; do not concatenate them as executable PowerShell expressions.

### 4. Validation & Error Matrix

| Condition | Result |
| --- | --- |
| Verified Windows package contains bridge directory | Detached updater replaces bridge directory at derived destination |
| Package has no bridge directory | Existing bridge copy branch is skipped |
| $shareDir is undefined | Invalid: with $ErrorActionPreference = Stop, bridge path construction aborts restart |
| Argument or path contains a single quote | psQuote doubles it; PowerShell receives literal data |

### 5. Good / Base / Bad Cases

- Good: destination agents file is <prefix>/share/mindfs/agents.json; bridge destination becomes <prefix>/share/mindfs/server/internal/agent/pi_sdk_bridge.
- Base: a portable package derives its bridge destination from the executable directory.
- Bad: reference $shareDir without assigning it. The binary and web may copy, but the strict PowerShell script fails before the replacement process starts.

### 6. Tests Required

- server/internal/update/service_test.go: generated Windows script declares $shareDir from $dstAgents and derives $dstBridge from that variable.
- Run /root/.local/go1.25/bin/go test ./server/internal/update -count=1.
- Compile the Windows-specific replacement process with GOOS=windows GOARCH=amd64 /root/.local/go1.25/bin/go test -c -o /tmp/mindfs-update-windows.test.exe ./server/internal/update.

### 7. Wrong vs Correct

#### Wrong

~~~powershell
$dstBridge = Join-Path $shareDir 'server\internal\agent\pi_sdk_bridge'
~~~

No earlier assignment establishes $shareDir, so strict PowerShell error handling aborts the detached updater.

#### Correct

~~~powershell
$shareDir = Split-Path -Parent $dstAgents
$dstBridge = Join-Path $shareDir 'server\internal\agent\pi_sdk_bridge'
~~~

The destination stays coupled to destinationPaths, matching installed and portable layouts without a duplicate prefix parameter.
