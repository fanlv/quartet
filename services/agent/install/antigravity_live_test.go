package install

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/json"
)

// TestLiveOfficialAgyAndAntigravityACP downloads the official agy CLI release
// and the ACP Registry server, then checks that each one actually starts.
// The downloads are large, so the test stays out of the default suite.
func TestLiveOfficialAgyAndAntigravityACP(t *testing.T) {
	if os.Getenv("QUARTET_ANTIGRAVITY_LIVE") != "1" {
		t.Skip("set QUARTET_ANTIGRAVITY_LIVE=1 to download and run the official agy CLI and Antigravity ACP server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	t.Run("agy", func(t *testing.T) {
		version, binary := installOfficialAgyCLI(ctx, t)
		output := runAgyVersion(ctx, t, binary)
		if !strings.Contains(output, version) {
			t.Fatalf("agy --version = %q, release tag %s", output, version)
		}
		t.Logf("agy %s: %s", version, output)
	})

	t.Run("antigravity-acp", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		var result StepResult
		if err := installOfficialAntigravityACP(ctx, nil, &result); err != nil {
			t.Fatalf("install official Antigravity ACP server failed: %v\n%s", err, result.Stdout)
		}
		t.Log(result.Stdout)
		launcher := filepath.Join(home, filepath.FromSlash(executil.AntigravityACPUnixLauncherRel))
		if runtime.GOOS == "windows" {
			launcher = filepath.Join(home, filepath.FromSlash(executil.AntigravityACPWindowsRootRel))
		}
		response := initializeAntigravityACP(ctx, t, launcher)
		t.Logf("initialize response: %s", response)
	})
}

func installOfficialAgyCLI(ctx context.Context, t *testing.T) (version, binary string) {
	t.Helper()
	tag, err := latestAgyReleaseTag(ctx)
	if err != nil {
		t.Fatalf("resolve official agy release failed: %v", err)
	}
	version = strings.TrimPrefix(tag, "v")
	asset, err := agyReleaseAsset()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), asset)
	source := "https://github.com/google-antigravity/antigravity-cli/releases/download/" + tag + "/" + asset
	t.Logf("downloading %s", source)
	if err := downloadToFile(ctx, source, archive); err != nil {
		t.Fatalf("download official agy CLI failed: %v", err)
	}
	dest := t.TempDir()
	if err := extractArchive(archive, dest); err != nil {
		t.Fatalf("extract official agy CLI failed: %v", err)
	}
	binary, err = findCLIBinary(dest)
	if err != nil {
		t.Fatal(err)
	}
	return version, binary
}

func findCLIBinary(root string) (string, error) {
	for _, name := range []string{"agy", "antigravity"} {
		binary, err := findExecutableNamed(root, name)
		if err == nil {
			return binary, nil
		}
	}
	return "", fmt.Errorf("agy CLI binary not found under %s", root)
}

func latestAgyReleaseTag(ctx context.Context) (string, error) {
	client := &http.Client{}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/google-antigravity/antigravity-cli/releases/latest", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "quartet")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d from %s", response.StatusCode, response.Request.URL)
	}
	tag := filepath.Base(response.Request.URL.Path)
	if tag == "" || tag == "latest" || tag == "." {
		return "", fmt.Errorf("latest agy release URL %s has no tag", response.Request.URL)
	}
	return tag, nil
}

func agyReleaseAsset() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "agy_cli_linux_x64.tar.gz", nil
	case "linux/arm64":
		return "agy_cli_linux_arm64.tar.gz", nil
	case "darwin/amd64":
		return "agy_cli_mac_x64.tar.gz", nil
	case "darwin/arm64":
		return "agy_cli_mac_arm64.tar.gz", nil
	case "windows/amd64":
		return "agy_cli_windows_x64.zip", nil
	case "windows/arm64":
		return "agy_cli_windows_arm64.zip", nil
	default:
		return "", fmt.Errorf("no official agy archive name for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func runAgyVersion(ctx context.Context, t *testing.T, binary string) string {
	t.Helper()
	commandCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, binary, "--version")
	cmd.Env = append(os.Environ(), "AGY_CLI_DISABLE_AUTO_UPDATE=true")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("agy --version failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	output := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	if !regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`).MatchString(output) {
		t.Fatalf("agy --version returned no semantic version\n%s", output)
	}
	return output
}

func initializeAntigravityACP(ctx context.Context, t *testing.T, launcher string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		binary, err := findExecutableNamed(launcher, "agy_acp_server.exe")
		if err != nil {
			t.Fatal(err)
		}
		launcher = binary
	}
	info, err := os.Stat(launcher)
	if err != nil {
		t.Fatalf("stat ACP launcher %s failed: %v", launcher, err)
	}
	if info.IsDir() {
		t.Fatalf("ACP launcher %s is a directory", launcher)
	}
	commandCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, launcher)
	prepareIsolatedCommand(cmd)
	cmd.Dir = t.TempDir()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start official Antigravity ACP server failed: %v", err)
	}
	defer stopCommand(cmd)
	request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientInfo":{"name":"quartet-live-test","version":"0"},"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}}}` + "\n"
	if _, err := io.WriteString(stdin, request); err != nil {
		t.Fatalf("write initialize failed: %v\nstderr:\n%s", err, stderr.String())
	}
	_ = stdin.Close()
	type read struct {
		line string
		err  error
	}
	incoming := make(chan read, 8)
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			incoming <- read{line: line, err: err}
			if err != nil {
				return
			}
		}
	}()
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	for {
		var item read
		select {
		case <-commandCtx.Done():
			t.Fatalf("ACP server exited before initialize: %v\nstderr:\n%s", commandCtx.Err(), stderr.String())
		case <-deadline.C:
			t.Fatalf("official Antigravity ACP server did not answer initialize\nstderr:\n%s", stderr.String())
		case item = <-incoming:
		}
		if item.err != nil && strings.TrimSpace(item.line) == "" {
			t.Fatalf("read initialize response failed: %v\nstderr:\n%s", item.err, stderr.String())
		}
		line := strings.TrimSpace(item.line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var message struct {
			Error  *jsonRPCError `json:"error"`
			Result struct {
				ProtocolVersion int `json:"protocolVersion"`
				AgentInfo       struct {
					Name    string `json:"name"`
					Title   string `json:"title"`
					Version string `json:"version"`
				} `json:"agentInfo"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			continue
		}
		if message.Error != nil {
			t.Fatalf("initialize failed: %s\nstderr:\n%s", message.Error, stderr.String())
		}
		if message.Result.ProtocolVersion < 1 {
			t.Fatalf("initialize protocolVersion = %d\n%s", message.Result.ProtocolVersion, line)
		}
		info := message.Result.AgentInfo
		if !strings.Contains(strings.ToLower(info.Name+" "+info.Title), "antigravity") {
			t.Fatalf("initialize agentInfo = %+v\n%s", info, line)
		}
		if info.Version == "" {
			t.Fatalf("initialize agentInfo has no version\n%s", line)
		}
		return line
	}
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonRPCError) String() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("code=%d message=%s", e.Code, e.Message)
}

func downloadToFile(ctx context.Context, source, destination string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "quartet")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("HTTP %d from %s: %s", response.StatusCode, source, strings.TrimSpace(string(snippet)))
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := copyCount(ctx, file, response.Body, 2<<30); err != nil {
		return err
	}
	return nil
}

func extractArchive(archive, dest string) error {
	switch {
	case strings.HasSuffix(archive, ".tar.gz"):
		return extractTarGz(archive, dest)
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(context.Background(), archive, dest)
	default:
		return fmt.Errorf("unsupported archive %s", archive)
	}
}

func extractTarGz(archive, dest string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeZipPath(dest, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(output, tarReader); err != nil {
				output.Close()
				return err
			}
			if err := output.Close(); err != nil {
				return err
			}
		}
	}
}

func findExecutableNamed(root, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != name {
			return nil
		}
		found = candidate
		return filepath.SkipAll
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s not found under %s", name, root)
	}
	return found, nil
}
