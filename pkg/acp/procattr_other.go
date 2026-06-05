//go:build !linux && !windows

package acp

import "syscall"

// sysProcAttr returns platform-specific process attributes for ACP subprocesses.
// On macOS/BSD, Pdeathsig is not available but Setpgid works.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true, // own process group so Close() can kill the entire tree via kill(-pgid)
	}
}
