package model

// EinoModel is the quartet-side view of one model entry in eino-cli's own
// catalog (~/.eino/models.json). The API key only ever appears masked here —
// it is written to eino-cli at creation and never read back.
type EinoModel struct {
	ID           string               `json:"id"`
	ModelClass   string               `json:"model_class"`
	DisplayName  string               `json:"display_name"`
	Connection   *EinoModelConnection `json:"connection"`
	ThinkingType string               `json:"thinking_type,omitempty"`
	CreatedAt    int64                `json:"created_at,omitempty"`
	UpdatedAt    int64                `json:"updated_at,omitempty"`
}

// EinoModelConnection carries provider connection fields for an eino model.
type EinoModelConnection struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model"`
}

// CreateEinoModelRequest is the payload for adding a model to eino-cli.
type CreateEinoModelRequest struct {
	ModelClass   string               `json:"model_class"`
	DisplayName  string               `json:"display_name"`
	Connection   *EinoModelConnection `json:"connection"`
	ThinkingType string               `json:"thinking_type,omitempty"`
}

// SaveEinoSystemPromptRequest updates eino-cli's system prompt.
type SaveEinoSystemPromptRequest struct {
	SystemPrompt string `json:"system_prompt"`
}
