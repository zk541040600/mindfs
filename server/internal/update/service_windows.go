//go:build windows

package update

import (
	"io"
	"os/exec"
	"syscall"
)

func startReplacementProcess(currentPID int, exe string, args []string, stdout, stderr io.Writer, pkgDir, dstBin, dstAgents, dstWeb string) error {
	script := windowsReplacementScript(currentPID, exe, args, pkgDir, dstBin, dstAgents, dstWeb)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	return cmd.Start()
}
