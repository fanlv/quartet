package modelbuilder

import (
	"context"

	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/components/model"
)

type claudeModelBuilder struct {
	cfg *ModelConfig
}

func newClaudeModelBuilder(cfg *ModelConfig) Builder {
	return &claudeModelBuilder{cfg: cfg}
}

func (b *claudeModelBuilder) Build(ctx context.Context, params *LLMParams) (model.ToolCallingChatModel, error) {
	conn := b.cfg.Connection

	conf := &claude.Config{
		APIKey: conn.APIKey,
		Model:  conn.Model,
	}

	if conn.BaseURL != "" {
		conf.BaseURL = &conn.BaseURL
	}

	switch b.cfg.ThinkingType {
	case ThinkingTypeEnable:
		conf.Thinking = &claude.Thinking{Enable: true}
	case ThinkingTypeDisable:
		conf.Thinking = &claude.Thinking{Enable: false}
	}

	b.applyParams(conf, params)

	return claude.NewChatModel(ctx, conf)
}

func (b *claudeModelBuilder) applyParams(conf *claude.Config, params *LLMParams) {
	if params == nil {
		return
	}

	conf.TopP = params.TopP
	conf.TopK = params.TopK
	conf.Temperature = params.Temperature

	if params.MaxTokens != nil {
		conf.MaxTokens = *params.MaxTokens
	}

	if params.EnableThinking != nil {
		conf.Thinking = &claude.Thinking{Enable: *params.EnableThinking}
	}
}
