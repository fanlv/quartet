package modelbuilder

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model"
)

var modelClassBuilders = map[ModelClass]func(*ModelConfig) Builder{
	ModelClassArk:      newArkModelBuilder,
	ModelClassOpenAI:   newOpenaiModelBuilder,
	ModelClassClaude:   newClaudeModelBuilder,
	ModelClassDeepSeek: newDeepseekModelBuilder,
	ModelClassGemini:   newGeminiModelBuilder,
	ModelClassOllama:   newOllamaModelBuilder,
	ModelClassQwen:     newQwenModelBuilder,
}

// IsSupportedClass reports whether a model class has a registered builder. It
// is the single source of truth for "is this a valid class": the config
// validator delegates here, so adding a provider means editing only
// modelClassBuilders.
func IsSupportedClass(c ModelClass) bool {
	_, ok := modelClassBuilders[c]
	return ok
}

// SupportsThoughtLevel reports whether a model class exposes a meaningful
// thinking switch: ark/claude/gemini/qwen/ollama do; openai/deepseek do not.
// It is the single home for this per-class fact; the ACP config-option layer
// delegates here when deciding whether to advertise a thought_level selector.
func SupportsThoughtLevel(c ModelClass) bool {
	switch c {
	case ModelClassArk,
		ModelClassClaude,
		ModelClassGemini,
		ModelClassQwen,
		ModelClassOllama:
		return true
	}
	return false
}

func NewBuilder(cfg *ModelConfig) (Builder, error) {
	if cfg == nil {
		return nil, fmt.Errorf("model config is nil")
	}
	if cfg.Connection == nil {
		return nil, fmt.Errorf("model connection is nil")
	}

	builderFn, ok := modelClassBuilders[cfg.ModelClass]
	if !ok {
		return nil, fmt.Errorf("model class %q not supported", cfg.ModelClass)
	}

	return builderFn(cfg), nil
}

func BuildModel(ctx context.Context, cfg *ModelConfig, opts ...BuildOption) (model.ToolCallingChatModel, error) {
	builder, err := NewBuilder(cfg)
	if err != nil {
		return nil, err
	}

	params := &LLMParams{}
	for _, opt := range opts {
		opt(params)
	}

	return builder.Build(ctx, params)
}
