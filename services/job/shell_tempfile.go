package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	typepath "github.com/fanlv/quartet/types/path"
)

// shellInstanceID is the per-process identifier embedded in shell temp dir
// names. It mixes the process start time (ns) with the PID so that a
// restart in a container where PIDs are reused (e.g. the quartet process
// is always PID 1) still produces a fresh identifier — the previous
// instance's dir is recognisably stale even though the PID matches.
//
// Captured once at package init so every call site agrees on the current
// instance's identifier without threading state.
var shellInstanceID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())

var (
	// processShellTempDir caches the process-isolated subdir for temp files.
	processShellTempDir string
	processShellTempMu  sync.Mutex
)

// parseShellTempDirName extracts the embedded identifier from a
// ".quartet-shell-*" directory name. It recognises two formats:
//   - new: ".quartet-shell-<startNanos>-<pid>" (current)
//   - legacy: ".quartet-shell-<pid>" (pre-instanceID naming)
//
// Legacy dirs are never owned by the current process (we always write the
// new format), so cleanup can treat them as stale provided no live quartet
// with a different PID is plausibly still using them.
func parseShellTempDirName(name string) (startNanos int64, pid int, legacy, ok bool) {
	const prefix = ".quartet-shell-"
	if !strings.HasPrefix(name, prefix) {
		return 0, 0, false, false
	}
	rest := name[len(prefix):]
	if rest == "" {
		return 0, 0, false, false
	}
	idx := strings.LastIndexByte(rest, '-')
	if idx <= 0 || idx == len(rest)-1 {
		// No dash (or leading/trailing dash): treat as legacy "<pid>" form.
		p, err := strconv.Atoi(rest)
		if err != nil || p <= 0 {
			return 0, 0, false, false
		}
		return 0, p, true, true
	}
	n, err := strconv.ParseInt(rest[:idx], 10, 64)
	if err != nil || n <= 0 {
		return 0, 0, false, false
	}
	p, err := strconv.Atoi(rest[idx+1:])
	if err != nil || p <= 0 {
		return 0, 0, false, false
	}
	return n, p, false, true
}

// staleWorkdirTempAge is the minimum age before a leftover temp file in a
// workspace workdir is considered orphaned. A live shell step rarely runs
// for more than a few minutes; the threshold is generous so an unusually
// long script (e.g. a large build) cannot have its on-disk script deleted
// out from under bash. Crashed-instance leftovers always satisfy this
// threshold by the time the next quartet boot reaches startup cleanup.
const staleWorkdirTempAge = 6 * time.Hour

// cleanupResidualTempFiles removes leftover per-process shell temp
// subdirectories from prior crashed Quartet instances. Active instances
// keep their own instance-scoped subdir (<startNanos>-<pid>), so startup
// cleanup by one process cannot delete another live process's live
// script/control files.
//
// The identifier includes the process start timestamp in addition to the
// PID so that a Docker/container restart — where the new process inherits
// PID 1, the same as the crashed one — still gets cleaned up. Matching on
// PID alone skipped those dirs forever because pid == currentPID was
// always true.
//
// workdirs lets the caller pass workspace project directories that may
// contain leftover .quartet-shell-*.sh / .quartet-ctrl-*.txt files
// from steps whose normal cleanup was bypassed (SIGKILL, OOM kill,
// `make web-stop`). Files there are deleted only when older than
// staleWorkdirTempAge so a concurrent live run on another quartet
// instance is not disturbed.
func cleanupResidualTempFiles(fm fileserver.FileManager, workdirs []string) {
	cleanupResidualShellTempDirs(fm)
	cleanupResidualWorkdirTempFiles(workdirs)
}

func cleanupResidualShellTempDirs(fm fileserver.FileManager) {
	baseDir, err := shellTempBaseDir()
	if err != nil {
		return
	}
	entries, err := fm.FileList(&fsmodel.FileListRequest{Path: baseDir})
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		logger.Debugf(context.Background(), "[shell] list shell temp base dir failed: %v", err)
		return
	}
	currentPID := os.Getpid()
	for _, entry := range entries.Files {
		if !entry.IsDir {
			continue
		}
		startNanos, pid, legacy, ok := parseShellTempDirName(entry.Name)
		if !ok {
			continue
		}
		// New-format dir owned by the current process: never touch it.
		if !legacy && fmt.Sprintf("%d-%d", startNanos, pid) == shellInstanceID {
			continue
		}
		// Conservative multi-instance guard: if a DIFFERENT live process
		// holds the PID encoded in this dir, assume it still owns the
		// files. Same-PID cases (our own PID, or PID reused after death)
		// fall through to delete — the instanceID check above already
		// protected our live dir.
		if pid != currentPID && processExists(pid) {
			continue
		}
		staleDir := filepath.Join(baseDir, entry.Name)
		if err := fm.FileDelete(&fsmodel.FileDeleteRequest{Path: staleDir}); err == nil {
			logger.Debugf(context.Background(), "[shell] cleaned stale shell temp dir: %s", staleDir)
		} else {
			logger.Warnf(context.Background(), "[shell] cleanup stale shell temp dir failed: %s err=%v", staleDir, err)
		}
	}
}

// cleanupResidualWorkdirTempFiles scans each workspace workdir and removes
// orphaned .quartet-shell-*.sh / .quartet-ctrl-*.txt files older than
// staleWorkdirTempAge. These show up when a shell step's defer cleanup is
// bypassed (process was SIGKILLed, OOM killed, or the parent died before
// the deferred FileDelete ran).
func cleanupResidualWorkdirTempFiles(workdirs []string) {
	if len(workdirs) == 0 {
		return
	}
	cutoff := time.Now().Add(-staleWorkdirTempAge)
	seen := make(map[string]struct{}, len(workdirs))
	for _, dir := range workdirs {
		if dir == "" {
			continue
		}
		if _, dup := seen[dir]; dup {
			continue
		}
		seen[dir] = struct{}{}
		cleanupWorkdirTempFiles(dir, cutoff)
	}
}

func cleanupWorkdirTempFiles(workdir string, cutoff time.Time) {
	entries, err := os.ReadDir(workdir)
	if err != nil {
		// Missing/inaccessible workdir is not actionable here — skip
		// silently so a misconfigured workspace doesn't spam WARNs at
		// every boot.
		logger.Debugf(context.Background(), "[shell] list workspace workdir for cleanup failed: %s err=%v", workdir, err)
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		if !isWorkdirShellTempName(name) {
			continue
		}
		full := filepath.Join(workdir, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(full); err == nil {
			logger.Debugf(context.Background(), "[shell] cleaned stale workdir temp file: %s", full)
		} else if !os.IsNotExist(err) {
			logger.Warnf(context.Background(), "[shell] cleanup stale workdir temp file failed: %s err=%v", full, err)
		}
	}
}

func isWorkdirShellTempName(name string) bool {
	return (strings.HasPrefix(name, ".quartet-shell-") && strings.HasSuffix(name, ".sh")) ||
		(strings.HasPrefix(name, ".quartet-ctrl-") && strings.HasSuffix(name, ".txt"))
}

func shellTempBaseDir() (string, error) {
	if dir, err := typepath.ShellTempDir(); err == nil && dir != "" {
		return dir, nil
	}
	return filepath.Join(os.TempDir(), "quartet", "shell"), nil
}

func shellTempDir() (string, error) {
	baseDir, err := shellTempBaseDir()
	if err != nil {
		return "", err
	}
	// Keep per-process temp files isolated inside an instance-scoped subdir.
	// The instance ID mixes process start time with PID so a Docker
	// container restart (where PID is reused, typically 1) still gets a
	// fresh dir name — otherwise cleanupResidualTempFiles can never tell a
	// prior crashed instance apart from the current one.
	return filepath.Join(baseDir, ".quartet-shell-"+shellInstanceID), nil
}

func ensureShellTempDir(fm fileserver.FileManager) (string, error) {
	processShellTempMu.Lock()
	defer processShellTempMu.Unlock()

	// Recompute desired path every time so tests that swap LOCAL_MEMORY via
	// t.Setenv don't get stuck with a stale cached directory.
	dir, err := shellTempDir()
	if err != nil {
		return "", err
	}
	if processShellTempDir == dir {
		return processShellTempDir, nil
	}
	if err := fm.MkDir(&fsmodel.MkDirRequest{Path: dir}); err != nil {
		return "", fmt.Errorf("ensure shell temp dir failed: %w", err)
	}
	processShellTempDir = dir
	return dir, nil
}

func writeShellTempFile(fm fileserver.FileManager, workdir, scriptContent string) (string, func(), error) {
	tempDir := workdir
	if tempDir == "" {
		dir, err := ensureShellTempDir(fm)
		if err != nil {
			return "", func() {}, fmt.Errorf("resolve temp dir failed: %w", err)
		}
		tempDir = dir
	}

	var content string
	if strings.HasPrefix(scriptContent, "#!") {
		if idx := strings.Index(scriptContent, "\n"); idx >= 0 {
			content = scriptContent[:idx+1] + shellHelpers + scriptContent[idx+1:]
		} else {
			content = scriptContent + "\n" + shellHelpers
		}
	} else {
		content = "#!/usr/bin/env bash\n" + shellHelpers + scriptContent
	}

	result, err := fm.FileCreateTemp(&fsmodel.FileCreateTempRequest{
		Dir:     tempDir,
		Pattern: ".quartet-shell-*.sh",
		Content: content,
		Mode:    0o700,
	})
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() {
		_ = fm.FileDelete(&fsmodel.FileDeleteRequest{Path: result.File})
	}
	return result.File, cleanup, nil
}

// createControlFile creates a temporary control file in the given directory
// and returns its path and a cleanup function.
func createControlFile(fm fileserver.FileManager, workdir string) (string, func(), error) {
	dir := workdir
	if dir == "" {
		tmp, err := ensureShellTempDir(fm)
		if err != nil {
			return "", func() {}, fmt.Errorf("resolve temp dir failed: %w", err)
		}
		dir = tmp
	}
	result, err := fm.FileCreateTemp(&fsmodel.FileCreateTempRequest{
		Dir:     dir,
		Pattern: ".quartet-ctrl-*.txt",
		Content: "",
		Mode:    0o600,
	})
	if err != nil {
		return "", func() {}, err
	}
	return result.File, func() { _ = fm.FileDelete(&fsmodel.FileDeleteRequest{Path: result.File}) }, nil
}
