//go:build !windows

package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

type processTree struct{}

func newProcessTree(cmd *exec.Cmd) (*processTree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &processTree{}, nil
}

func (*processTree) attach(*exec.Cmd) error {
	return nil
}

func (*processTree) terminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	// Setpgid makes the root PID the process-group ID. Addressing the group
	// directly still works after a short-lived shell root has exited, as long
	// as one of its installer descendants remains in the group.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	} else if errors.Is(err, syscall.ESRCH) {
		return nil
	} else {
		groupErr := err
		if directErr := cmd.Process.Kill(); directErr != nil && !errors.Is(directErr, os.ErrProcessDone) {
			return fmt.Errorf("kill process group failed: %v; kill direct process failed: %w", groupErr, directErr)
		}
		return fmt.Errorf("kill process group failed (direct process was terminated): %w", groupErr)
	}
}

func (*processTree) close() error {
	return nil
}
