package prompt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/consts"
	typepath "github.com/fanlv/quartet/types/path"
)

// homeAgentsMDKey is the prompt key for the "AGENTS.md (ACP)" tab.
// The raw template (with {{SOUL.MD}} / {{USER.MD}} / {{MEMORY.MD}}
// placeholders preserved) lives in the prompt repo so the settings UI
// can round-trip edits without the lossy content-based collapse that
// the previous mirror-to-file design required. An *expanded* copy is
// mirrored into the default workspace's workdir as AGENTS.md /
// CLAUDE.md so Claude CLI and ACP-aware tooling can pick it up.
const homeAgentsMDKey = "home_agents_md"

const (
	agentsFileName = "AGENTS.md"
	claudeFileName = "CLAUDE.md"
	// Workspace-local override filenames used by the chat page editor.
	// The home prompt reader can still fall back to them when AGENTS.md /
	// CLAUDE.md have not been created yet, but writes must never delete
	// them because they are an active, user-visible feature.
	localAgentsFileName = "AGENTS.local.md"
	localClaudeFileName = "CLAUDE.local.md"
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
	// preserved). For homeAgentsMDKey it additionally mirrors an
	// expanded copy to {workdir}/AGENTS.md and {workdir}/CLAUDE.md so
	// external tools can consume a flat prompt.
	SavePrompt(ctx context.Context, key string, content string) error
	// PromptFilePath returns the absolute on-disk path used to persist the
	// given prompt key, or "" if the key is not file-backed (e.g. stored in
	// the prompt DB, or derived from the workspace workdir at call time).
	// Callers use this to render "saved to <path>" hints in the UI without
	// the frontend having to know the on-disk layout.
	PromptFilePath(key string) (string, error)
}

type serviceImpl struct {
	repo      repository.PromptRepo
	wsService workspace.Service
}

func NewService(wsService workspace.Service) (Service, error) {
	repo, err := repository.NewPromptRepo()
	if err != nil {
		return nil, fmt.Errorf("init prompt repo failed: %w", err)
	}
	return &serviceImpl{repo: repo, wsService: wsService}, nil
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
	if key == homeAgentsMDKey {
		// Raw template lives in the prompt repo. Fall back to reading
		// the mirrored AGENTS.md / AGENTS.local.md file only when the
		// repo has no entry yet, so a user who migrated from the
		// previous (file-only) scheme or who edited AGENTS.md by hand
		// still sees *something* on first settings-page open.
		result, err := s.repo.Get(ctx, key)
		if err != nil {
			return "", err
		}
		if result != "" {
			return result, nil
		}
		fileContent, err := s.readHomeAgentsMDFile()
		if err != nil {
			return "", err
		}
		if fileContent != "" {
			return fileContent, nil
		}
		return defaultPrompt(key), nil
	}

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
	if err := s.repo.Save(ctx, key, content); err != nil {
		return err
	}
	if key == homeAgentsMDKey {
		// Mirror the *expanded* content to AGENTS.md / CLAUDE.md so
		// external tooling that cannot parse our placeholders still
		// sees a flat, up-to-date prompt. Failures here must not leave
		// the prompt repo and the mirror out of sync for the caller —
		// surface the error so the UI can retry.
		expanded, err := s.expandPromptReferences(ctx, key, content)
		if err != nil {
			return err
		}
		return s.writeHomeAgentsMDFile(expanded)
	}
	if isReferenceKey(key) {
		// Saving SOUL / USER / MEMORY changes the substitution for the
		// {{SOUL.MD}} / {{USER.MD}} / {{MEMORY.MD}} placeholders inside
		// the home AGENTS.md template, making the on-disk AGENTS.md /
		// CLAUDE.md mirror stale. Re-expand and rewrite so Claude CLI /
		// ACP tooling sees the updated content without requiring the
		// user to re-save the AGENTS.md (ACP) tab.
		return s.refreshHomeAgentsMDMirror(ctx)
	}
	return nil
}

// refreshHomeAgentsMDMirror re-expands the stored home AGENTS.md template
// against the latest SOUL / USER / MEMORY values and rewrites the workspace
// mirror. It is a no-op when the template has never been saved to the repo
// so that editing a memory file does not materialize AGENTS.md / CLAUDE.md
// out of thin air.
func (s *serviceImpl) refreshHomeAgentsMDMirror(ctx context.Context) error {
	raw, err := s.repo.Get(ctx, homeAgentsMDKey)
	if err != nil {
		return err
	}
	if raw == "" {
		return nil
	}
	expanded, err := s.expandPromptReferences(ctx, homeAgentsMDKey, raw)
	if err != nil {
		return err
	}
	return s.writeHomeAgentsMDFile(expanded)
}

// isReferenceKey reports whether key is one of the SOUL / USER / MEMORY
// memory files whose content is substituted into placeholders in other
// prompts.
func isReferenceKey(key string) bool {
	for _, ref := range promptReferences {
		if ref.key == key {
			return true
		}
	}
	return false
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
// MEMORY. Every other key returns "" — either it lives in the prompt
// repo (no single file to reveal to the UI) or its location is derived
// from a workspace workdir that the UI already knows how to compute.
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

// defaultWorkspaceWorkdir returns the Workdir of the default workspace. The
// "AGENTS.md (ACP)" prompt mirror is intentionally placed alongside that
// workdir so its AGENTS.md / CLAUDE.md files sit next to the user's work,
// rather than in $HOME.
func (s *serviceImpl) defaultWorkspaceWorkdir() (string, error) {
	if s.wsService == nil {
		return "", fmt.Errorf("workspace service is not configured")
	}
	ws, ok := s.wsService.Get(consts.DefaultWorkspaceID)
	if !ok || ws == nil {
		return "", fmt.Errorf("default workspace %q not found", consts.DefaultWorkspaceID)
	}
	if ws.Workdir == "" {
		return "", fmt.Errorf("default workspace %q has empty workdir", consts.DefaultWorkspaceID)
	}
	return ws.Workdir, nil
}

func (s *serviceImpl) readHomeAgentsMDFile() (string, error) {
	workdir, err := s.defaultWorkspaceWorkdir()
	if err != nil {
		return "", err
	}
	sb := fileserver.GetFileManager()
	// Settings page edits the global project prompt (AGENTS.md / CLAUDE.md).
	// Workspace-local overrides (AGENTS.local.md / CLAUDE.local.md) are owned by
	// the chat page editor and must NOT shadow the settings page view; otherwise
	// users can get stuck in a "saved but didn't change" loop.
	//
	// Still fall back to AGENTS.local.md when AGENTS.md does not exist so users
	// migrating from the local-only workflow see *something* instead of an empty
	// editor.
	for _, name := range []string{agentsFileName, localAgentsFileName} {
		filePath := filepath.Join(workdir, name)
		data, err := sb.FileRead(&fsmodel.FileReadRequest{File: filePath})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("read %s failed: %w", filePath, err)
		}
		return data.Content, nil
	}
	return "", nil
}

func (s *serviceImpl) writeHomeAgentsMDFile(content string) error {
	workdir, err := s.defaultWorkspaceWorkdir()
	if err != nil {
		return err
	}
	sb := fileserver.GetFileManager()
	names := []string{agentsFileName, claudeFileName}
	// Snapshot prior contents so a mid-write failure on the second file can
	// roll back the first — FileWrite is atomic per file, but the pair of
	// files must stay in sync or readers see a split-brain AGENTS.md /
	// CLAUDE.md.
	prior := make(map[string]*string, len(names))
	for _, name := range names {
		target := filepath.Join(workdir, name)
		res, readErr := sb.FileRead(&fsmodel.FileReadRequest{File: target})
		if readErr != nil {
			// Missing file is not an error — record "absent" so rollback
			// knows to delete rather than restore.
			prior[name] = nil
			continue
		}
		c := res.Content
		prior[name] = &c
	}

	written := make([]string, 0, len(names))
	for _, name := range names {
		target := filepath.Join(workdir, name)
		if err := sb.FileWrite(&fsmodel.FileWriteRequest{
			File:    target,
			Content: content,
			Atomic:  true,
		}); err != nil {
			// Best-effort rollback of already-written siblings so readers
			// do not see a partially updated pair.
			for _, done := range written {
				rollbackPath := filepath.Join(workdir, done)
				if prev := prior[done]; prev == nil {
					_ = sb.FileDelete(&fsmodel.FileDeleteRequest{Path: rollbackPath})
				} else {
					_ = sb.FileWrite(&fsmodel.FileWriteRequest{
						File:    rollbackPath,
						Content: *prev,
						Atomic:  true,
					})
				}
			}
			return fmt.Errorf("write %s failed: %w", target, err)
		}
		written = append(written, name)
	}
	// Intentionally leave AGENTS.local.md / CLAUDE.local.md untouched:
	// the chat page uses them as workspace-local overrides, so deleting
	// them here would silently discard user-authored local prompt state.
	return nil
}
