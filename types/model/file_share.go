package model

// FileShare is the durable token-to-path binding used by public file previews.
type FileShare struct {
	Token     string `json:"token"`
	Path      string `json:"path"`
	CreatedAt int64  `json:"createdAt"`
}
