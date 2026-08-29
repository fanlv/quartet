//go:build !windows

package acp

import (
	"strings"
	"testing"
)

func boolPtr(v bool) *bool {
	return &v
}

func resetAllowedAgentCommandsForTest(t *testing.T, commands ...string) {
	t.Helper()
	reset := func() {
		allowedAgentCommandsMu.Lock()
		allowedAgentCommands = make(map[string]bool)
		allowedAgentCommandsMu.Unlock()
	}
	reset()
	allowedAgentCommandsMu.Lock()
	for _, command := range commands {
		allowedAgentCommands[command] = true
	}
	allowedAgentCommandsMu.Unlock()
	t.Cleanup(reset)
}

// Reproduces the real-world orphan tree that slipped past the earlier
// PPID==1+keyword-on-same-process check:
// Real shape:
// PID 61915: `sh -c codex-acp`, PPID=1 (orphan root, cmdline lacks keyword)
// PID 61916: `node .../.bin/codex-acp`, PPID=61915
// PID 61923: `.../@agentclientprotocol/codex-acp/dist/index.js`, PPID=61916 (keyword match)
func TestWalkToOrphanRoot_NpxCodexAcpTree(t *testing.T) {
	procs := map[int]procInfo{
		61915: {ppid: 1, cmdline: "sh\x00-c\x00codex-acp\x00"},
		61916: {ppid: 61915, cmdline: "node\x00/root/.npm/_npx/e3854e347c184741/node_modules/.bin/codex-acp\x00"},
		61923: {ppid: 61916, cmdline: "/root/.npm/_npx/e3854e347c184741/node_modules/@agentclientprotocol/codex-acp/dist/index.js\x00"},
	}

	root, info, ok := walkToOrphanRoot(procs, 61923)
	if !ok {
		t.Fatalf("expected to reach orphan root from pid 61923, got ok=false")
	}
	if root != 61915 {
		t.Fatalf("expected root=61915 (sh wrapper), got %d", root)
	}
	if info.ppid != 1 {
		t.Fatalf("expected root ppid=1, got %d", info.ppid)
	}
}

// A process still attached to a live parent (e.g. the running quartet
// process) must NOT be treated as orphaned — walkToOrphanRoot must return
// ok=false so the caller leaves it alone.
func TestWalkToOrphanRoot_LiveParentNotOrphan(t *testing.T) {
	// quartet pid 1000 is alive (ppid points at some live init wrapper, but
	// that wrapper is NOT pid 1 — we simulate "parent chain exits the
	// snapshot" by omitting pid 500 from the map).
	procs := map[int]procInfo{
		1000: {ppid: 500, cmdline: "/usr/local/bin/quartet-web\x00"},
		1001: {ppid: 1000, cmdline: "node\x00/root/.npm/_npx/.bin/codex-acp\x00"},
		1002: {ppid: 1001, cmdline: ".../@agentclientprotocol/codex-acp/dist/index.js\x00"},
	}

	if _, _, ok := walkToOrphanRoot(procs, 1002); ok {
		t.Fatalf("expected ok=false for live subtree, got ok=true")
	}
}

// Direct-match case: the matched process itself has PPID==1.
func TestWalkToOrphanRoot_DirectOrphan(t *testing.T) {
	procs := map[int]procInfo{
		777: {ppid: 1, cmdline: "claude-agent-acp\x00--stdio\x00"},
	}
	root, _, ok := walkToOrphanRoot(procs, 777)
	if !ok || root != 777 {
		t.Fatalf("expected direct orphan root=777 ok=true, got root=%d ok=%v", root, ok)
	}
}

// Defensive: a cycle in the ppid chain must not loop forever.
func TestWalkToOrphanRoot_CycleGuard(t *testing.T) {
	procs := map[int]procInfo{
		100: {ppid: 200, cmdline: "@agentclientprotocol/codex-acp\x00"},
		200: {ppid: 100, cmdline: "sh\x00"},
	}
	if _, _, ok := walkToOrphanRoot(procs, 100); ok {
		t.Fatalf("expected ok=false on cycle, got ok=true")
	}
}

// renderCmdline must NUL→space and trim trailing separators so human logs
// read as ordinary argv strings.
func TestRenderCmdline(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"sh\x00-c\x00codex-acp\x00", "sh -c codex-acp"},
		{"claude-agent-acp\x00", "claude-agent-acp"},
		{"", ""},
	}
	for _, c := range cases {
		got := renderCmdline(c.in)
		if got != c.want {
			t.Fatalf("renderCmdline(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// matchedSystemRootBasename is the belt-and-suspenders blacklist: even if
// the marker check is bypassed by a future bug, refuse to kill anything
// rooted at one of these well-known host services.
func TestMatchedSystemRootBasename(t *testing.T) {
	cases := []struct {
		cmdline   string
		wantName  string
		wantBlock bool
	}{
		// sshd rewrites argv[0] via setproctitle: the whole "sshd:
		// <description>" string lives in argv[0] with embedded spaces and
		// slashes. After space-split + basename + trailing-`:` trim we
		// must still recover "sshd".
		{"sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups\x00", "sshd", true},
		{"/usr/sbin/sshd\x00-D\x00", "sshd", true},
		{"/etc/sysop/mongoosev3-agent/mongoosev3-agent\x00", "mongoosev3-agent", true},
		{"/etc/sysop/mongoosev3-agent/plugin/openclaw-collector/openclaw-collector\x00", "openclaw-collector", true},
		{"/etc/sysop/mongoosev4-agent/mongoosev4-agent\x00", "mongoosev4-agent", true},
		{"/lib/systemd/systemd\x00--user\x00", "systemd", true},
		{"/sbin/init\x00", "init", true},
		{"/usr/bin/dockerd\x00", "dockerd", true},
		{"/root/.npm/_npx/.../codex-acp\x00", "codex-acp", false},
		{"sh\x00-c\x00codex-acp\x00", "sh", false},
		{"", "", false},
	}
	for _, c := range cases {
		gotName, gotBlock := matchedSystemRootBasename(c.cmdline)
		if gotName != c.wantName || gotBlock != c.wantBlock {
			t.Fatalf("matchedSystemRootBasename(%q) = (%q,%v), want (%q,%v)",
				c.cmdline, gotName, gotBlock, c.wantName, c.wantBlock)
		}
	}
}

// Regression for the real incident where substring matching escalated to
// kill(-pgid) against system services because they merely shared a keyword
// with the allowlist:
//
//	sshd: /usr/sbin/sshd -D [listener]     PID 2895  PPID 1   (no QUARTET_ACP_CHILD)
//	└─ ...                                 ...
//	   └─ coco (some user command in ssh)  PID ...   (cmdline contains "coco")
//
//	/etc/sysop/mongoosev3-agent/mongoosev3-agent  PID 1039978  PPID 1
//	└─ plugin/openclaw-collector/openclaw-collector   (cmdline contains "openclaw")
//
// Substring matching will happily light up those trees. The marker check
// must be what stops them — none of these processes inherit
// QUARTET_ACP_CHILD=1 because they were never started by quartet.
func TestCleanupOrphanedConns_SkipsSystemTreesWithoutMarker(t *testing.T) {
	resetAllowedAgentCommandsForTest(t,
		"coco acp serve",
		"openclaw acp",
	)

	procs := map[int]procInfo{
		// sshd / coco subtree — substring "coco" will match on pid 30001
		// but no process in this tree carries the marker.
		2895:  {ppid: 1, cmdline: "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups\x00", marker: boolPtr(false)},
		30001: {ppid: 2895, cmdline: "coco\x00", marker: boolPtr(false)},

		// mongoosev3-agent / openclaw-collector subtree — substring
		// "openclaw" will match on the collector plugin. Again no marker.
		1039978: {ppid: 1, cmdline: "/etc/sysop/mongoosev3-agent/mongoosev3-agent\x00", marker: boolPtr(false)},
		1040346: {ppid: 1039978, cmdline: "/etc/sysop/mongoosev3-agent/plugin/openclaw-collector/openclaw-collector\x00", marker: boolPtr(false)},
	}

	kw := orphanCleanupKeywords()
	// Sanity: the keywords we rely on must actually be present.
	if !kw["coco"] || !kw["openclaw"] {
		t.Fatalf("expected coco+openclaw keywords in %v", kw)
	}

	// Simulate the orphan scan's decision loop without actually killing
	// anything. A true positive would request root 2895 or 1039978.
	wouldKill := map[int]bool{}
	for pid, info := range procs {
		matched := false
		for k := range kw {
			if strings.Contains(info.cmdline, k) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		root, _, ok := walkToOrphanRoot(procs, pid)
		if !ok {
			continue
		}
		if !hasACPChildMarkerCached(procs, pid) || !hasACPChildMarkerCached(procs, root) {
			continue
		}
		wouldKill[root] = true
	}

	if len(wouldKill) != 0 {
		t.Fatalf("expected zero kills for non-quartet system trees, got %v", wouldKill)
	}
}

// A quartet-owned orphan must still be killed: marker present on every
// node of the inherited chain, matched via keyword, root at PPID==1.
func TestCleanupOrphanedConns_KillsMarkedOrphanTree(t *testing.T) {
	resetAllowedAgentCommandsForTest(t, "npx @agentclientprotocol/codex-acp")

	procs := map[int]procInfo{
		61915: {ppid: 1, cmdline: "sh\x00-c\x00codex-acp\x00", marker: boolPtr(true)},
		61916: {ppid: 61915, cmdline: "node\x00/root/.npm/_npx/e3854e347c184741/node_modules/.bin/codex-acp\x00", marker: boolPtr(true)},
		61923: {ppid: 61916, cmdline: "/root/.npm/_npx/e3854e347c184741/node_modules/@agentclientprotocol/codex-acp/dist/index.js\x00", marker: boolPtr(true)},
	}
	kw := orphanCleanupKeywords()

	wouldKill := map[int]bool{}
	for pid, info := range procs {
		matched := false
		for k := range kw {
			if strings.Contains(info.cmdline, k) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		root, _, ok := walkToOrphanRoot(procs, pid)
		if !ok {
			continue
		}
		if !hasACPChildMarkerCached(procs, pid) || !hasACPChildMarkerCached(procs, root) {
			continue
		}
		wouldKill[root] = true
	}

	if !wouldKill[61915] {
		t.Fatalf("expected to kill root 61915, got %v", wouldKill)
	}
}
