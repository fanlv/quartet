package install

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
	Instructions string
}

// AutoInstallable reports whether this spec has an executable automatic flow.
func (s InstallSpec) AutoInstallable() bool {
	return len(s.Steps) > 0
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
		steps = append(steps, NPMUninstallStep(args[2]))
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

// NPMStep builds the single `npm install -g <pkg>` step for a package.
func NPMStep(pkg string) InstallStep {
	return InstallStep{
		Program: "npm",
		Args:    []string{"install", "-g", pkg},
		Display: "npm install -g " + pkg,
	}
}

// NPMUninstallStep builds the single `npm uninstall -g <pkg>` step for a
// package.
func NPMUninstallStep(pkg string) InstallStep {
	return InstallStep{
		Program: "npm",
		Args:    []string{"uninstall", "-g", pkg},
		Display: "npm uninstall -g " + pkg,
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
