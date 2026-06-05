package modelbuilder

import (
	"context"

	"github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino/components/model"
)

type deepseekModelBuilder struct {
	cfg *ModelConfig
}

func newDeepseekModelBuilder(cfg *ModelConfig) Builder {
	return &deepseekModelBuilder{cfg: cfg}
}

func (b *deepseekModelBuilder) Build(ctx context.Context, params *LLMParams) (model.ToolCallingChatModel, error) {
	conn := b.cfg.Connection

	conf := &deepseek.ChatModelConfig{
		APIKey:  conn.APIKey,
		BaseURL: conn.BaseURL,
		Model:   conn.Model,
	}

	b.applyParams(conf, params)

	return deepseek.NewChatModel(ctx, conf)
}

func (b *deepseekModelBuilder) applyParams(conf *deepseek.ChatModelConfig, params *LLMParams) {
	if params == nil {
		return
	}

	if params.Temperature != nil {
		conf.Temperature = *params.Temperature
	}

	if params.TopP != nil {
		conf.TopP = *params.TopP
	}

	if params.MaxTokens != nil {
		conf.MaxTokens = *params.MaxTokens
	}

	if params.FrequencyPenalty != nil {
		conf.FrequencyPenalty = *params.FrequencyPenalty
	}

	if params.PresencePenalty != nil {
		conf.PresencePenalty = *params.PresencePenalty
	}

	switch params.ResponseFormat {
	case ResponseFormatJSON:
		conf.ResponseFormatType = deepseek.ResponseFormatTypeJSONObject
	default:
		conf.ResponseFormatType = deepseek.ResponseFormatTypeText
	}
}
