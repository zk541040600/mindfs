package update

import (
	"strconv"
	"strings"
)

// windowsReplacementScript builds the detached updater script with paths derived from install destinations.
func windowsReplacementScript(currentPID int, exe string, args []string, pkgDir, dstBin, dstAgents, dstWeb string) string {
	quotedArgs := make([]string, 0, len(args))
	for _, arg := range args {
		quotedArgs = append(quotedArgs, psQuote(arg))
	}
	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$pidToWait = " + strconv.Itoa(currentPID),
		"$exe = " + psQuote(exe),
		"$pkgDir = " + psQuote(pkgDir),
		"$cleanupDir = Split-Path -Parent (Split-Path -Parent $pkgDir)",
		"$dstBin = " + psQuote(dstBin),
		"$dstAgents = " + psQuote(dstAgents),
		"$dstWeb = " + psQuote(dstWeb),
		"$shareDir = Split-Path -Parent $dstAgents",
		"$argList = @(" + strings.Join(quotedArgs, ", ") + ")",
		"for ($i = 0; $i -lt 100; $i++) {",
		"  if (-not (Get-Process -Id $pidToWait -ErrorAction SilentlyContinue)) { break }",
		"  Start-Sleep -Milliseconds 200",
		"}",
		"$srcBin = Join-Path $pkgDir 'mindfs.exe'",
		"New-Item -ItemType Directory -Force -Path (Split-Path -Parent $dstBin) | Out-Null",
		"Copy-Item -Force $srcBin $dstBin",
		"$srcAgents = Join-Path $pkgDir 'agents.json'",
		"if (Test-Path $srcAgents -PathType Leaf) {",
		"  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $dstAgents) | Out-Null",
		"  Copy-Item -Force $srcAgents $dstAgents",
		"}",
		"$srcWeb = Join-Path $pkgDir 'web'",
		"if (Test-Path $srcWeb -PathType Container) {",
		"  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $dstWeb) | Out-Null",
		"  if (Test-Path $dstWeb) { Remove-Item -Recurse -Force $dstWeb }",
		"  Copy-Item -Recurse $srcWeb $dstWeb",
		"}",
		"$srcBridge = Join-Path $pkgDir 'server\\internal\\agent\\pi_sdk_bridge'",
		"$dstBridge = Join-Path $shareDir 'server\\internal\\agent\\pi_sdk_bridge'",
		"if (Test-Path $srcBridge -PathType Container) {",
		"  if (Test-Path $dstBridge) { Remove-Item -Recurse -Force $dstBridge }",
		"  New-Item -ItemType Directory -Force -Path (Split-Path $dstBridge) | Out-Null",
		"  Copy-Item -Recurse $srcBridge $dstBridge",
		"}",
		"$env:MINDFS_INTERNAL_RESTART = '1'",
		"Start-Process -FilePath $exe -ArgumentList $argList -WindowStyle Hidden",
		"if ($cleanupDir -and (Test-Path $cleanupDir)) { Remove-Item -Recurse -Force $cleanupDir -ErrorAction SilentlyContinue }",
	}, "; ")
}

func psQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
