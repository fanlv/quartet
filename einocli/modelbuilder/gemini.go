package modelbuilder

import (
	"context"

	"github.com/cloudwego/eino-ext/components/model/gemini"
	"github.com/cloudwego/eino/components/model"
	"google.golang.org/genai"
)

type geminiModelBuilder struct {
	cfg *ModelConfig
}

func newGeminiModelBuilder(cfg *ModelConfig) Builder {
	return &geminiModelBuilder{cfg: cfg}
}

func (b *geminiModelBuilder) Build(ctx context.Context, params *LLMParams) (model.ToolCallingChatModel, error) {
	conn := b.cfg.Connection

	clientCfg := &genai.ClientConfig{
		APIKey: conn.APIKey,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: "https://generativelanguage.googleapis.com/",
		},
	}

	if conn.BaseURL != "" {
		clientCfg.HTTPOptions.BaseURL = conn.BaseURL
	}

	if conn.Gemini != nil {
		clientCfg.Backend = genai.BackendGeminiAPI
		if conn.Gemini.Backend == "vertex" {
			clientCfg.Backend = genai.BackendVertexAI
		}
		clientCfg.Project = conn.Gemini.Project
		clientCfg.Location = conn.Gemini.Location
	}

	client, err := genai.NewClient(ctx, clientCfg)
	if err != nil {
		return nil, err
	}

	conf := &gemini.Config{
		Client: client,
		Model:  conn.Model,
	}

	switch b.cfg.ThinkingType {
	case ThinkingTypeEnable:
		conf.ThinkingConfig = &genai.ThinkingConfig{IncludeThoughts: true}
	case ThinkingTypeDisable:
		conf.ThinkingConfig = &genai.ThinkingConfig{IncludeThoughts: false}
	}

	b.applyParams(conf, params)

	return gemini.NewChatModel(ctx, conf)
}

func (b *geminiModelBuilder) applyParams(conf *gemini.Config, params *LLMParams) {
	if params == nil {
		return
	}

	conf.TopP = params.TopP
	conf.TopK = params.TopK
	conf.Temperature = params.Temperature

	if params.MaxTokens != nil {
		conf.MaxTokens = params.MaxTokens
	}

	if params.EnableThinking != nil {
		conf.ThinkingConfig = &genai.ThinkingConfig{
			IncludeThoughts: *params.EnableThinking,
		}
	}
}
