//go:build !windows

package job

import (
	"os/exec"
	"syscall"
	"time"
)

// shellSysProcAttr returns Unix process attributes that place the child in its
// own process group so the entire tree can be killed with kill(-pgid).
func shellSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// shellKillProcessGroup gracefully terminates the process group, then force-kills
// if it doesn't exit within the grace period.
func shellKillProcessGroup(cmd *exec.Cmd, processExited <-chan struct{}, gracePeriod time.Duration) {
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-processExited:
		case <-time.After(gracePeriod):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
