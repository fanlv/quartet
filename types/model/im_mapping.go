package model

// IMJobMapping binds one external chat to its current Quartet workspace and
// Job. It is a domain model shared by the IM service and persistence layer.
type IMJobMapping struct {
	Platform    string `json:"platform"`
	ChatID      string `json:"chatId"`
	WorkspaceID string `json:"workspaceId"`
	JobID       string `json:"jobId"`
}
