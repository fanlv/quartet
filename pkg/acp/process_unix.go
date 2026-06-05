//go:build !windows

package acp

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
)

// terminate kills the subprocess and its entire process group.
func (c *Conn) terminate() {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	if !c.isAlive() {
		return
	}

	pgid, err := syscall.Getpgid(c.cmd.Process.Pid)
	if err != nil {
		// Process already exited.
		c.waitForProcessExit(gracefulShutdownTimeout)
		return
	}

	// Try graceful shutdown first so the agent can persist state.
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	select {
	case <-c.processDone:
		return
	case <-time.After(gracefulShutdownTimeout):
		// Force kill if graceful shutdown timed out.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		c.waitForProcessExit(gracefulShutdownTimeout)
	}
}

// CleanupOrphanedConns finds ACP agent subprocesses orphaned by a previous
// parent crash and kills their process groups. This should be called once
// at server startup before any new connections are created.
//
// Matching has three stages:
//
//  1. Cheap filter — allowlist keyword appears as a substring of
//     /proc/<pid>/cmdline. Keywords like "coco", "openclaw", "gemini" are
//     specific enough to look distinctive but can still collide with
//     unrelated processes (e.g. /etc/sysop/mongoosev3-agent/plugin/
//     openclaw-collector/openclaw-collector or an ssh session running a
//     user command happening to contain "coco"). That substring match on
//     its own is NOT safe to escalate to kill(-pgid).
//
//  2. Marker guard — every ACP subprocess NewConn launches is started with
//     QUARTET_ACP_CHILD=1 in its env. Children inherit env across fork/
//     exec, so the whole subtree (including the sh/node wrappers npx
//     creates) carries the marker. Before we kill the PPID==1 root's
//     process group we require that marker on both the matched process
//     AND the root — a non-quartet root (sshd, mongoosev3-agent, any
//     system service) has no way to carry it and is skipped.
//
//  3. System-root blacklist — even if a future bug or env propagation
//     issue lets the marker leak into something it should not, we refuse
//     to kill anything whose root cmdline basename matches a known system
//     service (sshd, systemd, init, mongoose*-agent, ...). Belt-and-
//     suspenders: the marker check is the contract, this is the safety
//     net for when the contract is violated.
//
// Earlier revisions only did stage 1 and then kill(-pgid) on the root,
// which produced real system-level kills of sshd and sysop agents on
// machines where unrelated processes happened to share a keyword.
func CleanupOrphanedConns() {
	keywords := orphanCleanupKeywords()
	if len(keywords) == 0 {
		return
	}

	logger.Infof(context.Background(), "[ACP] scanning for orphaned ACP processes, keywords=%v", keywords)

	procs, ok := readProcSnapshot()
	if !ok {
		// /proc does not exist on macOS — this is expected and not an error.
		return
	}

	// Walk every matched process up to its PPID==1 ancestor. Dedup on the
	// ancestor so a 3-level tree (sh → node → binary) only kills once.
	killedRoots := make(map[int]bool)
	skippedLive := 0
	skippedNoMarker := 0
	skippedSystemRoot := 0

	for pid, info := range procs {
		matched := false
		for kw := range keywords {
			if strings.Contains(info.cmdline, kw) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		root, rootInfo, reachedInit := walkToOrphanRoot(procs, pid)
		if !reachedInit {
			// Still attached to a live parent (e.g. our own quartet
			// process). Not an orphan — leave it alone.
			skippedLive++
			logger.Debugf(context.Background(),
				"[ACP] matched live ACP process, skipping: pid=%d ppid=%d cmdline=%s",
				pid, info.ppid, renderCmdline(info.cmdline))
			continue
		}

		// Marker guard: both the matched process and the PPID==1 root must
		// carry QUARTET_ACP_CHILD. If either one lacks it we walked into
		// an unrelated system subtree via a substring false positive
		// (openclaw-collector under mongoosev3-agent, a user command
		// under sshd, etc.) — never kill those.
		//
		// This is the expected, designed-for safety path: the cheap
		// keyword prefilter is intentionally loose so it can collide
		// with system services, and the marker is the contract that
		// rejects those collisions. Logged at DEBUG (not WARN) because
		// the path triggering means the safety net is doing its job;
		// a WARN here would treat normal operation as an alarm. The
		// per-scan "skippedNoMarker=N" summary at the end of this
		// function is the operator-visible counter.
		matchedHasMarker := hasACPChildMarkerCached(procs, pid)
		rootHasMarker := hasACPChildMarkerCached(procs, root)
		if !matchedHasMarker || !rootHasMarker {
			skippedNoMarker++
			logger.Debugf(context.Background(),
				"[ACP] skip orphan kill, missing QUARTET_ACP_CHILD marker (substring false positive, safe by design): rootPid=%d rootHasMarker=%t rootCmdline=%s matchedPid=%d matchedHasMarker=%t matchedCmdline=%s",
				root, rootHasMarker, renderCmdline(rootInfo.cmdline),
				pid, matchedHasMarker, renderCmdline(info.cmdline))
			continue
		}

		// System-root blacklist: even when the marker is present, refuse
		// to send SIGKILL to a process group whose root looks like a host
		// service (sshd, systemd, init, mongoose*-agent, ...). The marker
		// check should already have caught this; this is the safety net.
		if name, blocked := matchedSystemRootBasename(rootInfo.cmdline); blocked {
			skippedSystemRoot++
			logger.Errorf(context.Background(),
				"[ACP] refusing to kill system root despite marker present: rootPid=%d rootBasename=%s rootCmdline=%s matchedPid=%d matchedCmdline=%s",
				root, name, renderCmdline(rootInfo.cmdline),
				pid, renderCmdline(info.cmdline))
			continue
		}

		if killedRoots[root] {
			continue
		}
		killedRoots[root] = true

		pgid, err := syscall.Getpgid(root)
		if err != nil {
			logger.Warnf(context.Background(),
				"[ACP] orphan root pgid lookup failed: pid=%d err=%v", root, err)
			continue
		}
		logger.Infof(context.Background(),
			"[ACP] killing orphaned ACP process tree: rootPid=%d pgid=%d rootCmdline=%s matchedPid=%d matchedCmdline=%s",
			root, pgid,
			renderCmdline(rootInfo.cmdline),
			pid,
			renderCmdline(info.cmdline))
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
			logger.Warnf(context.Background(),
				"[ACP] kill orphan process group failed: rootPid=%d pgid=%d err=%v",
				root, pgid, err)
		}
	}

	logger.Infof(context.Background(),
		"[ACP] orphan scan done: killed=%d skippedLiveMatches=%d skippedNoMarker=%d skippedSystemRoot=%d scannedPids=%d",
		len(killedRoots), skippedLive, skippedNoMarker, skippedSystemRoot, len(procs))
}

// systemRootBasenames lists basenames of processes that must never have
// SIGKILL delivered to their process group by the orphan scanner, even if
// the marker check somehow passes. The marker check is the contract; this
// is the safety net for when the contract is violated.
//
// Matched against the FIRST argv token (the executable path) of the
// PPID==1 root's cmdline. We compare both the full basename and a prefix
// for "mongoose-style" service families.
var systemRootBasenames = []string{
	"sshd",
	"systemd",
	"init",
	"crond", "cron",
	"rsyslogd",
	"dbus-daemon", "dbus-broker",
	"NetworkManager",
	"agetty", "login",
	"docker", "dockerd", "containerd", "containerd-shim",
	"kubelet", "kube-proxy",
	"runc",
}

// systemRootPrefixes catches name families that ship many sibling
// binaries — e.g. /etc/sysop/mongoosev3-agent/mongoosev3-agent and the
// dozens of plugin/* binaries it spawns. Match by HasPrefix on the
// basename so future versions (mongoosev4-agent, ...) stay covered.
var systemRootPrefixes = []string{
	"mongoose", // sysop agents: mongoosev3-agent, mongoosev4-agent, ...
	"openclaw", // openclaw-collector and friends running under sysop
	"sysop",    // anything under /etc/sysop/...
}

// matchedSystemRootBasename returns (basename, true) if the rootCmdline's
// argv[0] basename is in the system-root blacklist; (basename, false)
// otherwise. The basename is returned unconditionally so callers can log
// it.
//
// Parsing order matters because some daemons rewrite argv[0] via
// setproctitle. sshd's argv[0] looks like `sshd: /usr/sbin/sshd -D
// [listener] 0 of 10-100 startups` — a single NUL-terminated string that
// embeds spaces and slashes. We take the first NUL-bounded chunk, then
// the first whitespace token, then strip leading path and trailing
// punctuation so `sshd:` collapses to `sshd`.
func matchedSystemRootBasename(rootCmdline string) (string, bool) {
	if rootCmdline == "" {
		return "", false
	}
	first := rootCmdline
	if i := strings.IndexByte(first, '\x00'); i >= 0 {
		first = first[:i]
	}
	if i := strings.IndexAny(first, " \t"); i >= 0 {
		first = first[:i]
	}
	if i := strings.LastIndexByte(first, '/'); i >= 0 {
		first = first[i+1:]
	}
	first = strings.TrimRight(first, ":,;")
	if first == "" {
		return "", false
	}
	if slices.Contains(systemRootBasenames, first) {
		return first, true
	}
	for _, p := range systemRootPrefixes {
		if strings.HasPrefix(first, p) {
			return first, true
		}
	}
	return first, false
}

// renderCmdline turns /proc/<pid>/cmdline's NUL-separated argv into a
// space-separated string for human-readable logs.
func renderCmdline(cmdline string) string {
	return strings.TrimRight(strings.ReplaceAll(cmdline, "\x00", " "), " ")
}

type procInfo struct {
	ppid    int
	cmdline string
	// marker caches whether /proc/<pid>/environ contains QUARTET_ACP_CHILD=1.
	// nil means the value has not been read from /proc yet.
	marker *bool
}

// hasACPChildMarkerCached reads/caches the ACP child marker into procs.
// It mutates procs and must be called only from the single-threaded orphan scan.
func hasACPChildMarkerCached(procs map[int]procInfo, pid int) bool {
	info, ok := procs[pid]
	if !ok {
		return false
	}
	if info.marker != nil {
		return *info.marker
	}
	v := readHasACPChildMarker(pid)
	info.marker = &v
	procs[pid] = info
	return v
}

// readProcSnapshot returns pid → (ppid, cmdline) for every live process under
// /proc except the caller itself. The environment marker is read lazily only
// for keyword-matched orphan candidates. ok=false when /proc is not present
// (macOS).
func readProcSnapshot() (map[int]procInfo, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}

	procs := make(map[int]procInfo, len(entries))
	myPID := os.Getpid()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == myPID {
			continue
		}

		statBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		stat := string(statBytes)
		closeIdx := strings.LastIndex(stat, ")")
		if closeIdx < 0 || closeIdx+2 >= len(stat) {
			continue
		}
		rest := strings.Fields(stat[closeIdx+2:]) // "state ppid ..."
		if len(rest) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(rest[1])
		if err != nil {
			continue
		}

		cmdlineBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		procs[pid] = procInfo{
			ppid:    ppid,
			cmdline: string(cmdlineBytes),
		}
	}
	return procs, true
}

// readHasACPChildMarker reports whether /proc/<pid>/environ contains
// QUARTET_ACP_CHILD=1. Returns false on any read error (permission
// denied for a foreign-user process, process gone, empty environ, etc.) —
// "false" is the safe default since the orphan scanner only ever uses
// this to authorize a kill.
func readHasACPChildMarker(pid int) bool {
	envBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil || len(envBytes) == 0 {
		return false
	}
	target := ACPChildMarkerEnv + "=" + ACPChildMarkerValue
	for kv := range strings.SplitSeq(string(envBytes), "\x00") {
		if kv == target {
			return true
		}
	}
	return false
}

// walkToOrphanRoot walks up the parent chain starting at `pid`. Returns the
// ancestor whose PPID==1 along with its procInfo and ok=true. If the chain
// exits the snapshot or loops before reaching PPID==1, returns ok=false.
func walkToOrphanRoot(procs map[int]procInfo, pid int) (int, procInfo, bool) {
	curInfo, ok := procs[pid]
	if !ok {
		return 0, procInfo{}, false
	}
	cur := pid
	seen := map[int]bool{pid: true}
	for curInfo.ppid != 1 {
		parent, ok := procs[curInfo.ppid]
		if !ok || seen[curInfo.ppid] {
			return 0, procInfo{}, false
		}
		seen[curInfo.ppid] = true
		cur, curInfo = curInfo.ppid, parent
	}
	return cur, curInfo, true
}
