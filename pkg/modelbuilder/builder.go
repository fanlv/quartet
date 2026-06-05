package modelbuilder

import (
	"context"

	"github.com/cloudwego/eino/components/model"
)

type Builder interface {
	Build(ctx context.Context, params *LLMParams) (model.ToolCallingChatModel, error)
}
