package modelbuilder

type LLMParams struct {
	Temperature      *float32       `json:"temperature,omitempty" yaml:"temperature,omitempty"`
	FrequencyPenalty *float32       `json:"frequency_penalty,omitempty" yaml:"frequency_penalty,omitempty"`
	PresencePenalty  *float32       `json:"presence_penalty,omitempty" yaml:"presence_penalty,omitempty"`
	MaxTokens        *int           `json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`
	TopP             *float32       `json:"top_p,omitempty" yaml:"top_p,omitempty"`
	TopK             *int32         `json:"top_k,omitempty" yaml:"top_k,omitempty"`
	ResponseFormat   ResponseFormat `json:"response_format,omitempty" yaml:"response_format,omitempty"`
	EnableThinking   *bool          `json:"enable_thinking,omitempty" yaml:"enable_thinking,omitempty"`
}

type BuildOption func(p *LLMParams)

func WithLLMMaxTokens(maxTokens int) BuildOption {
	return func(p *LLMParams) {
		p.MaxTokens = &maxTokens
	}
}

func ptr[T any](v T) *T {
	return &v
}
