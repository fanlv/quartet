package modelbuilder

import (
	"context"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

type openaiModelBuilder struct {
	cfg *ModelConfig
}

func newOpenaiModelBuilder(cfg *ModelConfig) Builder {
	return &openaiModelBuilder{cfg: cfg}
}

func (b *openaiModelBuilder) Build(ctx context.Context, params *LLMParams) (model.ToolCallingChatModel, error) {
	conn := b.cfg.Connection

	conf := &openai.ChatModelConfig{
		APIKey:              conn.APIKey,
		BaseURL:             conn.BaseURL,
		Model:               conn.Model,
		MaxCompletionTokens: ptr(4096),
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeText,
		},
	}

	if conn.OpenAI != nil {
		conf.ByAzure = conn.OpenAI.ByAzure
		conf.APIVersion = conn.OpenAI.APIVersion
	}

	b.applyParams(conf, params)

	return openai.NewChatModel(ctx, conf)
}

func (b *openaiModelBuilder) applyParams(conf *openai.ChatModelConfig, params *LLMParams) {
	if params == nil {
		return
	}

	conf.TopP = params.TopP
	conf.Temperature = params.Temperature

	if params.MaxTokens != nil {
		conf.MaxCompletionTokens = params.MaxTokens
	}

	if params.FrequencyPenalty != nil {
		conf.FrequencyPenalty = params.FrequencyPenalty
	}

	if params.PresencePenalty != nil {
		conf.PresencePenalty = params.PresencePenalty
	}

	switch params.ResponseFormat {
	case ResponseFormatJSON:
		conf.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		}
	default:
		conf.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeText,
		}
	}
}
