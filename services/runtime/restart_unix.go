//go:build !windows

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func validateWebRestartSupported() error { return nil }

func restartWeb(ctx context.Context, repoRoot, stageDir string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	script := fmt.Sprintf(`set -eu
cd %s
(
  sleep 1
  printf '\n----- web restart requested at %%s -----\n' "$(date '+%%F %%T')"
  exec make --no-print-directory activate-web-stage WEB_STAGE_DIR=%s
) >> %s 2>&1 < /dev/null &
`, shellQuote(repoRoot), shellQuote(stageDir), shellQuote(WebRestartLogPath))
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", script)
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Run()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
