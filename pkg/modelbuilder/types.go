package modelbuilder

type ModelClass string

const (
	ModelClassArk      ModelClass = "ark"
	ModelClassOpenAI   ModelClass = "openai"
	ModelClassClaude   ModelClass = "claude"
	ModelClassDeepSeek ModelClass = "deepseek"
	ModelClassGemini   ModelClass = "gemini"
	ModelClassOllama   ModelClass = "ollama"
	ModelClassQwen     ModelClass = "qwen"
)

type ThinkingType string

const (
	ThinkingTypeAuto    ThinkingType = "auto"
	ThinkingTypeEnable  ThinkingType = "enable"
	ThinkingTypeDisable ThinkingType = "disable"
)

type ResponseFormat string

const (
	ResponseFormatText     ResponseFormat = "text"
	ResponseFormatJSON     ResponseFormat = "json"
	ResponseFormatMarkdown ResponseFormat = "markdown"
)

type ModelConfig struct {
	ModelClass   ModelClass       `json:"model_class" yaml:"model_class"`
	Connection   *ConnectionInfo  `json:"connection" yaml:"connection"`
	ThinkingType ThinkingType     `json:"thinking_type,omitempty" yaml:"thinking_type,omitempty"`
}

type ConnectionInfo struct {
	APIKey  string `json:"api_key" yaml:"api_key"`
	BaseURL string `json:"base_url,omitempty" yaml:"base_url,omitempty"`
	Model   string `json:"model" yaml:"model"`

	Ark    *ArkConnectionInfo    `json:"ark,omitempty" yaml:"ark,omitempty"`
	OpenAI *OpenAIConnectionInfo `json:"openai,omitempty" yaml:"openai,omitempty"`
	Gemini *GeminiConnectionInfo `json:"gemini,omitempty" yaml:"gemini,omitempty"`
}

type ArkConnectionInfo struct {
	Region string `json:"region,omitempty" yaml:"region,omitempty"`
}

type OpenAIConnectionInfo struct {
	ByAzure    bool   `json:"by_azure,omitempty" yaml:"by_azure,omitempty"`
	APIVersion string `json:"api_version,omitempty" yaml:"api_version,omitempty"`
}

type GeminiConnectionInfo struct {
	Backend  string `json:"backend,omitempty" yaml:"backend,omitempty"`
	Project  string `json:"project,omitempty" yaml:"project,omitempty"`
	Location string `json:"location,omitempty" yaml:"location,omitempty"`
}
