package model

// SkillInfo is one installed skill as reported by the skills CLI
// (`skills ls --json`).
type SkillInfo struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Scope string `json:"scope"`
	// Agents holds the CLI's human-readable agent labels ("Claude Code",
	// "Trae CN"), not the slugs accepted by `skills add -a`.
	Agents     []string `json:"agents"`
	Source     string   `json:"source,omitempty"`
	SourceURL  string   `json:"sourceUrl,omitempty"`
	SourceType string   `json:"sourceType,omitempty"`
}

// SkillListResponse is the reply for GET /api/v1/skills/list.
//
// Ready reports whether the backend cache already holds a completed skills-CLI
// listing for the requested scope. Callers must not render an empty Skills as
// "nothing installed" while Ready is false — the first listing is still in
// flight. Error carries the full failure text of the last listing attempt so
// the UI can show it instead of silently rendering an empty list.
type SkillListResponse struct {
	Code   int         `json:"code"`
	Skills []SkillInfo `json:"skills"`
	Ready  bool        `json:"ready"`
	Error  string      `json:"error,omitempty"`
}

// SkillAddRequest is the request body for POST /api/v1/skills/add.
type SkillAddRequest struct {
	Package string `json:"package"`
	Global  bool   `json:"global"`
	// WorkspaceID selects the directory used for project-scope installs. It is
	// required whenever Global is false: project-scope skills only reach an
	// agent when they are installed into the workspace the agent runs in.
	WorkspaceID string   `json:"workspaceId"`
	Agents      []string `json:"agents"`
	Skills      []string `json:"skills"`
}

// SkillRemoveRequest is the request body for POST /api/v1/skills/remove.
type SkillRemoveRequest struct {
	Name        string `json:"name"`
	Global      bool   `json:"global"`
	WorkspaceID string `json:"workspaceId"`
}

// SkillUpdateRequest is the request body for POST /api/v1/skills/update.
//
// The skills CLI has no read-only "check for updates" mode — its `check`
// verb is a plain alias of `update` — so this endpoint is the only update
// entry point and always writes.
type SkillUpdateRequest struct {
	Global      bool   `json:"global"`
	WorkspaceID string `json:"workspaceId"`
}

// SkillCommandResponse is the reply for the skill mutation endpoints.
type SkillCommandResponse struct {
	Code   int    `json:"code"`
	Msg    string `json:"msg,omitempty"`
	Output string `json:"output"`
}

// SkillFindResult is one entry of the skills registry search.
type SkillFindResult struct {
	Name     string `json:"name"`
	Installs string `json:"installs"`
	URL      string `json:"url"`
}

// SkillFindResponse is the reply for GET /api/v1/skills/find.
type SkillFindResponse struct {
	Code    int               `json:"code"`
	Results []SkillFindResult `json:"results"`
}

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
