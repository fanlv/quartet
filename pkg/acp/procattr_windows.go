//go:build windows

package acp

import "syscall"

// sysProcAttr returns platform-specific process attributes for ACP subprocesses.
// On Windows, we use CREATE_NEW_PROCESS_GROUP so the subprocess can be killed
// as a group via GenerateConsoleCtrlEvent / taskkill, analogous to Unix Setpgid.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
