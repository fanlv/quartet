package model

const MessagePresetSchemaVersion = 1

type MessagePreset struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
}

type MessagePresetConfig struct {
	SchemaVersion    int             `json:"schemaVersion"`
	WorkspaceID      string          `json:"workspaceId,omitempty"`
	WorkspaceTitle   string          `json:"workspaceTitle,omitempty"`
	WorkspaceWorkdir string          `json:"workspaceWorkdir,omitempty"`
	Messages         []MessagePreset `json:"messages"`
}

type MessagePresetScopeResponse struct {
	Code     int                 `json:"code"`
	Revision string              `json:"revision"`
	Config   MessagePresetConfig `json:"config"`
}

type MessagePresetLoadError struct {
	Scope string `json:"scope"`
	File  string `json:"file"`
	Error string `json:"error"`
}

type EffectiveMessagePresetsResponse struct {
	Code        int                      `json:"code"`
	WorkspaceID string                   `json:"workspaceId"`
	Project     []MessagePreset          `json:"project"`
	Global      []MessagePreset          `json:"global"`
	Errors      []MessagePresetLoadError `json:"errors,omitempty"`
}

type SaveMessagePresetScopeRequest struct {
	Revision string          `json:"revision"`
	Messages []MessagePreset `json:"messages"`
}

type OrphanMessagePreset struct {
	Revision string              `json:"revision"`
	Config   MessagePresetConfig `json:"config"`
}

type ListOrphanMessagePresetsResponse struct {
	Code    int                      `json:"code"`
	Configs []OrphanMessagePreset    `json:"configs"`
	Errors  []MessagePresetLoadError `json:"errors,omitempty"`
}

type RebindMessagePresetRequest struct {
	Revision          string `json:"revision"`
	TargetWorkspaceID string `json:"targetWorkspaceId"`
}
