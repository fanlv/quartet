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
