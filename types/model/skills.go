package model

// ProjectToolsInstallResult is the complete result of installing the
// repository-owned quartet-cli binary and all skills shipped by this project.
type ProjectToolsInstallResult struct {
	Command    string `json:"command"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

type ProjectToolsInstallResponse struct {
	Code   int                        `json:"code"`
	Result *ProjectToolsInstallResult `json:"result"`
}
