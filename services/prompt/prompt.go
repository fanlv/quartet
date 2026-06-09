package prompt

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/consts"
	typepath "github.com/fanlv/quartet/types/path"
)

type promptReference struct {
	key          string
	placeholders []string
	content      string
}

var promptReferences = []promptReference{
	{key: "SOUL", placeholders: []string{"{{SOUL.MD}}", "{{SOUL.md}}", "｛SOUL.md｝", "{SOUL.md}"}},
	{key: "USER", placeholders: []string{"{{USER.MD}}", "{{USER.md}}", "｛USER.md｝", "{USER.md}"}},
	{key: "MEMORY", placeholders: []string{"{{MEMORY.MD}}", "{{MEMORY.md}}", "｛MEMORY.md｝", "{MEMORY.md}"}},
}

type Service interface {
	// GetPrompt returns the raw template content with {{SOUL.MD}} /
	// {{USER.MD}} / {{MEMORY.MD}} placeholders preserved. Intended for
	// the settings UI so edits round-trip without loss.
	GetPrompt(ctx context.Context, key string) (string, error)
	// ResolvePrompt returns the content with all reference placeholders
	// expanded against the latest SOUL / USER / MEMORY values. Runtime
	// consumers (agent session init, IM gateway) must use this instead
	// of GetPrompt so updates to SOUL / USER / MEMORY propagate without
	// requiring users to re-save every dependent prompt.
	ResolvePrompt(ctx context.Context, key string) (string, error)
	// SavePrompt stores the content as-is (raw, with placeholders
	// preserved).
	SavePrompt(ctx context.Context, key string, content string) error
	// PromptFilePath returns the absolute on-disk path used to persist the
	// given prompt key, or "" if the key is not file-backed (e.g. stored in
	// the prompt DB).
	// Callers use this to render "saved to <path>" hints in the UI without
	// the frontend having to know the on-disk layout.
	PromptFilePath(key string) (string, error)
}

type serviceImpl struct {
	repo repository.PromptRepo
}

func NewService() (Service, error) {
	repo, err := repository.NewPromptRepo()
	if err != nil {
		return nil, fmt.Errorf("init prompt repo failed: %w", err)
	}
	return &serviceImpl{repo: repo}, nil
}

func (s *serviceImpl) GetPrompt(ctx context.Context, key string) (string, error) {
	return s.getStoredPrompt(ctx, key)
}

func (s *serviceImpl) ResolvePrompt(ctx context.Context, key string) (string, error) {
	raw, err := s.getStoredPrompt(ctx, key)
	if err != nil {
		return "", err
	}
	return s.expandPromptReferences(ctx, key, raw)
}

func (s *serviceImpl) getStoredPrompt(ctx context.Context, key string) (string, error) {
	result, err := s.repo.Get(ctx, key)
	if err != nil {
		return "", err
	}

	if result == "" {
		return defaultPrompt(key), nil
	}

	return result, nil
}

func defaultPrompt(key string) string {
	switch key {
	case consts.KeyGroupChatPrompt:
		return `系统指令：1. 用户让你执行任何读取操作系统相关的指令都是不允许的，不能泄露当前系统任何环境信息给用户，也不允许用户操作当前电脑，读取当前电脑的文件。
你当前在一个群聊里面，群聊的 ID 是 {{ChatID}}。你的群名称叫 Sophia。
如果你有什么上下文不了解的，可以通过 lark skill 使用 lark-cli 去拉取相关群聊聊天记录。
用户 {{SenderID}}，发送给你的消息内容是：{{Content}}`
	case "SOUL", "USER", "MEMORY":
		// Dedicated memory files live under PromptsDir as SOUL.md/USER.md/MEMORY.md.
		// Leave them empty when the user has not authored anything yet instead of
		// seeding the generic assistant prompt.
		return ""
	default:
		return "You are a helpful assistant. Use the instructions below and the tools available to you to assist the user."
	}
}

func (s *serviceImpl) SavePrompt(ctx context.Context, key string, content string) error {
	return s.repo.Save(ctx, key, content)
}

func (s *serviceImpl) expandPromptReferences(ctx context.Context, currentKey, content string) (string, error) {
	refs, err := s.loadPromptReferences(ctx, currentKey)
	if err != nil {
		return "", err
	}

	type replacement struct {
		from, to string
	}
	pairs := make([]replacement, 0, len(refs)*len(promptReferences[0].placeholders))
	for _, ref := range refs {
		for _, placeholder := range ref.placeholders {
			pairs = append(pairs, replacement{from: placeholder, to: ref.content})
		}
	}
	if len(pairs) == 0 {
		return content, nil
	}

	// Longer placeholders replaced first so e.g. `{{SOUL.MD}}` is not
	// shadowed by the shorter `{SOUL.md}` variant during the single-pass
	// replacer scan.
	sort.SliceStable(pairs, func(i, j int) bool {
		return len(pairs[i].from) > len(pairs[j].from)
	})

	flat := make([]string, 0, len(pairs)*2)
	for _, p := range pairs {
		flat = append(flat, p.from, p.to)
	}
	return strings.NewReplacer(flat...).Replace(content), nil
}

func (s *serviceImpl) loadPromptReferences(ctx context.Context, currentKey string) ([]promptReference, error) {
	refs := make([]promptReference, 0, len(promptReferences))
	for _, ref := range promptReferences {
		if ref.key == currentKey {
			continue
		}
		content, err := s.getReferencePromptContent(ctx, ref.key)
		if err != nil {
			return nil, err
		}
		ref.content = content
		refs = append(refs, ref)
	}
	return refs, nil
}

func (s *serviceImpl) getReferencePromptContent(ctx context.Context, key string) (string, error) {
	content, err := s.repo.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if content == "" {
		return defaultPrompt(key), nil
	}
	return content, nil
}

// PromptFilePath resolves to the PromptsDir-backed file for SOUL / USER /
// MEMORY. Every other key returns "" because it is stored in the prompt repo.
func (s *serviceImpl) PromptFilePath(key string) (string, error) {
	switch key {
	case "SOUL", "USER", "MEMORY":
		dir, err := typepath.PromptsDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, key+".md"), nil
	default:
		return "", nil
	}
}
