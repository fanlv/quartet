package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const WebRestartLogPath = "/tmp/quartet-web-restart.log"
const webReleaseTimeout = 10 * time.Minute
const startupCheckTimeout = time.Minute

var webRestartMu sync.Mutex
var webActivationInProgress bool

// RestartWeb builds a complete candidate release and boots the candidate on an
// isolated loopback listener before scheduling the process hand-off. The
// running binary and static assets are untouched until both checks have passed.
func RestartWeb(_ context.Context) error {
	if !webRestartMu.TryLock() {
		return fmt.Errorf("a Web release build is already in progress; full log: %s", WebRestartLogPath)
	}
	defer webRestartMu.Unlock()
	if webActivationInProgress {
		return fmt.Errorf("a validated Web release is already waiting to restart; full log: %s", WebRestartLogPath)
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}
	if err := validateWebRestartSupported(); err != nil {
		return err
	}
	if strings.TrimSpace(os.Getenv("LOCAL_MEMORY")) == "" {
		return fmt.Errorf("LOCAL_MEMORY environment variable is not set")
	}

	binDir := filepath.Join(repoRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create Web release staging parent %q failed: %w", binDir, err)
	}
	stageDir, err := os.MkdirTemp(binDir, ".web-release-")
	if err != nil {
		return fmt.Errorf("create Web release staging directory failed: %w", err)
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stageDir)
		}
	}()

	logFile, err := os.OpenFile(WebRestartLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open Web release log %q failed: %w", WebRestartLogPath, err)
	}
	defer logFile.Close()

	// The request context may be cancelled by a browser/proxy timeout while the
	// build is still healthy. Keep the release transaction on its own bounded
	// context; callers still receive the final build error when connected.
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), webReleaseTimeout)
	defer cancelBuild()
	if err := runReleaseCommand(
		buildCtx, logFile, repoRoot, os.Environ(),
		"make", "--no-print-directory", "stage-web", "WEB_STAGE_DIR="+stageDir,
	); err != nil {
		return fmt.Errorf("build candidate Web release failed: %w", err)
	}
	if err := copyCurrentExecutable(filepath.Join(stageDir, "previous-quartet-web")); err != nil {
		return fmt.Errorf("snapshot currently running Web binary for rollback failed: %w", err)
	}

	checkMemory, err := os.MkdirTemp("", "quartet-web-startup-check-")
	if err != nil {
		return fmt.Errorf("create candidate startup-check directory failed: %w", err)
	}
	defer os.RemoveAll(checkMemory)
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), startupCheckTimeout)
	defer cancelCheck()
	checkAddr, err := availableLoopbackAddress()
	if err != nil {
		return fmt.Errorf("reserve candidate startup-check address failed: %w", err)
	}
	checkEnv := replaceEnv(os.Environ(), map[string]string{
		"LOCAL_MEMORY":          checkMemory,
		"QUARTET_STARTUP_CHECK": "1",
		"QUARTET_LISTEN_ADDR":   checkAddr,
		"QUARTET_STATIC_DIR":    filepath.Join(stageDir, "static"),
	})
	if err := runReleaseCommand(
		checkCtx, logFile, repoRoot, checkEnv, filepath.Join(stageDir, "quartet-web"),
	); err != nil {
		return fmt.Errorf("candidate Web startup check failed: %w", err)
	}

	if err := logFile.Sync(); err != nil {
		return fmt.Errorf("flush Web release log %q failed: %w", WebRestartLogPath, err)
	}
	if err := restartWeb(context.Background(), repoRoot, stageDir); err != nil {
		return fmt.Errorf("schedule validated Web release activation failed: %w", err)
	}
	webActivationInProgress = true
	time.AfterFunc(2*time.Minute, func() {
		webRestartMu.Lock()
		webActivationInProgress = false
		webRestartMu.Unlock()
	})
	keepStage = true
	return nil
}

func runReleaseCommand(ctx context.Context, logFile *os.File, dir string, env []string, name string, args ...string) error {
	var output bytes.Buffer
	writer := io.MultiWriter(logFile, &output)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = writer
	cmd.Stderr = writer
	startedAt := time.Now()
	_, _ = fmt.Fprintf(logFile, "\n----- %s %s at %s -----\n", name, strings.Join(args, " "), startedAt.Format(time.RFC3339))
	if err := cmd.Run(); err != nil {
		cause := err
		if ctx.Err() != nil {
			cause = ctx.Err()
		}
		return fmt.Errorf("%s %s failed after %s: %w\n%s\nfull log: %s",
			name, strings.Join(args, " "), time.Since(startedAt).Round(time.Millisecond), cause, output.String(), WebRestartLogPath)
	}
	_, _ = fmt.Fprintf(logFile, "----- completed in %s -----\n", time.Since(startedAt).Round(time.Millisecond))
	return nil
}

func replaceEnv(base []string, replacements map[string]string) []string {
	out := make([]string, 0, len(base)+len(replacements))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			if _, replace := replacements[key]; replace {
				continue
			}
		}
		out = append(out, item)
	}
	for key, value := range replacements {
		out = append(out, key+"="+value)
	}
	return out
}

func availableLoopbackAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return addr, nil
}

func copyCurrentExecutable(dest string) error {
	source, err := os.Executable()
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		// Linux can still expose an executing, unlinked binary through procfs.
		fallback, fallbackErr := os.Open("/proc/self/exe")
		if fallbackErr != nil {
			return fmt.Errorf("open current executable %q failed: %v; procfs fallback failed: %w", source, err, fallbackErr)
		}
		in = fallback
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	removeDest := true
	defer func() {
		_ = out.Close()
		if removeDest {
			_ = os.Remove(dest)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	removeDest = false
	return nil
}

func findRepoRoot() (string, error) {
	if wd, err := os.Getwd(); err == nil {
		if root, ok := walkUpForMakefile(wd); ok {
			return root, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		if root, ok := walkUpForMakefile(filepath.Dir(exe)); ok {
			return root, nil
		}
	}
	return "", fmt.Errorf("cannot locate repository root containing Makefile")
}

func walkUpForMakefile(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
