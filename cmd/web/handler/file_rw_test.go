package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetWorkspaceRootsProvider clears the package-level provider installed by
// other tests / real wiring so each test starts from a known blank slate.
// Tests that exercise workspaceRootsProvider set it explicitly.
func resetWorkspaceRootsProvider(t *testing.T) {
	t.Helper()
	prev, _ := workspaceRootsProvider.Load().(workspaceRootsProviderFn)
	SetWorkspaceRootsProvider(nil)
	t.Cleanup(func() { SetWorkspaceRootsProvider(prev) })
}

func TestHasPathPrefix(t *testing.T) {
	root := t.TempDir()

	innerDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(innerDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	innerFile := filepath.Join(innerDir, "file.txt")
	if err := os.WriteFile(innerFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("no"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Symlink inside root pointing at a file outside root. hasPathPrefix must
	// reject this even though the textual path starts with root, because the
	// real target escapes the allowed region.
	escapeLink := filepath.Join(root, "escape")
	if err := os.Symlink(outsideFile, escapeLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Symlink inside root pointing at a sibling inside root — must be accepted
	// so ordinary convenience symlinks keep working.
	insideLink := filepath.Join(root, "alias")
	if err := os.Symlink(innerDir, insideLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tests := []struct {
		name     string
		filePath string
		root     string
		want     bool
	}{
		{"path equals root", root, root, true},
		{"file directly under root", innerFile, root, true},
		{"nested dir under root", innerDir, root, true},
		{"non-existent leaf under root", filepath.Join(innerDir, "new.txt"), root, true},
		{"path outside root", outsideFile, root, false},
		{"sibling with shared prefix rejected", root + "-sibling/f", root, false},
		{"symlink inside root resolving outside rejected", escapeLink, root, false},
		{"symlink inside root resolving inside accepted", filepath.Join(insideLink, "file.txt"), root, true},
		{"non-existent file under escape-link rejected", filepath.Join(escapeLink, "child"), root, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hasPathPrefix(tc.filePath, tc.root)
			if got != tc.want {
				t.Fatalf("hasPathPrefix(%q, %q) = %v, want %v", tc.filePath, tc.root, got, tc.want)
			}
		})
	}
}

func TestIsPathInAllowedRegion(t *testing.T) {
	resetWorkspaceRootsProvider(t)

	memoryRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(memoryRoot, "uploads"), 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	wsRoot := t.TempDir()
	stranger := t.TempDir()
	homeRoot := t.TempDir()

	t.Setenv("LOCAL_MEMORY", memoryRoot)
	t.Setenv("HOME", homeRoot)

	SetWorkspaceRootsProvider(func() []string { return []string{wsRoot} })

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"empty rejected", "", false},
		{"under LOCAL_MEMORY accepted", filepath.Join(memoryRoot, "notes.md"), true},
		{"under uploads accepted", filepath.Join(memoryRoot, "uploads", "a.bin"), true},
		{"under workspace root accepted", filepath.Join(wsRoot, "src", "main.go"), true},
		{"under HOME accepted", filepath.Join(homeRoot, "code", "main.go"), true},
		{"outside every root rejected", filepath.Join(stranger, "secret"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isPathInAllowedRegion(tc.path)
			if got != tc.want {
				t.Fatalf("isPathInAllowedRegion(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestServeFileContentType(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		wantInline bool
		wantPrefix string // Content-Type must start with this when wantInline
	}{
		{"png is inline", "/a/b.png", true, "image/png"},
		{"jpg is inline", "/a/b.jpg", true, "image/jpeg"},
		{"webp is inline", "/a/b.webp", true, "image/webp"},
		{"pdf is inline", "/a/b.pdf", true, "application/pdf"},
		{"mp4 is inline", "/a/b.mp4", true, "video/mp4"},

		{"svg is blocked (can embed script)", "/a/b.svg", false, ""},
		{"html is blocked", "/a/b.html", false, ""},
		{"htm is blocked", "/a/b.htm", false, ""},
		{"xhtml is blocked", "/a/b.xhtml", false, ""},
		{"xml is blocked", "/a/b.xml", false, ""},
		{"js is blocked", "/a/b.js", false, ""},
		{"unknown extension falls back to octet-stream", "/a/b.weirdext", false, ""},
		{"no extension falls back to octet-stream", "/a/noext", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct, inline := serveFileContentType(tc.path)
			if inline != tc.wantInline {
				t.Fatalf("inline=%v want %v (ct=%q)", inline, tc.wantInline, ct)
			}
			if !inline {
				if ct != "application/octet-stream" {
					t.Fatalf("non-inline must be application/octet-stream, got %q", ct)
				}
				return
			}
			if tc.wantPrefix != "" && !strings.HasPrefix(ct, tc.wantPrefix) {
				t.Fatalf("content-type %q does not start with %q", ct, tc.wantPrefix)
			}
		})
	}
}

func TestSanitizeAttachmentFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "report.pdf", "report.pdf"},
		{"empty falls back", "", "download"},
		{"strip CR/LF to prevent header injection", "a\r\nX-Injected: 1\r\n.txt", "aX-Injected: 1.txt"},
		{"strip quote and backslash", `a"b\c.txt`, "abc.txt"},
		{"drop non-ASCII from ASCII-only fallback", "日本語.txt", ".txt"},
		{"all-bad falls back", "\r\n\"\\", "download"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeAttachmentFilename(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeAttachmentFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildAttachmentDisposition(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "ascii pass-through without filename*",
			in:   "report.pdf",
			want: `attachment; filename="report.pdf"`,
		},
		{
			name: "empty falls back to download",
			in:   "",
			want: `attachment; filename="download"`,
		},
		{
			name: "non-ASCII preserved via RFC 5987",
			in:   "测试.txt",
			want: `attachment; filename=".txt"; filename*=UTF-8''%E6%B5%8B%E8%AF%95.txt`,
		},
		{
			name: "japanese preserved via RFC 5987",
			in:   "日本語.txt",
			want: `attachment; filename=".txt"; filename*=UTF-8''%E6%97%A5%E6%9C%AC%E8%AA%9E.txt`,
		},
		{
			name: "header injection still blocked in fallback, encoded in filename*",
			in:   "a\r\nX: 1.txt",
			want: `attachment; filename="aX: 1.txt"; filename*=UTF-8''a%0D%0AX%3A%201.txt`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildAttachmentDisposition(tc.in)
			if got != tc.want {
				t.Fatalf("buildAttachmentDisposition(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsPathInAllowedRegionProviderRefreshes(t *testing.T) {
	resetWorkspaceRootsProvider(t)

	t.Setenv("LOCAL_MEMORY", "")
	t.Setenv("HOME", t.TempDir())

	wsA := t.TempDir()
	wsB := t.TempDir()
	fileA := filepath.Join(wsA, "a.txt")
	fileB := filepath.Join(wsB, "b.txt")

	roots := []string{wsA}
	SetWorkspaceRootsProvider(func() []string { return roots })

	if !isPathInAllowedRegion(fileA) {
		t.Fatalf("fileA should be allowed when wsA is registered")
	}
	if isPathInAllowedRegion(fileB) {
		t.Fatalf("fileB must not be allowed before wsB is registered")
	}

	roots = []string{wsA, wsB}
	if !isPathInAllowedRegion(fileB) {
		t.Fatalf("fileB should be allowed after workspace provider returns wsB")
	}
}
