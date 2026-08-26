package install

import (
	"fmt"
	"runtime"
	"strings"
)

// InstallMethod classifies how a built-in agent's CLI is installed.
type InstallMethod string

const (
	// InstallMethodNPM installs via one or more npm install -g steps.
	InstallMethodNPM InstallMethod = "npm"
	// InstallMethodScript installs via the publisher's platform installer.
	InstallMethodScript InstallMethod = "script"
	// InstallMethodProject installs from this repository's own build tooling.
	InstallMethodProject InstallMethod = "project"
	// InstallMethodManual has no automatic flow on the current platform.
	InstallMethodManual InstallMethod = "manual"
)

// Platform selects the commands that are valid on the backend host.
type Platform string

const (
	PlatformDarwin  Platform = "darwin"
	PlatformLinux   Platform = "linux"
	PlatformWindows Platform = "windows"
)

// CurrentPlatform returns the catalog platform matching runtime.GOOS.
func CurrentPlatform() Platform {
	return Platform(runtime.GOOS)
}

// InstallStep is one preset command of an automatic lifecycle flow. Program
// and Args are executed verbatim (no shell word-splitting); Display is the
// human-readable rendering shown in the UI and results.
type InstallStep struct {
	Program       string
	Args          []string
	Dir           string
	Display       string
	SkipIfMissing bool
}

// PlatformSteps declares host-specific lifecycle commands. Shared applies to
// every platform; a non-empty platform slice replaces Shared on that host.
type PlatformSteps struct {
	Shared  []InstallStep
	Darwin  []InstallStep
	Linux   []InstallStep
	Windows []InstallStep
}

func (s PlatformSteps) For(platform Platform) []InstallStep {
	var steps []InstallStep
	switch platform {
	case PlatformDarwin:
		steps = s.Darwin
	case PlatformLinux:
		steps = s.Linux
	case PlatformWindows:
		steps = s.Windows
	}
	if len(steps) == 0 {
		steps = s.Shared
	}
	return cloneSteps(steps)
}

func (s PlatformSteps) clone() PlatformSteps {
	return PlatformSteps{
		Shared:  cloneSteps(s.Shared),
		Darwin:  cloneSteps(s.Darwin),
		Linux:   cloneSteps(s.Linux),
		Windows: cloneSteps(s.Windows),
	}
}

// InstallSpec is the preset lifecycle flow declared by a built-in agent
// catalog entry. Clients only select an AgentID; commands never come from the
// request. UninstallSteps intentionally remove executables and managed install
// artifacts only, leaving credentials, sessions and configuration untouched.
type InstallSpec struct {
	Method         InstallMethod
	InstallSteps   PlatformSteps
	UpgradeSteps   PlatformSteps
	UninstallSteps PlatformSteps
	// VersionPackage is an npm package whose published version matches the
	// executable release, even when PATH currently selects a native install.
	VersionPackage string
	// VersionURL returns the latest executable version as a plain semver string.
	VersionURL   string
	Instructions string
}

func (s InstallSpec) StepsForInstall(platform Platform) []InstallStep {
	return s.InstallSteps.For(platform)
}

// StepsForUpgrade returns the dedicated upgrade flow when one is declared for
// the platform, or the install flow for package managers/installers whose
// install command also upgrades.
func (s InstallSpec) StepsForUpgrade(platform Platform) []InstallStep {
	steps := s.UpgradeSteps.For(platform)
	if len(steps) > 0 {
		return steps
	}
	return s.StepsForInstall(platform)
}

func (s InstallSpec) StepsForUninstall(platform Platform) []InstallStep {
	return s.UninstallSteps.For(platform)
}

func (s InstallSpec) AutoInstallable(platform Platform) bool {
	return len(s.StepsForInstall(platform)) > 0
}

func (s InstallSpec) AutoUpgradeable(platform Platform) bool {
	return len(s.StepsForUpgrade(platform)) > 0
}

func (s InstallSpec) AutoUninstallable(platform Platform) bool {
	return len(s.StepsForUninstall(platform)) > 0
}

// StepDisplays returns the current platform's install commands for the UI.
func (s InstallSpec) StepDisplays(platform Platform) []string {
	steps := s.StepsForInstall(platform)
	displays := make([]string, 0, len(steps))
	for _, step := range steps {
		displays = append(displays, step.Display)
	}
	return displays
}

// UninstallStepDisplays returns the current platform's uninstall commands so
// the destructive confirmation can show exactly what Quartet will execute.
func (s InstallSpec) UninstallStepDisplays(platform Platform) []string {
	steps := s.StepsForUninstall(platform)
	displays := make([]string, 0, len(steps))
	for _, step := range steps {
		displays = append(displays, step.Display)
	}
	return displays
}

// NPMPackages returns package names managed by the current platform's install
// flow. Version suffixes such as pkg@1.2.3 and @scope/pkg@latest are stripped.
func (s InstallSpec) NPMPackages(platform Platform) []string {
	packages := make([]string, 0)
	seen := make(map[string]bool)
	for _, step := range s.StepsForInstall(platform) {
		pkg, ok := npmInstallPackage(step)
		if !ok || seen[pkg] {
			continue
		}
		seen[pkg] = true
		packages = append(packages, pkg)
	}
	return packages
}

// HasNonNPMSteps reports whether the current install flow manages anything
// outside npm. Such agents also get a local binary version probe.
func (s InstallSpec) HasNonNPMSteps(platform Platform) bool {
	for _, step := range s.StepsForInstall(platform) {
		if _, ok := npmInstallPackage(step); !ok {
			return true
		}
	}
	return false
}

// Clone returns a deep copy suitable for exposing catalog snapshots.
func (s InstallSpec) Clone() InstallSpec {
	s.InstallSteps = s.InstallSteps.clone()
	s.UpgradeSteps = s.UpgradeSteps.clone()
	s.UninstallSteps = s.UninstallSteps.clone()
	return s
}

func cloneSteps(in []InstallStep) []InstallStep {
	out := append([]InstallStep(nil), in...)
	for index := range out {
		out[index].Args = append([]string(nil), out[index].Args...)
	}
	return out
}

func npmInstallPackage(step InstallStep) (string, bool) {
	if step.Program != "npm" || len(step.Args) != 3 || step.Args[0] != "install" || step.Args[1] != "-g" {
		return "", false
	}
	spec := strings.TrimSpace(step.Args[2])
	if spec == "" {
		return "", false
	}
	if strings.HasPrefix(spec, "@") {
		slash := strings.Index(spec, "/")
		if slash < 0 {
			return spec, true
		}
		if versionAt := strings.LastIndex(spec, "@"); versionAt > slash {
			return spec[:versionAt], true
		}
		return spec, true
	}
	if versionAt := strings.LastIndex(spec, "@"); versionAt > 0 {
		return spec[:versionAt], true
	}
	return spec, true
}

// NPMStep builds the single npm install -g step for a package.
func NPMStep(pkg string) InstallStep {
	return InstallStep{Program: "npm", Args: []string{"install", "-g", pkg}, Display: "npm install -g " + pkg}
}

// NPMUninstallStep builds the matching npm uninstall -g step.
func NPMUninstallStep(pkg string) InstallStep {
	return InstallStep{Program: "npm", Args: []string{"uninstall", "-g", pkg}, Display: "npm uninstall -g " + pkg}
}

// OptionalNPMUninstallStep is used when an Agent may have been installed by
// either npm or a native installer. A missing npm executable means there is no
// npm installation to remove; other npm errors still fail with full output.
func OptionalNPMUninstallStep(pkg string) InstallStep {
	step := NPMUninstallStep(pkg)
	step.SkipIfMissing = true
	return step
}

// CommandStep builds a shell-free command step used by CLIs with native
// lifecycle commands.
func CommandStep(program string, args ...string) InstallStep {
	return InstallStep{Program: program, Args: append([]string(nil), args...), Display: strings.Join(append([]string{program}, args...), " ")}
}

// UnixScriptStep downloads an official script completely before executing it.
// This preserves curl failures: a pipeline could otherwise return the shell's
// success status after receiving an empty script.
func UnixScriptStep(url, shell string) InstallStep {
	display := "curl -fsSL " + url + " | " + shell
	command := "installer=$(mktemp \"${TMPDIR:-/tmp}/quartet-agent-install.XXXXXX\") && " +
		"trap 'rm -f \"$installer\"' EXIT HUP INT TERM && " +
		"curl -fsSL " + url + " -o \"$installer\" && " + shell + " \"$installer\""
	return InstallStep{Program: shell, Args: []string{"-c", command}, Display: display}
}

// PowerShellScriptStep downloads and executes an official Windows installer.
func PowerShellScriptStep(url string) InstallStep {
	command := "irm '" + url + "' | iex"
	return InstallStep{Program: "powershell.exe", Args: []string{"-NoProfile", "-NonInteractive", "-Command", command}, Display: command}
}

// PowerShellStep builds a controlled PowerShell command for Windows-only
// lifecycle operations.
func PowerShellStep(command string) InstallStep {
	return InstallStep{Program: "powershell.exe", Args: []string{"-NoProfile", "-NonInteractive", "-Command", command}, Display: command}
}

// RemovePathsStep builds a cross-platform executable-removal step. Every path
// is resolved relative to the running backend user's home directory, and the
// helper refuses paths outside that home directory. Directories are removed
// recursively only when explicitly listed by the trusted catalog.
func RemovePathsStep(paths ...string) InstallStep {
	return InstallStep{
		Program: InternalProgramRemovePaths,
		Args:    append([]string(nil), paths...),
		Display: "remove " + strings.Join(paths, " "),
	}
}

// ProjectMakeStep builds a repository-local install step that runs make from
// the backend process working directory.
func ProjectMakeStep(target string) InstallStep {
	return InstallStep{Program: "make", Args: []string{target}, Dir: ".", Display: "make " + target}
}

// GoBuildInstallStep builds the project-owned eino-cli directly. It works on
// Windows, macOS and Linux without depending on make or cp.
func GoBuildInstallStep() InstallStep {
	return InstallStep{Program: InternalProgramBuildEinoCLI, Display: "go build ./cmd/eino-cli and install to the user executable directory"}
}

func NPMInstallFlow(packages ...string) PlatformSteps {
	steps := make([]InstallStep, 0, len(packages))
	for _, pkg := range packages {
		steps = append(steps, NPMStep(pkg))
	}
	return PlatformSteps{Shared: steps}
}

func NPMUninstallFlow(packages ...string) PlatformSteps {
	steps := make([]InstallStep, 0, len(packages))
	for index := len(packages) - 1; index >= 0; index-- {
		steps = append(steps, NPMUninstallStep(packages[index]))
	}
	return PlatformSteps{Shared: steps}
}

// NPMOrNativeUninstallFlow removes both supported installation sources. npm
// reports success when a package is absent, so the native-path cleanup remains
// safe for script installs and npm installs alike.
func NPMOrNativeUninstallFlow(packages []string, paths ...string) PlatformSteps {
	steps := make([]InstallStep, 0, len(packages)+1)
	for index := len(packages) - 1; index >= 0; index-- {
		steps = append(steps, OptionalNPMUninstallStep(packages[index]))
	}
	steps = append(steps, RemovePathsStep(paths...))
	return PlatformSteps{Shared: steps}
}

func (s InstallSpec) Validate(agentID string) error {
	for _, platform := range []Platform{PlatformDarwin, PlatformLinux, PlatformWindows} {
		if len(s.StepsForInstall(platform)) == 0 {
			return fmt.Errorf("built-in Agent %q has no install flow for %s", agentID, platform)
		}
		if len(s.StepsForUninstall(platform)) == 0 {
			return fmt.Errorf("built-in Agent %q has no uninstall flow for %s", agentID, platform)
		}
	}
	return nil
}
