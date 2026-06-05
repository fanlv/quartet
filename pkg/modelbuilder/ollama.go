package modelbuilder

import (
	"context"

	"github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino/components/model"
	"github.com/eino-contrib/ollama/api"
)

type ollamaModelBuilder struct {
	cfg *ModelConfig
}

func newOllamaModelBuilder(cfg *ModelConfig) Builder {
	return &ollamaModelBuilder{cfg: cfg}
}

func (b *ollamaModelBuilder) Build(ctx context.Context, params *LLMParams) (model.ToolCallingChatModel, error) {
	conn := b.cfg.Connection

	conf := &ollama.ChatModelConfig{
		Model:   conn.Model,
		BaseURL: "http://127.0.0.1:11434",
		Options: &api.Options{},
	}

	if conn.BaseURL != "" {
		conf.BaseURL = conn.BaseURL
	}

	switch b.cfg.ThinkingType {
	case ThinkingTypeEnable:
		conf.Thinking = &api.ThinkValue{Value: ptr(true)}
	case ThinkingTypeDisable:
		conf.Thinking = &api.ThinkValue{Value: ptr(false)}
	}

	b.applyParams(conf, params)

	return ollama.NewChatModel(ctx, conf)
}

func (b *ollamaModelBuilder) applyParams(conf *ollama.ChatModelConfig, params *LLMParams) {
	if params == nil {
		return
	}

	if params.Temperature != nil {
		conf.Options.Temperature = *params.Temperature
	}

	if params.TopP != nil {
		conf.Options.TopP = *params.TopP
	}

	if params.TopK != nil {
		conf.Options.TopK = int(*params.TopK)
	}

	if params.FrequencyPenalty != nil {
		conf.Options.FrequencyPenalty = *params.FrequencyPenalty
	}

	if params.PresencePenalty != nil {
		conf.Options.PresencePenalty = *params.PresencePenalty
	}

	if params.EnableThinking != nil {
		conf.Thinking = &api.ThinkValue{Value: params.EnableThinking}
	}
}
