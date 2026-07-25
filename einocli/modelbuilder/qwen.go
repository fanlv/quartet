package modelbuilder

import (
	"context"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino-ext/components/model/qwen"
	"github.com/cloudwego/eino/components/model"
)

type qwenModelBuilder struct {
	cfg *ModelConfig
}

func newQwenModelBuilder(cfg *ModelConfig) Builder {
	return &qwenModelBuilder{cfg: cfg}
}

func (b *qwenModelBuilder) Build(ctx context.Context, params *LLMParams) (model.ToolCallingChatModel, error) {
	conn := b.cfg.Connection

	conf := &qwen.ChatModelConfig{
		APIKey:      conn.APIKey,
		BaseURL:     conn.BaseURL,
		Model:       conn.Model,
		Temperature: ptr(float32(0.7)),
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeText,
		},
	}

	switch b.cfg.ThinkingType {
	case ThinkingTypeEnable:
		conf.EnableThinking = ptr(true)
	case ThinkingTypeDisable:
		conf.EnableThinking = ptr(false)
	}

	b.applyParams(conf, params)

	return qwen.NewChatModel(ctx, conf)
}

func (b *qwenModelBuilder) applyParams(conf *qwen.ChatModelConfig, params *LLMParams) {
	if params == nil {
		return
	}

	conf.TopP = params.TopP
	conf.Temperature = params.Temperature

	if params.MaxTokens != nil {
		conf.MaxTokens = params.MaxTokens
	}

	if params.FrequencyPenalty != nil {
		conf.FrequencyPenalty = params.FrequencyPenalty
	}

	if params.PresencePenalty != nil {
		conf.PresencePenalty = params.PresencePenalty
	}

	if params.EnableThinking != nil {
		conf.EnableThinking = params.EnableThinking
	}
}
