package probe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reproduces the npm ENOTEMPTY shape produced by a failed npx package rename:
// npm error path /root/.npm/_npx/<hash>/node_modules/@agentclientprotocol/codex-acp
// npm error dest /root/.npm/_npx/<hash>/node_modules/@agentclientprotocol/.codex-acp-rAcBvLrs
// npm error errno -39
// npm error ENOTEMPTY: directory not empty, rename '<path>' -> '<dest>'
func TestTryHealNpxENOTEMPTY_RemovesDotfileDest(t *testing.T) {
	tmp := t.TempDir()
	npxRoot := filepath.Join(tmp, "_npx", "e3854e347c184741", "node_modules", "@agentclientprotocol")
	if err := os.MkdirAll(npxRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := filepath.Join(npxRoot, ".codex-acp-rAcBvLrs")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	// A real package directory in the same scope that must NOT be touched.
	real := filepath.Join(npxRoot, "codex-acp")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}

	errText := strings.Join([]string{
		"npm warn exec The following package was not found and will be installed: @agentclientprotocol/codex-acp@1.0.0",
		"npm error code ENOTEMPTY",
		"npm error syscall rename",
		"npm error path " + filepath.Join(npxRoot, "codex-acp"),
		"npm error dest " + stale,
		"npm error errno -39",
		"npm error ENOTEMPTY: directory not empty, rename '" + filepath.Join(npxRoot, "codex-acp") + "' -> '" + stale + "'",
	}, "\n")

	if got := tryHealNpxENOTEMPTY(errText); got != 1 {
		t.Fatalf("tryHealNpxENOTEMPTY removed=%d, want 1", got)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp still present: err=%v", err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("real package directory disappeared! err=%v", err)
	}
}

// A non-ENOTEMPTY error text must not delete anything.
func TestTryHealNpxENOTEMPTY_NoMatch(t *testing.T) {
	if got := tryHealNpxENOTEMPTY("acp initialize failed: connection closed: EOF"); got != 0 {
		t.Fatalf("removed=%d, want 0", got)
	}
}

// Defense in depth: looksLikeNpxStaleTemp must reject paths outside the
// _npx cache or whose final segment is not a dotfile.
func TestLooksLikeNpxStaleTemp(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// True positives — npm holding directories under _npx/...
		{"/root/.npm/_npx/abc/node_modules/@agentclientprotocol/.codex-acp-rAcBvLrs", true},
		{"/home/x/.npm/_npx/h/node_modules/.bin/.something", true},

		// Outside the npx cache — must not match.
		{"/etc/passwd", false},
		{"/", false},
		{"", false},
		{"/root/.npm/_cacache/.tmp/abc", false},

		// Inside _npx but final segment is a real package directory.
		{"/root/.npm/_npx/abc/node_modules/@agentclientprotocol/codex-acp", false},
		{"/root/.npm/_npx/abc/node_modules/some-pkg", false},
	}
	for _, c := range cases {
		if got := looksLikeNpxStaleTemp(c.path); got != c.want {
			t.Fatalf("looksLikeNpxStaleTemp(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
