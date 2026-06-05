package modelbuilder

import (
	"context"

	"github.com/cloudwego/eino-ext/components/model/ark"
	"github.com/cloudwego/eino/components/model"
	arkmodel "github.com/volcengine/volcengine-go-sdk/service/arkruntime/model"
)

type arkModelBuilder struct {
	cfg *ModelConfig
}

func newArkModelBuilder(cfg *ModelConfig) Builder {
	return &arkModelBuilder{cfg: cfg}
}

func (b *arkModelBuilder) Build(ctx context.Context, params *LLMParams) (model.ToolCallingChatModel, error) {
	conn := b.cfg.Connection

	conf := &ark.ChatModelConfig{
		APIKey:  conn.APIKey,
		BaseURL: conn.BaseURL,
		Model:   conn.Model,
	}

	if conn.Ark != nil {
		conf.Region = conn.Ark.Region
	}

	switch b.cfg.ThinkingType {
	case ThinkingTypeEnable:
		conf.Thinking = &arkmodel.Thinking{Type: arkmodel.ThinkingTypeEnabled}
	case ThinkingTypeDisable:
		conf.Thinking = &arkmodel.Thinking{Type: arkmodel.ThinkingTypeDisabled}
	case ThinkingTypeAuto:
		conf.Thinking = &arkmodel.Thinking{Type: arkmodel.ThinkingTypeAuto}
	}

	b.applyParams(conf, params)

	return ark.NewChatModel(ctx, conf)
}

func (b *arkModelBuilder) applyParams(conf *ark.ChatModelConfig, params *LLMParams) {
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
		if *params.EnableThinking {
			conf.Thinking = &arkmodel.Thinking{Type: arkmodel.ThinkingTypeEnabled}
		} else {
			conf.Thinking = &arkmodel.Thinking{Type: arkmodel.ThinkingTypeDisabled}
		}
	}

	switch params.ResponseFormat {
	case ResponseFormatJSON:
		conf.ResponseFormat = &ark.ResponseFormat{Type: arkmodel.ResponseFormatJsonObject}
	case ResponseFormatText, ResponseFormatMarkdown:
		conf.ResponseFormat = &ark.ResponseFormat{Type: arkmodel.ResponseFormatText}
	}
}
