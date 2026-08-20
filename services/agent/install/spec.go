package install

import "strings"

// InstallMethod classifies how a built-in agent's CLI is installed.
type InstallMethod string

const (
	// InstallMethodNPM installs via one or more `npm install -g <pkg>` steps.
	InstallMethodNPM InstallMethod = "npm"
	// InstallMethodScript installs via an official install script (at least one
	// step pipes a script into a shell).
	InstallMethodScript InstallMethod = "script"
	// InstallMethodProject installs from this repository's own build tooling.
	InstallMethodProject InstallMethod = "project"
	// InstallMethodManual has no automatic flow; only manual instructions are
	// shown (used for installs requiring interaction, auth, or a project
	// toolchain).
	InstallMethodManual InstallMethod = "manual"
)

// InstallStep is one preset command of an automatic install flow. Program and
// Args are executed verbatim (no shell word-splitting); Display is the
// human-readable rendering shown in the UI and results.
type InstallStep struct {
	Program string
	Args    []string
	Dir     string
	Display string
}

// InstallSpec is the preset install flow declared by a built-in agent catalog
// entry. Steps run sequentially and only ever come from the catalog — the
// install API never accepts commands from the client. Manual entries carry no
// Steps and only Instructions.
type InstallSpec struct {
	Method       InstallMethod
	Steps        []InstallStep
	UpgradeSteps []InstallStep
	// VersionPackage is an npm package whose published version matches the
	// executable release, even when the executable currently selected by PATH
	// was installed by that CLI's native installer.
	VersionPackage string
	// VersionURL returns the latest executable version as a plain semver string.
	// It is used for native installers whose release channel is not npm-backed.
	VersionURL   string
	Instructions string
}

// AutoInstallable reports whether this spec has an executable automatic flow.
func (s InstallSpec) AutoInstallable() bool {
	return len(s.Steps) > 0
}

// AutoUpgradeable reports whether an installed Agent has a controlled upgrade
// flow. Entries without dedicated UpgradeSteps reuse their install steps.
func (s InstallSpec) AutoUpgradeable() bool {
	return len(s.UpgradeSteps) > 0 || len(s.Steps) > 0
}

// StepsForUpgrade returns the dedicated upgrade flow when one is declared, or
// the install flow for package managers whose install command also upgrades.
func (s InstallSpec) StepsForUpgrade() []InstallStep {
	if len(s.UpgradeSteps) > 0 {
		return s.UpgradeSteps
	}
	return s.Steps
}

// UninstallSteps returns the automatic uninstall flow, derived by reversing the
// npm install steps into `npm uninstall -g <pkg>` (last-installed removed
// first). Only npm-method specs are auto-uninstallable: script and manual
// installs have no reliable reverse, so this returns nil for them.
func (s InstallSpec) UninstallSteps() []InstallStep {
	if s.Method != InstallMethodNPM {
		return nil
	}
	steps := make([]InstallStep, 0, len(s.Steps))
	for i := len(s.Steps) - 1; i >= 0; i-- {
		// Each npm install step is NPMStep("pkg") → Args ["install","-g",pkg].
		args := s.Steps[i].Args
		if len(args) != 3 || args[0] != "install" {
			continue
		}
		steps = append(steps, npmUninstallStep(args[2]))
	}
	return steps
}

// AutoUninstallable reports whether this spec has an executable automatic
// uninstall flow.
func (s InstallSpec) AutoUninstallable() bool {
	return len(s.UninstallSteps()) > 0
}

// StepDisplays returns the human-readable rendering of every step, for UI
// display before anything runs.
func (s InstallSpec) StepDisplays() []string {
	displays := make([]string, 0, len(s.Steps))
	for _, step := range s.Steps {
		displays = append(displays, step.Display)
	}
	return displays
}

// NPMPackages returns the package names managed by this install spec. Version
// suffixes such as "pkg@1.2.3" and "@scope/pkg@latest" are stripped so the
// names can be matched against npm's global list/outdated JSON output.
func (s InstallSpec) NPMPackages() []string {
	packages := make([]string, 0)
	seen := make(map[string]bool)
	for _, step := range s.Steps {
		pkg, ok := npmInstallPackage(step)
		if !ok || seen[pkg] {
			continue
		}
		seen[pkg] = true
		packages = append(packages, pkg)
	}
	return packages
}

// HasNonNPMSteps reports whether this install spec manages anything outside
// npm. Those agents also get a local `<bin> --version` probe because the npm
// package list alone does not describe their complete installation.
func (s InstallSpec) HasNonNPMSteps() bool {
	for _, step := range s.Steps {
		if _, ok := npmInstallPackage(step); !ok {
			return true
		}
	}
	return false
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

// NPMStep builds the single `npm install -g <pkg>` step for a package.
func NPMStep(pkg string) InstallStep {
	return InstallStep{
		Program: "npm",
		Args:    []string{"install", "-g", pkg},
		Display: "npm install -g " + pkg,
	}
}

// npmUninstallStep builds the single `npm uninstall -g <pkg>` step used by
// the generic uninstall flow. It is intentionally not part of the catalog's
// public install-step builders: install and upgrade specs should only describe
// the components they own, not historical package cleanup.
func npmUninstallStep(pkg string) InstallStep {
	return InstallStep{
		Program: "npm",
		Args:    []string{"uninstall", "-g", pkg},
		Display: "npm uninstall -g " + pkg,
	}
}

// CommandStep builds a shell-free command step used by CLIs with a native
// updater, such as `grok update` and `qoderclicn update`.
func CommandStep(program string, args ...string) InstallStep {
	return InstallStep{
		Program: program,
		Args:    append([]string(nil), args...),
		Display: strings.Join(append([]string{program}, args...), " "),
	}
}

// ScriptStep builds a step piping an official install script into a shell,
// e.g. ScriptStep("https://cursor.com/install", "bash").
func ScriptStep(url, shell string) InstallStep {
	command := "curl -fsSL " + url + " | " + shell
	return InstallStep{
		Program: shell,
		Args:    []string{"-c", command},
		Display: command,
	}
}

// ProjectMakeStep builds a repository-local install step that runs make from
// the backend process working directory.
func ProjectMakeStep(target string) InstallStep {
	return InstallStep{
		Program: "make",
		Args:    []string{target},
		Dir:     ".",
		Display: "make " + target,
	}
}
