//go:build windows

package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var resumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

const taskkillTimeout = 2 * time.Second

type processTree struct {
	job      windows.Handle
	attached bool
}

func newProcessTree(cmd *exec.Cmd) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create Job Object failed: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("configure Job Object failed: %w", err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
	return &processTree{job: job}, nil
}

func (tree *processTree) attach(cmd *exec.Cmd) error {
	if tree == nil || tree.job == 0 || cmd == nil || cmd.Process == nil {
		return fmt.Errorf("attach install process tree failed: Job Object and process are required")
	}
	var attachErr error
	if err := cmd.Process.WithHandle(func(rawHandle uintptr) {
		process := windows.Handle(rawHandle)
		if err := windows.AssignProcessToJobObject(tree.job, process); err != nil {
			attachErr = fmt.Errorf("assign install process to Job Object failed: %w", err)
			return
		}
		tree.attached = true
		status, _, callErr := resumeProcess.Call(rawHandle)
		if status != 0 {
			attachErr = fmt.Errorf("resume install process failed: NTSTATUS=0x%x error=%v", status, callErr)
		}
	}); err != nil {
		return fmt.Errorf("access install process handle failed: %w", err)
	}
	return attachErr
}

func (tree *processTree) terminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if tree != nil && tree.job != 0 && tree.attached {
		if err := windows.TerminateJobObject(tree.job, 1); err == nil {
			return nil
		}
	}
	taskkillCtx, cancel := context.WithTimeout(context.Background(), taskkillTimeout)
	defer cancel()
	output, treeErr := exec.CommandContext(
		taskkillCtx,
		"taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid),
	).CombinedOutput()
	if treeErr == nil {
		return nil
	}
	directErr := cmd.Process.Kill()
	if directErr != nil && !errors.Is(directErr, os.ErrProcessDone) {
		return fmt.Errorf(
			"taskkill process tree failed: %v: %s; kill direct process failed: %w",
			treeErr, strings.TrimSpace(string(output)), directErr,
		)
	}
	return fmt.Errorf(
		"taskkill process tree failed (direct process was terminated): %v: %s",
		treeErr, strings.TrimSpace(string(output)),
	)
}

func (tree *processTree) close() error {
	if tree == nil || tree.job == 0 {
		return nil
	}
	err := windows.CloseHandle(tree.job)
	tree.job = 0
	if err != nil {
		return fmt.Errorf("close Job Object failed: %w", err)
	}
	return nil
}
