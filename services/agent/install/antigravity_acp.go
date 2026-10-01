package install

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/json"
	"github.com/fanlv/quartet/pkg/logger"
)

const (
	AntigravityACPRegistryID = executil.AntigravityACPRegistryID
	AntigravityACPProgram    = executil.AntigravityACPProgram

	acpRegistryURL         = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"
	acpRegistryHost        = "cdn.agentclientprotocol.com"
	antigravityArchiveHost = "dl.google.com"
	maxRegistryBody        = 2 << 20
	maxZipUncompressed     = 4 << 30
)

// OfficialAntigravityACPStep downloads the Google ACP server published in the
// ACP Registry and installs a launcher named agy_acp_server.
func OfficialAntigravityACPStep() InstallStep {
	return InstallStep{
		Program: InternalProgramInstallAntigravityACP,
		Display: "install official Antigravity ACP server from the ACP Registry",
	}
}

// AntigravityUninstallPaths are the home-relative files and directories the
// agy uninstall flow removes on platform. Credentials stay in place.
func AntigravityUninstallPaths(platform Platform) []string {
	if platform == PlatformWindows {
		return []string{
			executil.AntigravityACPWindowsRootRel,
			executil.AntigravityACPWindowsCLIRel,
		}
	}
	return []string{
		executil.AntigravityACPUnixRootRel,
		executil.AntigravityACPUnixLauncherRel,
		executil.AntigravityACPUnixCLIRel,
	}
}

type acpRegistryDocument struct {
	Agents []acpRegistryAgent `json:"agents"`
}

type acpRegistryAgent struct {
	ID           string                  `json:"id"`
	Version      string                  `json:"version"`
	Distribution acpRegistryDistribution `json:"distribution"`
}

type acpRegistryDistribution struct {
	Binary map[string]acpRegistryBinary `json:"binary"`
}

type acpRegistryBinary struct {
	Archive string   `json:"archive"`
	Cmd     string   `json:"cmd"`
	Args    []string `json:"args"`
}

func installOfficialAntigravityACP(ctx context.Context, env map[string]string, result *StepResult) error {
	root, err := executil.AntigravityACPRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create Antigravity ACP directory %q failed: %w", root, err)
	}
	release, err := fetchAntigravityACPRelease(ctx, env)
	if err != nil {
		return err
	}
	note(ctx, result, "ACP Registry %s version %s (%s)", release.agent.ID, release.agent.Version, release.platform)

	binaryName := filepath.Base(filepath.FromSlash(release.binary.Cmd))
	if binaryName == "." || binaryName == string(filepath.Separator) || binaryName == "" {
		return fmt.Errorf("ACP Registry command %q has no executable name", release.binary.Cmd)
	}
	if installed, err := antigravityACPInstalled(root, release.agent.Version, binaryName); err != nil {
		return err
	} else if installed {
		binary := filepath.Join(root, filepath.FromSlash(readMarker(root, executil.AntigravityACPLaunchDirFile)), binaryName)
		if err := installAntigravityLauncher(binary, release.binary.Args); err != nil {
			return err
		}
		note(ctx, result, "official Antigravity ACP server %s already installed at %s", release.agent.Version, binary)
		return nil
	}

	archivePath := filepath.Join(root, ".download-"+release.agent.Version+".zip")
	defer os.Remove(archivePath)
	note(ctx, result, "downloading %s", release.binary.Archive)
	if err := downloadFile(ctx, env, release.binary.Archive, archivePath); err != nil {
		return err
	}

	staging := filepath.Join(root, ".staging-"+release.agent.Version)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return fmt.Errorf("create staging directory %q failed: %w", staging, err)
	}
	defer os.RemoveAll(staging)
	if err := extractZip(ctx, archivePath, staging); err != nil {
		return err
	}
	binaryPath, err := findNamedFile(staging, binaryName)
	if err != nil {
		return err
	}
	if err := ensureServerExecutable(binaryPath); err != nil {
		return fmt.Errorf("mark %q executable failed: %w", binaryPath, err)
	}
	relBinary, err := filepath.Rel(staging, binaryPath)
	if err != nil {
		return fmt.Errorf("resolve Antigravity ACP server path inside %q failed: %w", staging, err)
	}
	relBinary = filepath.ToSlash(relBinary)

	finalDir := filepath.Join(root, release.agent.Version)
	backup := finalDir + ".old"
	_ = os.RemoveAll(backup)
	replaced := false
	if _, statErr := os.Stat(finalDir); statErr == nil {
		if err := os.Rename(finalDir, backup); err != nil {
			return fmt.Errorf("move previous Antigravity ACP server %q failed: %w", finalDir, err)
		}
		replaced = true
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat Antigravity ACP server %q failed: %w", finalDir, statErr)
	}
	if err := os.Rename(staging, finalDir); err != nil {
		if replaced {
			_ = os.Rename(backup, finalDir)
		}
		return fmt.Errorf("install Antigravity ACP server into %q failed: %w", finalDir, err)
	}
	_ = os.RemoveAll(backup)

	movedBinary := filepath.Join(finalDir, filepath.FromSlash(relBinary))
	if _, err := os.Stat(movedBinary); err != nil {
		return fmt.Errorf("installed Antigravity ACP server %q is missing: %w", movedBinary, err)
	}
	launchDir := release.agent.Version
	if dir := path.Dir(filepath.ToSlash(relBinary)); dir != "." {
		launchDir = path.Join(release.agent.Version, dir)
	}
	if err := writeMarker(root, executil.AntigravityACPLaunchDirFile, launchDir); err != nil {
		return err
	}
	if err := writeMarker(root, executil.AntigravityACPVersionFile, release.agent.Version); err != nil {
		return err
	}
	if err := installAntigravityLauncher(movedBinary, release.binary.Args); err != nil {
		return err
	}
	removed, err := removeOtherAntigravityVersions(root, release.agent.Version)
	if err != nil {
		return err
	}
	for _, dir := range removed {
		note(ctx, result, "removed previous Antigravity ACP server %s", dir)
	}
	note(ctx, result, "installed official Antigravity ACP server %s at %s", release.agent.Version, movedBinary)
	return nil
}

type antigravityRelease struct {
	agent    acpRegistryAgent
	binary   acpRegistryBinary
	platform string
}

func fetchAntigravityACPRelease(ctx context.Context, env map[string]string) (antigravityRelease, error) {
	platform, err := antigravityRegistryPlatform()
	if err != nil {
		return antigravityRelease{}, err
	}
	body, err := httpGetLimited(ctx, env, acpRegistryURL, acpRegistryHost, maxRegistryBody)
	if err != nil {
		return antigravityRelease{}, fmt.Errorf("fetch ACP Registry failed: %w", err)
	}
	var document acpRegistryDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return antigravityRelease{}, fmt.Errorf("parse ACP Registry failed: %w\nbody:\n%s", err, strings.TrimSpace(string(body)))
	}
	var agent acpRegistryAgent
	found := false
	for _, candidate := range document.Agents {
		if candidate.ID == AntigravityACPRegistryID {
			agent = candidate
			found = true
			break
		}
	}
	if !found {
		return antigravityRelease{}, fmt.Errorf("ACP Registry has no agent %q", AntigravityACPRegistryID)
	}
	if err := validateRegistryVersion(agent.Version); err != nil {
		return antigravityRelease{}, err
	}
	binary, ok := agent.Distribution.Binary[platform]
	if !ok {
		return antigravityRelease{}, fmt.Errorf("ACP Registry agent %q %s has no archive for %s", agent.ID, agent.Version, platform)
	}
	if err := requireDownloadURL(binary.Archive, antigravityArchiveHost); err != nil {
		return antigravityRelease{}, fmt.Errorf("ACP Registry archive for %s: %w", platform, err)
	}
	if strings.TrimSpace(binary.Cmd) == "" {
		return antigravityRelease{}, fmt.Errorf("ACP Registry agent %q does not declare a command for %s", agent.ID, platform)
	}
	return antigravityRelease{agent: agent, binary: binary, platform: platform}, nil
}

func antigravityRegistryPlatform() (string, error) {
	var osName string
	switch CurrentPlatform() {
	case PlatformDarwin:
		osName = "darwin"
	case PlatformLinux:
		osName = "linux"
	case PlatformWindows:
		osName = "windows"
	default:
		return "", fmt.Errorf("unsupported platform %q for the official Antigravity ACP server", CurrentPlatform())
	}
	var archName string
	switch runtime.GOARCH {
	case "amd64":
		archName = "x86_64"
	case "arm64":
		archName = "aarch64"
	default:
		return "", fmt.Errorf("unsupported architecture %q for the official Antigravity ACP server", runtime.GOARCH)
	}
	return osName + "-" + archName, nil
}

func validateRegistryVersion(version string) error {
	version = strings.TrimSpace(version)
	if version == "" || version != path.Clean(version) || strings.Contains(version, "/") || strings.Contains(version, `\`) {
		return fmt.Errorf("ACP Registry version %q cannot be used as a directory name", version)
	}
	return nil
}

func antigravityACPInstalled(root, version, binaryName string) (bool, error) {
	if readMarker(root, executil.AntigravityACPVersionFile) != version {
		return false, nil
	}
	launchDir := readMarker(root, executil.AntigravityACPLaunchDirFile)
	if launchDir == "" {
		return false, nil
	}
	binary := filepath.Join(root, filepath.FromSlash(launchDir), binaryName)
	info, err := os.Stat(binary)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat official Antigravity ACP server %q failed: %w", binary, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("official Antigravity ACP server %q is not a regular file", binary)
	}
	return true, nil
}

func readMarker(root, name string) string {
	body, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func writeMarker(root, name, value string) error {
	if err := writeFileAtomic(filepath.Join(root, name), value+"\n", 0o644); err != nil {
		return fmt.Errorf("write %s failed: %w", name, err)
	}
	return nil
}

func writeFileAtomic(path, content string, mode os.FileMode) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(content), mode); err != nil {
		return err
	}
	if err := replaceFile(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func removeOtherAntigravityVersions(root, keep string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read Antigravity ACP directory %q failed: %w", root, err)
	}
	var removed []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == keep || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		target := filepath.Join(root, entry.Name())
		if err := os.RemoveAll(target); err != nil {
			return removed, fmt.Errorf("remove previous Antigravity ACP server %q failed: %w", target, err)
		}
		removed = append(removed, target)
	}
	return removed, nil
}

func downloadFile(ctx context.Context, env map[string]string, source, destination string) error {
	body, err := openDownload(ctx, env, source)
	if err != nil {
		return err
	}
	defer body.Close()
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create download %q failed: %w", destination, err)
	}
	defer file.Close()
	if err := copyContext(ctx, file, body, 0); err != nil {
		return fmt.Errorf("download %s failed: %w", source, err)
	}
	return nil
}

func openDownload(ctx context.Context, env map[string]string, source string) (io.ReadCloser, error) {
	if err := requireDownloadURL(source, antigravityArchiveHost); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request for %q failed: %w", source, err)
	}
	request.Header.Set("User-Agent", "quartet")
	client := httpClientForEnv(env, allowHostRedirect(antigravityArchiveHost))
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s failed: %w", source, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("download %s failed: HTTP %d: %s", source, response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return response.Body, nil
}

func httpGetLimited(ctx context.Context, env map[string]string, source, host string, limit int64) ([]byte, error) {
	if err := requireDownloadURL(source, host); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %q failed: %w", source, err)
	}
	request.Header.Set("User-Agent", "quartet")
	client := httpClientForEnv(env, allowHostRedirect(host))
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request %s failed: %w", source, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s failed: %w", source, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response from %s exceeds %d bytes", source, limit)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("request %s failed: HTTP %d: %s", source, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func allowHostRedirect(host string) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return requireDownloadURL(request.URL.String(), host)
	}
}

func requireDownloadURL(raw, host string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse URL %q failed: %w", raw, err)
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), host) {
		return fmt.Errorf("refuse URL %q: expected https://%s/...", raw, host)
	}
	return nil
}

func extractZip(ctx context.Context, zipPath, dest string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open archive %q failed: %w", zipPath, err)
	}
	defer reader.Close()
	var written uint64
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		target, err := safeZipPath(dest, file.Name)
		if err != nil {
			return err
		}
		info := file.FileInfo()
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create %q failed: %w", target, err)
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := extractSymlink(dest, target, file); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse zip entry %q with mode %s", file.Name, info.Mode())
		}
		if file.UncompressedSize64 > maxZipUncompressed || written > maxZipUncompressed-file.UncompressedSize64 {
			return fmt.Errorf("archive %q expands past %d bytes", zipPath, maxZipUncompressed)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create %q failed: %w", filepath.Dir(target), err)
		}
		size, err := writeZipFile(ctx, target, file)
		if err != nil {
			return err
		}
		written += size
	}
	return nil
}

func writeZipFile(ctx context.Context, target string, file *zip.File) (uint64, error) {
	source, err := file.Open()
	if err != nil {
		return 0, fmt.Errorf("open zip entry %q failed: %w", file.Name, err)
	}
	defer source.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("create %q failed: %w", target, err)
	}
	defer output.Close()
	written, err := copyCount(ctx, output, source, maxZipUncompressed)
	if err != nil {
		return 0, fmt.Errorf("extract %q failed: %w", file.Name, err)
	}
	mode := file.Mode().Perm()
	if mode&0o111 != 0 {
		if err := os.Chmod(target, mode); err != nil {
			return 0, fmt.Errorf("chmod %q failed: %w", target, err)
		}
	}
	return written, nil
}

func extractSymlink(dest, target string, file *zip.File) error {
	source, err := file.Open()
	if err != nil {
		return fmt.Errorf("open symlink %q failed: %w", file.Name, err)
	}
	defer source.Close()
	body, err := io.ReadAll(io.LimitReader(source, 4096))
	if err != nil {
		return fmt.Errorf("read symlink %q failed: %w", file.Name, err)
	}
	linkTarget := string(body)
	if linkTarget == "" || filepath.IsAbs(linkTarget) {
		return fmt.Errorf("refuse symlink %q -> %q", file.Name, linkTarget)
	}
	resolved := filepath.Join(filepath.Dir(target), linkTarget)
	if !pathWithin(resolved, dest) {
		return fmt.Errorf("refuse symlink %q -> %q outside %s", file.Name, linkTarget, dest)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create %q failed: %w", filepath.Dir(target), err)
	}
	if err := os.Symlink(linkTarget, target); err != nil {
		return fmt.Errorf("create symlink %q failed: %w", target, err)
	}
	return nil
}

func safeZipPath(dest, name string) (string, error) {
	slash := strings.ReplaceAll(name, `\`, "/")
	if slash == "" || path.IsAbs(slash) {
		return "", fmt.Errorf("refuse zip entry %q", name)
	}
	clean := path.Clean(slash)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		if clean == "." && strings.Trim(slash, "/") == "" {
			return dest, nil
		}
		if clean == "." {
			return "", fmt.Errorf("refuse zip entry %q", name)
		}
		return "", fmt.Errorf("refuse zip entry %q", name)
	}
	target := filepath.Join(dest, filepath.FromSlash(clean))
	if !pathWithin(target, dest) {
		return "", fmt.Errorf("refuse zip entry %q outside %s", name, dest)
	}
	return target, nil
}

func findNamedFile(root, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(entry.Name(), name) {
			return nil
		}
		if found != "" {
			return fmt.Errorf("archive contains more than one %s", name)
		}
		found = candidate
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("search archive for %s failed: %w", name, err)
	}
	if found == "" {
		return "", fmt.Errorf("archive does not contain %s", name)
	}
	return found, nil
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader, limit uint64) error {
	_, err := copyCount(ctx, dst, src, limit)
	return err
}

func copyCount(ctx context.Context, dst io.Writer, src io.Reader, limit uint64) (uint64, error) {
	var written uint64
	buffer := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			if limit > 0 && written+uint64(read) > limit {
				return written, fmt.Errorf("write exceeds %d bytes", limit)
			}
			if _, err := dst.Write(buffer[:read]); err != nil {
				return written, err
			}
			written += uint64(read)
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func note(ctx context.Context, result *StepResult, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	result.Stdout += line + "\n"
	logger.Infof(ctx, "[agent.install] %s", line)
}

// OfficialAntigravityACPVersions reads the installed registry marker and the
// current ACP Registry release. A missing marker still returns the registry
// version when that fetch succeeds. Unlike the install flow this probe runs on
// ordinary page loads, so it stays on the backend process environment.
func OfficialAntigravityACPVersions(ctx context.Context) (current, latest string, err error) {
	root, rootErr := executil.AntigravityACPRoot()
	if rootErr != nil {
		return "", "", rootErr
	}
	current = readMarker(root, executil.AntigravityACPVersionFile)
	release, fetchErr := fetchAntigravityACPRelease(ctx, nil)
	if fetchErr != nil {
		if current == "" {
			return "", "", fetchErr
		}
		return current, "", fetchErr
	}
	latest = release.agent.Version
	if current == "" {
		return "", latest, fmt.Errorf("official Antigravity ACP server version marker is missing in %s", root)
	}
	return current, latest, nil
}
