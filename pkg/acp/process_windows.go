//go:build windows

package acp

import (
	"context"
	"os/exec"
	"strconv"

	"github.com/fanlv/quartet/pkg/logger"
)

// terminate kills the subprocess tree on Windows using taskkill /T (tree kill).
func (c *Conn) terminate() {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	if !c.isAlive() {
		return
	}

	// There is no portable Windows equivalent of POSIX SIGTERM here.
	// Conn.Close closes the ACP transport before calling terminate; give
	// cooperative agents a chance to exit after stdio shutdown, then force-kill
	// the whole process tree if they are still alive.
	if c.waitForProcessExit(gracefulShutdownTimeout) {
		return
	}
	_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(c.cmd.Process.Pid)).Run()
	c.waitForProcessExit(gracefulShutdownTimeout)
}

// CleanupOrphanedConns is a no-op on Windows because /proc is not available
// and orphaned processes are handled differently by the OS.
func CleanupOrphanedConns() {
	logger.Infof(context.Background(), "[ACP] orphan cleanup not supported on Windows, skipping")
}
