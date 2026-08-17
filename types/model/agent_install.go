package model

// AgentInstallCandidate describes one not-yet-installed built-in agent shown
// in the add-agent form. Manual-only entries carry Instructions and no
// InstallCommands.
type AgentInstallCandidate struct {
	AgentID         string   `json:"agent_id"`
	Bin             string   `json:"bin"`
	ACPProgram      string   `json:"acp_program"`
	Command         string   `json:"command"`
	DisplayName     string   `json:"display_name"`
	IconURL         string   `json:"icon_url"`
	InstallMethod   string   `json:"install_method"`
	InstallCommands []string `json:"install_commands,omitempty"`
	Instructions    string   `json:"instructions,omitempty"`
	AutoInstallable bool     `json:"auto_installable"`
}

type AgentInstallCandidatesResponse struct {
	Code       int                     `json:"code"`
	Candidates []AgentInstallCandidate `json:"candidates"`
}

// AgentInstallRequest installs a built-in agent by its catalog AgentID. The
// backend only ever runs the preset catalog flow for that AgentID — clients
// cannot supply commands.
type AgentInstallRequest struct {
	AgentID string `json:"agent_id"`
}

// AgentInstallStepResult is the complete outcome of one executed install
// step. Stdout/Stderr are returned verbatim.
type AgentInstallStepResult struct {
	Display    string `json:"display"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// AgentValidationResult is the outcome of the full ACP validation run after a
// successful install recheck.
type AgentValidationResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// AgentInstallResult carries the full install attempt: every executed step's
// output, the post-install recheck, and the ACP validation result.
type AgentInstallResult struct {
	AgentID string                   `json:"agent_id"`
	Steps   []AgentInstallStepResult `json:"steps"`
	// Installed is the unified install recheck after the steps ran. A zero
	// exit status with the binaries still missing from the backend PATH
	// leaves this false.
	Installed    bool                   `json:"installed"`
	InstallError string                 `json:"install_error,omitempty"`
	Validation   *AgentValidationResult `json:"validation,omitempty"`
}

type AgentInstallResponse struct {
	Code   int                 `json:"code"`
	Result *AgentInstallResult `json:"result"`
}
