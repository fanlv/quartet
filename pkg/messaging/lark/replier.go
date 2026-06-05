package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fanlv/quartet/pkg/messaging"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type Replier struct {
	configFn ConfigProvider
	brand    string

	// Cache the SDK client so we reuse the SDK's internal tenant_access_token
	// cache across calls. Rebuilding lark.Client on every SendText/ReplyText
	// drops the token cache and risks hitting the 60/minute token API limit.
	client larkClientCache
}

var _ messaging.Replier = (*Replier)(nil)

func NewReplier(configFn ConfigProvider) *Replier {
	return &Replier{configFn: configFn, brand: os.Getenv(envBrand)}
}

func (r *Replier) SendText(ctx context.Context, chatID, content string) error {
	client, err := r.newClient()
	if err != nil {
		return err
	}
	postContent, err := buildPostContent(content)
	if err != nil {
		return fmt.Errorf("build post content failed: %w", err)
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypePost).
			Content(postContent).
			Build()).
		Build()

	resp, err := client.Im.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("send message failed: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark API error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (r *Replier) ReplyText(ctx context.Context, messageID, content string) error {
	client, err := r.newClient()
	if err != nil {
		return err
	}
	postContent, err := buildPostContent(content)
	if err != nil {
		return fmt.Errorf("build post content failed: %w", err)
	}

	req := larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(larkim.MsgTypePost).
			Content(postContent).
			Build()).
		Build()

	resp, err := client.Im.Message.Reply(ctx, req)
	if err != nil {
		return fmt.Errorf("reply message failed: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark API error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (r *Replier) newClient() (*lark.Client, error) {
	return r.client.Get(r.configFn, r.brand)
}

type postContent struct {
	ZhCN postLocale `json:"zh_cn"`
	EnUS postLocale `json:"en_us"`
}

type postLocale struct {
	Title   string          `json:"title"`
	Content [][]postElement `json:"content"`
}

type postElement struct {
	Tag  string `json:"tag"`
	Text string `json:"text"`
}

func buildPostContent(content string) (string, error) {
	locale := postLocale{
		Title: "",
		Content: [][]postElement{
			{
				{
					Tag:  "md",
					Text: content,
				},
			},
		},
	}
	payload := postContent{
		ZhCN: locale,
		EnUS: locale,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
