package wechat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"
)

func TestIsSupportedAttachmentExt(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"png ok", "/tmp/a.png", true},
		{"PNG uppercase ok", "a.PNG", true},
		{"pdf ok", "report.pdf", true},
		{"mp4 ok", "v.mp4", true},
		{"exe rejected", "malware.exe", false},
		{"no ext rejected", "README", false},
		{"url with query", "https://x/a.jpg?t=1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSupportedAttachmentExt(tc.in); got != tc.want {
				t.Fatalf("got %v want %v for %q", got, tc.want, tc.in)
			}
		})
	}
}

func TestCheckAttachmentSize(t *testing.T) {
	cases := []struct {
		name    string
		typ     int
		size    int
		wantErr bool
	}{
		{"image under limit", ilink.ItemTypeImage, 1024, false},
		{"image at limit", ilink.ItemTypeImage, maxImageBytes, false},
		{"image over limit", ilink.ItemTypeImage, maxImageBytes + 1, true},
		{"video over limit", ilink.ItemTypeVideo, maxVideoBytes + 1, true},
		{"file over limit", ilink.ItemTypeFile, maxFileBytes + 1, true},
		{"file under limit", ilink.ItemTypeFile, 500, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkAttachmentSize(tc.typ, tc.size)
			if (err != nil) != tc.wantErr {
				t.Fatalf("got err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// TestSendMediaFromPath_UnsupportedExtension: bad extension short-circuits
// before any CDN call or file read.
func TestSendMediaFromPath_UnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cmd.exe")
	sb := fileserver.GetFileManager()
	if err := sb.FileWrite(&fsmodel.FileWriteRequest{File: path, Content: "x", Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	err := sendMediaFromPath(t.Context(), nil, "@u", path, "")
	if err == nil {
		t.Fatal("expected error for .exe")
	}
}

// TestSendMediaFromPath_NonexistentFile: stat failure surfaced before read.
func TestSendMediaFromPath_NonexistentFile(t *testing.T) {
	err := sendMediaFromPath(t.Context(), nil, "@u", "/nonexistent/path/x.png", "")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestRejectNonPublicAddress: the SSRF guard on externalHTTPClient must block
// dials to loopback, private RFC1918, link-local, multicast, and unspecified
// addresses, while allowing regular public IPs through. 169.254.169.254 is
// the cloud-metadata IP that SSRF attacks most commonly target.
func TestRejectNonPublicAddress(t *testing.T) {
	cases := []struct {
		name    string
		address string
		wantErr bool
	}{
		{"public IPv4", "8.8.8.8:443", false},
		{"public IPv6", "[2001:4860:4860::8888]:443", false},
		{"loopback v4", "127.0.0.1:80", true},
		{"loopback v6", "[::1]:80", true},
		{"private 10.x", "10.0.0.1:22", true},
		{"private 192.168", "192.168.1.1:80", true},
		{"private 172.16-31", "172.16.0.1:80", true},
		{"AWS IMDS (link-local)", "169.254.169.254:80", true},
		{"link-local v6", "[fe80::1]:80", true},
		{"multicast", "224.0.0.1:80", true},
		{"unspecified", "0.0.0.0:80", true},
		{"malformed", "not-a-host", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectNonPublicAddress("tcp", tc.address, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("address=%q: got err=%v wantErr=%v", tc.address, err, tc.wantErr)
			}
		})
	}
}

// TestDownloadFile_SSRFBlocksLoopback: the dial-level guard refuses to reach
// a local httptest.Server when rejectNonPublicAddress is active — confirms
// the guard is actually wired into externalHTTPClient's Transport (not just
// defined). The handler must never be called.
func TestDownloadFile_SSRFBlocksLoopback(t *testing.T) {
	var reached bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer server.Close()

	_, _, err := downloadFile(context.Background(), server.URL+"/x.png")
	if err == nil {
		t.Fatal("expected SSRF guard to block dial to loopback httptest.Server")
	}
	if reached {
		t.Fatal("SSRF guard did not prevent the handler from running")
	}
}

// TestDownloadFile_RejectsNonHTTPScheme: a markdown ![](file:///etc/passwd)
// in LLM output must not make downloadFile read local files, regardless of
// the dial-time guard.
func TestDownloadFile_RejectsNonHTTPScheme(t *testing.T) {
	for _, badURL := range []string{
		"file:///etc/passwd",
		"ftp://example.com/x.png",
		"gopher://example.com/",
	} {
		if _, _, err := downloadFile(context.Background(), badURL); err == nil {
			t.Fatalf("expected error for %q", badURL)
		}
	}
}

// TestDownloadFile_HappyPathWithGuardOverride: the non-SSRF code path works.
// With the dial guard swapped for a no-op, downloadFile returns the body and
// a Content-Type header — this is the path PR#5 will exercise once the
// helper is wired into Replier.ReplyImage.
func TestDownloadFile_HappyPathWithGuardOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake-png-data"))
	}))
	defer server.Close()

	original := rejectNonPublicAddress
	rejectNonPublicAddress = func(string, string, syscall.RawConn) error { return nil }
	t.Cleanup(func() { rejectNonPublicAddress = original })

	data, ct, err := downloadFile(context.Background(), server.URL+"/x.png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "fake-png-data" {
		t.Fatalf("body mismatch: got %q", data)
	}
	if !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("content-type mismatch: got %q", ct)
	}
}
