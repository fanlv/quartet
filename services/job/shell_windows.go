//go:build windows

package job

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// shellSysProcAttr returns Windows process attributes that place the child in
// its own process group.
func shellSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// shellKillProcessGroup kills the process on Windows.
func shellKillProcessGroup(cmd *exec.Cmd, processExited <-chan struct{}, gracePeriod time.Duration) {
	_ = cmd.Process.Kill()
	select {
	case <-processExited:
	case <-time.After(gracePeriod):
	}
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(h)
	// Keep an os.Process reference out of callers' hands and release it promptly
	// if FindProcess succeeds. OpenProcess above is the real liveness check;
	// FindProcess alone can succeed for non-existent PIDs on Windows.
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Release()
	}
	return true
}
