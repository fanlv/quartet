//go:build windows

package executil

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// ConfigureProcessTreeCancellation makes context cancellation terminate the
// command and its children. Apply it to a CommandContext result before Start.
func ConfigureProcessTreeCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.WaitDelay = 3 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		output, err := Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").CombinedOutput()
		if err != nil {
			return fmt.Errorf("stop process tree: %w; output: %s", err, output)
		}
		return nil
	}
}
