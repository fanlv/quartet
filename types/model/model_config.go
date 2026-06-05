package model

import "github.com/fanlv/quartet/pkg/modelbuilder"

type ModelInstance struct {
	ID              int64                        `json:"id"`
	ModelClass      modelbuilder.ModelClass      `json:"model_class"`
	DisplayName     string                       `json:"display_name"`
	Connection      *modelbuilder.ConnectionInfo `json:"connection"`
	ThinkingType    modelbuilder.ThinkingType    `json:"thinking_type,omitempty"`
	EnableBase64URL bool                         `json:"enable_base64_url,omitempty"`
	Status          int                          `json:"status"`
	CreatedAt       int64                        `json:"created_at"`
	UpdatedAt       int64                        `json:"updated_at"`
	DeletedAt       int64                        `json:"deleted_at,omitempty"`
}

type ProviderInfo struct {
	ModelClass    modelbuilder.ModelClass `json:"model_class"`
	Name          string                  `json:"name"`
	Description   string                  `json:"description"`
	IconURL       string                  `json:"icon_url"`
	NameEN        string                  `json:"-"`
	DescriptionEN string                  `json:"-"`
}

type ProviderModelList struct {
	Provider  *ProviderInfo    `json:"provider"`
	ModelList []*ModelInstance `json:"model_list"`
}

type CreateModelRequest struct {
	ModelClass      modelbuilder.ModelClass      `json:"model_class"`
	DisplayName     string                       `json:"display_name"`
	Connection      *modelbuilder.ConnectionInfo `json:"connection"`
	ThinkingType    modelbuilder.ThinkingType    `json:"thinking_type,omitempty"`
	EnableBase64URL bool                         `json:"enable_base64_url,omitempty"`
}

var DefaultProviders = []ProviderInfo{
	{ModelClass: modelbuilder.ModelClassArk, Name: "豆包模型", Description: "火山引擎 Ark 大模型服务", IconURL: "https://upload-images.jianshu.io/upload_images/12321605-0ece441a9983a40d.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240", NameEN: "Doubao", DescriptionEN: "Volcengine Ark LLM Service"},
	{ModelClass: modelbuilder.ModelClassOpenAI, Name: "OpenAI", Description: "OpenAI GPT 系列模型", IconURL: "https://upload-images.jianshu.io/upload_images/12321605-91a8106e59f7126f.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240", NameEN: "OpenAI", DescriptionEN: "OpenAI GPT Series"},
	{ModelClass: modelbuilder.ModelClassClaude, Name: "Claude", Description: "Anthropic Claude 系列模型", IconURL: "https://upload-images.jianshu.io/upload_images/12321605-2fc28d63c089a216.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240", NameEN: "Claude", DescriptionEN: "Anthropic Claude Series"},
	{ModelClass: modelbuilder.ModelClassDeepSeek, Name: "DeepSeek", Description: "DeepSeek 深度求索", IconURL: "https://upload-images.jianshu.io/upload_images/12321605-6a3bdc5e184a6e04.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240", NameEN: "DeepSeek", DescriptionEN: "DeepSeek AI"},
	{ModelClass: modelbuilder.ModelClassGemini, Name: "Gemini", Description: "Google Gemini 系列模型", IconURL: "https://upload-images.jianshu.io/upload_images/12321605-21f811ad1bed58bd.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240", NameEN: "Gemini", DescriptionEN: "Google Gemini Series"},
	{ModelClass: modelbuilder.ModelClassOllama, Name: "Ollama", Description: "本地部署模型", IconURL: "https://upload-images.jianshu.io/upload_images/12321605-ee4bd5afa8598a64.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240", NameEN: "Ollama", DescriptionEN: "Locally Deployed Models"},
	{ModelClass: modelbuilder.ModelClassQwen, Name: "通义千问", Description: "阿里云通义千问", IconURL: "https://upload-images.jianshu.io/upload_images/12321605-2763958be48a880a.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240", NameEN: "Qwen", DescriptionEN: "Alibaba Cloud Qwen"},
}
