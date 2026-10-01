//go:build windows

package install

import "os/exec"

func prepareIsolatedCommand(*exec.Cmd) {}

func stopCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}
