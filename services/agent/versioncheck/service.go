// Package versioncheck inspects installed Agent components without mutating
// them. NPM-backed components are compared with the registry in one batched
// check; script/manual/project agents fall back to their local binary version.
package versioncheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/services/agent/catalog"
	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/types/model"
)

const (
	cacheTTL          = 5 * time.Minute
	npmCommandTimeout = 45 * time.Second
	binaryTimeout     = 10 * time.Second
	binaryConcurrency = 4
)

var versionPattern = regexp.MustCompile(`\bv?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)\b`)

type Service struct {
	catalog *catalog.Service

	mu        sync.Mutex
	cached    []model.AgentVersionInfo
	checkedAt time.Time
}

func NewService(agentCatalog *catalog.Service) *Service {
	return &Service{catalog: agentCatalog}
}

// Invalidate drops the process-local result cache after an install, upgrade
// or uninstall changes the set of Agent components.
func (s *Service) Invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.checkedAt = time.Time{}
	s.mu.Unlock()
}

// Check returns installed Agent version information. The mutex deliberately
// spans the external probes: duplicate page loads share one bounded check
// rather than spawning competing npm registry requests.
func (s *Service) Check(ctx context.Context, force bool) ([]model.AgentVersionInfo, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !force && len(s.cached) > 0 && time.Since(s.checkedAt) < cacheTTL {
		return cloneInfos(s.cached), s.checkedAt.UnixMilli(), nil
	}

	entries, err := s.catalog.List(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("load Agent catalog for version check failed: %w", err)
	}

	infos := make([]model.AgentVersionInfo, 0, len(entries))
	npmRefs := make(map[string][]componentRef)
	publishedVersionRefs := make(map[string][]componentRef)
	publishedURLRefs := make(map[string][]componentRef)
	binaryTasks := make([]binaryTask, 0)
	checker := agentinstall.Checker{}

	for _, entry := range entries {
		var (
			agentID          string
			definition       model.AgentRuntimeDefinition
			packages         []string
			probeBinary      bool
			versionPackage   string
			versionURL       string
			upgradeSupported bool
		)
		switch entry.Source {
		case model.AgentCatalogSourceBuiltin:
			if entry.Builtin == nil || entry.Builtin.Deprecated {
				continue
			}
			agentID = entry.Builtin.AgentID
			definition = entry.Builtin.RuntimeDefinition()
			versionPackage = strings.TrimSpace(entry.Builtin.Install.VersionPackage)
			versionURL = strings.TrimSpace(entry.Builtin.Install.VersionURL)
			if versionPackage == "" {
				packages = entry.Builtin.Install.NPMPackages()
			}
			probeBinary = versionPackage != "" || versionURL != "" || len(packages) == 0 || entry.Builtin.Install.HasNonNPMSteps()
			upgradeSupported = entry.Builtin.Install.AutoUpgradeable()
		case model.AgentCatalogSourceCustom:
			if entry.Custom == nil || entry.Custom.Lifecycle != model.AgentLifecycleActive {
				continue
			}
			agentID = entry.Custom.AgentID
			definition = currentCustomDefinition(*entry.Custom)
			probeBinary = true
		default:
			continue
		}

		installed := checker.Check(agentinstall.Definition{
			Bin:        definition.Bin,
			ACPProgram: definition.ACPProgram,
		})
		if !installed.Installed {
			continue
		}

		infoIndex := len(infos)
		infos = append(infos, model.AgentVersionInfo{
			AgentID:          agentID,
			UpgradeSupported: upgradeSupported,
		})
		for _, pkg := range packages {
			componentIndex := len(infos[infoIndex].Components)
			infos[infoIndex].Components = append(infos[infoIndex].Components, model.AgentVersionComponent{
				Name: pkg,
				Kind: "npm",
			})
			npmRefs[pkg] = append(npmRefs[pkg], componentRef{infoIndex: infoIndex, componentIndex: componentIndex})
		}
		if probeBinary {
			componentIndex := len(infos[infoIndex].Components)
			infos[infoIndex].Components = append(infos[infoIndex].Components, model.AgentVersionComponent{
				Name: definition.Bin,
				Kind: "binary",
			})
			binaryTasks = append(binaryTasks, binaryTask{
				infoIndex: infoIndex, componentIndex: componentIndex, binary: definition.Bin,
			})
			if versionPackage != "" {
				publishedVersionRefs[versionPackage] = append(
					publishedVersionRefs[versionPackage],
					componentRef{infoIndex: infoIndex, componentIndex: componentIndex},
				)
			}
			if versionURL != "" {
				publishedURLRefs[versionURL] = append(
					publishedURLRefs[versionURL],
					componentRef{infoIndex: infoIndex, componentIndex: componentIndex},
				)
			}
		}
	}

	if len(npmRefs) > 0 {
		snapshot, npmErr := inspectNPM(ctx)
		for pkg, refs := range npmRefs {
			component := model.AgentVersionComponent{Name: pkg, Kind: "npm"}
			if npmErr != nil {
				component.Error = npmErr.Error()
			} else {
				component = snapshot.component(ctx, pkg)
			}
			for _, ref := range refs {
				infos[ref.infoIndex].Components[ref.componentIndex] = component
			}
		}
	}

	applyBinaryVersions(ctx, infos, binaryTasks)
	for pkg, refs := range publishedVersionRefs {
		latest, latestErr := inspectNPMLatest(ctx, pkg)
		for _, ref := range refs {
			component := &infos[ref.infoIndex].Components[ref.componentIndex]
			if latestErr != nil {
				component.Error = latestErr.Error()
				continue
			}
			component.LatestVersion = latest
			component.UpdateAvailable = semanticVersionLess(component.CurrentVersion, latest)
		}
	}
	for versionURL, refs := range publishedURLRefs {
		latest, latestErr := inspectLatestVersionURL(ctx, versionURL)
		for _, ref := range refs {
			component := &infos[ref.infoIndex].Components[ref.componentIndex]
			if latestErr != nil {
				component.Error = latestErr.Error()
				continue
			}
			component.LatestVersion = latest
			component.UpdateAvailable = semanticVersionLess(component.CurrentVersion, latest)
		}
	}
	for infoIndex := range infos {
		for _, component := range infos[infoIndex].Components {
			if component.UpdateAvailable {
				infos[infoIndex].UpdateAvailable = true
				break
			}
		}
	}

	s.cached = cloneInfos(infos)
	s.checkedAt = time.Now()
	return cloneInfos(infos), s.checkedAt.UnixMilli(), nil
}

type componentRef struct {
	infoIndex      int
	componentIndex int
}

type binaryTask struct {
	infoIndex      int
	componentIndex int
	binary         string
}

type binaryResult struct {
	task      binaryTask
	component model.AgentVersionComponent
}

func applyBinaryVersions(ctx context.Context, infos []model.AgentVersionInfo, tasks []binaryTask) {
	if len(tasks) == 0 {
		return
	}
	sem := make(chan struct{}, binaryConcurrency)
	results := make(chan binaryResult, len(tasks))
	var wg sync.WaitGroup
	for _, task := range tasks {
		task := task
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results <- binaryResult{task: task, component: model.AgentVersionComponent{
					Name: task.binary, Kind: "binary", Error: fmt.Sprintf("binary version check canceled: %v", ctx.Err()),
				}}
				return
			}
			version, err := inspectBinary(ctx, task.binary)
			component := model.AgentVersionComponent{Name: task.binary, Kind: "binary", CurrentVersion: version}
			if err != nil {
				component.Error = err.Error()
			}
			results <- binaryResult{task: task, component: component}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	for result := range results {
		infos[result.task.infoIndex].Components[result.task.componentIndex] = result.component
	}
}

func inspectBinary(ctx context.Context, binary string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, binaryTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(probeCtx, binary, "--version")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", commandError([]string{binary, "--version"}, err, stdout.String(), stderr.String())
	}
	match := versionPattern.FindStringSubmatch(stdout.String() + "\n" + stderr.String())
	if len(match) < 2 {
		return "", fmt.Errorf("%s --version returned no semantic version\nstdout:\n%s\nstderr:\n%s", binary, stdout.String(), stderr.String())
	}
	return match[1], nil
}

type npmListResponse struct {
	Dependencies map[string]struct {
		Version string `json:"version"`
	} `json:"dependencies"`
}

type npmOutdatedEntry struct {
	Current string `json:"current"`
	Wanted  string `json:"wanted"`
	Latest  string `json:"latest"`
}

type npmSnapshot struct {
	current  map[string]string
	outdated map[string]npmOutdatedEntry
}

func inspectNPM(ctx context.Context) (*npmSnapshot, error) {
	listStdout, listStderr, listExit, listErr := runCommand(ctx, npmCommandTimeout, "npm", "ls", "-g", "--depth=0", "--json")
	if listErr != nil && listExit != 1 {
		return nil, commandError([]string{"npm", "ls", "-g", "--depth=0", "--json"}, listErr, listStdout, listStderr)
	}
	var listed npmListResponse
	if err := json.Unmarshal([]byte(listStdout), &listed); err != nil {
		return nil, fmt.Errorf("parse npm global package list failed: %v\nstdout:\n%s\nstderr:\n%s", err, listStdout, listStderr)
	}

	outdatedStdout, outdatedStderr, outdatedExit, outdatedErr := runCommand(ctx, npmCommandTimeout, "npm", "outdated", "-g", "--depth=0", "--json")
	if outdatedErr != nil && outdatedExit != 1 {
		return nil, commandError([]string{"npm", "outdated", "-g", "--depth=0", "--json"}, outdatedErr, outdatedStdout, outdatedStderr)
	}
	var rawOutdated map[string]json.RawMessage
	if err := json.Unmarshal([]byte(outdatedStdout), &rawOutdated); err != nil {
		return nil, fmt.Errorf("parse npm outdated result failed: %v\nstdout:\n%s\nstderr:\n%s", err, outdatedStdout, outdatedStderr)
	}

	snapshot := &npmSnapshot{
		current:  make(map[string]string, len(listed.Dependencies)),
		outdated: make(map[string]npmOutdatedEntry, len(rawOutdated)),
	}
	for name, dependency := range listed.Dependencies {
		snapshot.current[name] = dependency.Version
	}
	for name, raw := range rawOutdated {
		if entry, ok := decodeOutdatedEntry(raw); ok {
			snapshot.outdated[name] = entry
		}
	}
	return snapshot, nil
}

func decodeOutdatedEntry(raw json.RawMessage) (npmOutdatedEntry, bool) {
	var entry npmOutdatedEntry
	if err := json.Unmarshal(raw, &entry); err == nil && (entry.Current != "" || entry.Latest != "") {
		return entry, true
	}
	var entries []npmOutdatedEntry
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 {
		return npmOutdatedEntry{}, false
	}
	for _, candidate := range entries {
		if candidate.Current != "" && candidate.Latest != "" {
			return candidate, true
		}
	}
	return entries[0], true
}

func (s *npmSnapshot) component(ctx context.Context, pkg string) model.AgentVersionComponent {
	component := model.AgentVersionComponent{Name: pkg, Kind: "npm", CurrentVersion: s.current[pkg]}
	if outdated, ok := s.outdated[pkg]; ok {
		if component.CurrentVersion == "" {
			component.CurrentVersion = outdated.Current
		}
		component.LatestVersion = outdated.Latest
	} else if component.CurrentVersion != "" {
		component.LatestVersion = component.CurrentVersion
	}
	if component.CurrentVersion == "" {
		latest, err := inspectNPMLatest(ctx, pkg)
		if err != nil {
			component.Error = err.Error()
			return component
		}
		component.LatestVersion = latest
		component.UpdateAvailable = latest != ""
		component.Error = fmt.Sprintf("npm package %q is not installed globally, although the Agent executables are available", pkg)
		return component
	}
	component.UpdateAvailable = semanticVersionLess(component.CurrentVersion, component.LatestVersion)
	return component
}

func inspectNPMLatest(ctx context.Context, pkg string) (string, error) {
	stdout, stderr, exitCode, err := runCommand(ctx, npmCommandTimeout, "npm", "view", pkg, "version", "--json")
	if err != nil || exitCode != 0 {
		return "", commandError([]string{"npm", "view", pkg, "version", "--json"}, err, stdout, stderr)
	}
	var version string
	if err := json.Unmarshal([]byte(stdout), &version); err != nil {
		return "", fmt.Errorf("parse latest npm version for %q failed: %v\nstdout:\n%s\nstderr:\n%s", pkg, err, stdout, stderr)
	}
	return version, nil
}

func inspectLatestVersionURL(ctx context.Context, versionURL string) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, binaryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, versionURL, nil)
	if err != nil {
		return "", fmt.Errorf("build latest-version request for %q failed: %w", versionURL, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch latest version from %q failed: %w", versionURL, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return "", fmt.Errorf("read latest version from %q failed: %w", versionURL, readErr)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf(
			"fetch latest version from %q failed: HTTP %d: %s",
			versionURL,
			resp.StatusCode,
			strings.TrimSpace(string(body)),
		)
	}
	version := strings.TrimSpace(string(body))
	if _, ok := parseSemanticVersion(version); !ok {
		return "", fmt.Errorf("latest version endpoint %q returned invalid semver %q", versionURL, version)
	}
	return version, nil
}

func runCommand(ctx context.Context, timeout time.Duration, program string, args ...string) (string, string, int, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(commandCtx, program, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	return stdout.String(), stderr.String(), exitCode, err
}

func commandError(command []string, runErr error, stdout, stderr string) error {
	return fmt.Errorf("run %q failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(command, " "), runErr, stdout, stderr)
}

type parsedVersion struct {
	core       [3]int64
	prerelease []string
}

func semanticVersionLess(current, latest string) bool {
	left, leftOK := parseSemanticVersion(current)
	right, rightOK := parseSemanticVersion(latest)
	if !leftOK || !rightOK {
		return false
	}
	for index := range left.core {
		if left.core[index] != right.core[index] {
			return left.core[index] < right.core[index]
		}
	}
	if len(left.prerelease) == 0 || len(right.prerelease) == 0 {
		return len(left.prerelease) > 0 && len(right.prerelease) == 0
	}
	limit := len(left.prerelease)
	if len(right.prerelease) < limit {
		limit = len(right.prerelease)
	}
	for index := 0; index < limit; index++ {
		if left.prerelease[index] == right.prerelease[index] {
			continue
		}
		leftNumber, leftErr := strconv.ParseInt(left.prerelease[index], 10, 64)
		rightNumber, rightErr := strconv.ParseInt(right.prerelease[index], 10, 64)
		switch {
		case leftErr == nil && rightErr == nil:
			return leftNumber < rightNumber
		case leftErr == nil:
			return true
		case rightErr == nil:
			return false
		default:
			return left.prerelease[index] < right.prerelease[index]
		}
	}
	return len(left.prerelease) < len(right.prerelease)
}

func parseSemanticVersion(value string) (parsedVersion, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	value = strings.SplitN(value, "+", 2)[0]
	parts := strings.SplitN(value, "-", 2)
	coreParts := strings.Split(parts[0], ".")
	if len(coreParts) != 3 {
		return parsedVersion{}, false
	}
	var parsed parsedVersion
	for index, part := range coreParts {
		number, err := strconv.ParseInt(part, 10, 64)
		if err != nil || number < 0 {
			return parsedVersion{}, false
		}
		parsed.core[index] = number
	}
	if len(parts) == 2 && parts[1] != "" {
		parsed.prerelease = strings.Split(parts[1], ".")
	}
	return parsed, true
}

func currentCustomDefinition(agent model.CustomAgent) model.AgentRuntimeDefinition {
	for _, revision := range agent.Revisions {
		if revision.Revision == agent.CurrentRevision {
			return revision.Definition
		}
	}
	return model.AgentRuntimeDefinition{}
}

func cloneInfos(in []model.AgentVersionInfo) []model.AgentVersionInfo {
	out := make([]model.AgentVersionInfo, len(in))
	copy(out, in)
	for index := range out {
		out[index].Components = append([]model.AgentVersionComponent(nil), in[index].Components...)
	}
	return out
}
