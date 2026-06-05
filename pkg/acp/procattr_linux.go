package acp

import "syscall"

// sysProcAttr returns platform-specific process attributes for ACP subprocesses.
// On Linux, Pdeathsig ensures the kernel kills the child if the parent dies.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:   true,            // own process group so Close() can kill the entire tree via kill(-pgid)
		Pdeathsig: syscall.SIGKILL, // kernel kills this child if the parent dies (including SIGKILL)
	}
}
