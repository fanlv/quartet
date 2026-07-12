package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fanlv/quartet/services/memorylayout"
	"github.com/fanlv/quartet/types/model"
)

const canonicalMemoryRoot = "/home/fanlv/memory"

type operation struct {
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type migrationReport struct {
	BatchID         string            `json:"batchId"`
	Root            string            `json:"root"`
	Mode            string            `json:"mode"`
	StartedAt       time.Time         `json:"startedAt"`
	FinishedAt      time.Time         `json:"finishedAt"`
	BeforeInventory *inventorySummary `json:"beforeInventory,omitempty"`
	AfterInventory  *inventorySummary `json:"afterInventory,omitempty"`
	Operations      []operation       `json:"operations"`
	Error           string            `json:"error,omitempty"`
}

type inventorySummary struct {
	Files         int64  `json:"files"`
	Directories   int64  `json:"directories"`
	Symlinks      int64  `json:"symlinks"`
	Bytes         int64  `json:"bytes"`
	ContentSHA256 string `json:"contentSha256"`
}

type moveSpec struct {
	sourceRel string
	targetRel string
	exclude   map[string]bool
}

type migrator struct {
	root       string
	batchID    string
	reportPath string
	startedAt  time.Time
	operations []operation
	referenced map[string]bool
	before     *inventorySummary
	after      *inventorySummary
}

func newMigrator(root, batchID, reportPath string) (*migrator, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("Memory root must be absolute: %s", root)
	}
	if !filepath.IsAbs(reportPath) {
		return nil, fmt.Errorf("migration report path must be absolute: %s", reportPath)
	}
	if pathWithin(reportPath, root) {
		return nil, fmt.Errorf("migration report must be outside Memory root: root=%s report=%s", root, reportPath)
	}
	return &migrator{
		root:       filepath.Clean(root),
		batchID:    batchID,
		reportPath: filepath.Clean(reportPath),
		startedAt:  time.Now(),
	}, nil
}

func (m *migrator) plan() error {
	m.operations = nil
	if err := m.captureBeforeInventory(); err != nil {
		return err
	}
	if err := m.preflight(); err != nil {
		return err
	}
	after := *m.before
	m.after = &after
	m.record("PLAN", "", "", "ready", "all source mappings and target conflicts checked")
	return nil
}

func (m *migrator) execute() (retErr error) {
	m.operations = nil
	if err := m.captureBeforeInventory(); err != nil {
		return err
	}
	if err := m.validateLayout(); err == nil {
		if err := m.captureAfterInventory(); err != nil {
			return err
		}
		m.record("MIGRATION", "", "", "already-complete", "layout already passes validation")
		return nil
	}
	if err := m.preflight(); err != nil {
		return err
	}

	if err := m.writeLayoutManifest("migrating"); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			if err := m.writeLayoutManifest("failed"); err != nil {
				retErr = fmt.Errorf("%v\nwrite failed migration marker also failed: %v", retErr, err)
			}
		}
	}()

	if err := m.ensureFinalDirectories(); err != nil {
		return err
	}
	if err := m.archiveKnowledgeDrafts(true); err != nil {
		return err
	}
	if err := m.splitSchedules(true); err != nil {
		return err
	}
	for _, spec := range m.moveSpecs() {
		if err := m.move(spec, true); err != nil {
			return err
		}
	}
	if err := m.moveUploads(true); err != nil {
		return err
	}
	if err := m.removeRebuildableContent(true); err != nil {
		return err
	}
	if err := m.removeGitkeepFiles(true); err != nil {
		return err
	}
	if err := m.writeMemoryReadme(); err != nil {
		return err
	}
	if err := m.writeMemoryGitignore(); err != nil {
		return err
	}
	if err := m.rewriteAutomationLayout(); err != nil {
		return err
	}
	if err := m.rewriteActiveReferences(); err != nil {
		return err
	}
	if err := m.cleanupLegacyEntries(); err != nil {
		return err
	}
	if err := m.ensureFinalDirectories(); err != nil {
		return err
	}
	if err := m.assertNoLegacyEntries(); err != nil {
		return err
	}
	if err := m.writeLayoutManifest(model.MemoryLayoutStatusComplete); err != nil {
		return err
	}
	if err := m.validateLayout(); err != nil {
		return fmt.Errorf("post-migration validation failed: %w", err)
	}
	if err := m.captureAfterInventory(); err != nil {
		return err
	}
	m.record("MIGRATION", "", "", "complete", "layout activated and validated")
	return nil
}

func (m *migrator) verifyMode() error {
	m.operations = nil
	if err := m.captureBeforeInventory(); err != nil {
		return err
	}
	if err := m.validateLayout(); err != nil {
		return err
	}
	return m.captureAfterInventory()
}

func (m *migrator) validateLayout() error {
	previous, hadPrevious := os.LookupEnv("LOCAL_MEMORY")
	if err := os.Setenv("LOCAL_MEMORY", m.root); err != nil {
		return fmt.Errorf("set LOCAL_MEMORY for validation failed: %w", err)
	}
	defer func() {
		if hadPrevious {
			_ = os.Setenv("LOCAL_MEMORY", previous)
		} else {
			_ = os.Unsetenv("LOCAL_MEMORY")
		}
	}()
	service, err := memorylayout.NewService()
	if err != nil {
		return err
	}
	if err := service.Validate(context.Background()); err != nil {
		return err
	}
	m.record("VERIFY", m.root, "", "ok", "version, completion marker, directories, data files and legacy entries validated")
	return nil
}

func (m *migrator) captureBeforeInventory() error {
	summary, err := inventoryTree(m.root)
	if err != nil {
		return fmt.Errorf("create pre-migration content inventory for %s failed: %w", m.root, err)
	}
	m.before = summary
	m.record("INVENTORY", m.root, "", "before", fmt.Sprintf(
		"files=%d directories=%d symlinks=%d bytes=%d sha256=%s",
		summary.Files, summary.Directories, summary.Symlinks, summary.Bytes, summary.ContentSHA256,
	))
	return nil
}

func (m *migrator) captureAfterInventory() error {
	summary, err := inventoryTree(m.root)
	if err != nil {
		return fmt.Errorf("create post-migration content inventory for %s failed: %w", m.root, err)
	}
	m.after = summary
	m.record("INVENTORY", m.root, "", "after", fmt.Sprintf(
		"files=%d directories=%d symlinks=%d bytes=%d sha256=%s",
		summary.Files, summary.Directories, summary.Symlinks, summary.Bytes, summary.ContentSHA256,
	))
	return nil
}

func inventoryTree(root string) (*inventorySummary, error) {
	hash := sha256.New()
	summary := &inventorySummary{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		logicalSize := int64(0)
		if info.Mode().IsRegular() {
			logicalSize = info.Size()
		}
		// Directory inode sizes are filesystem-specific and are not part of
		// the logical tree content, so only regular-file sizes enter the digest.
		if _, err := fmt.Fprintf(hash, "%s\x00%s\x00%d\x00", filepath.ToSlash(relative), info.Mode().String(), logicalSize); err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			summary.Symlinks++
			if _, err := io.WriteString(hash, target); err != nil {
				return err
			}
		case info.IsDir():
			summary.Directories++
		case info.Mode().IsRegular():
			summary.Files++
			summary.Bytes += info.Size()
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported filesystem entry in inventory: %s (%s)", path, info.Mode())
		}
		if _, err := hash.Write([]byte{0}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	summary.ContentSHA256 = hex.EncodeToString(hash.Sum(nil))
	return summary, nil
}

func (m *migrator) preflight() error {
	info, err := os.Stat(m.root)
	if err != nil {
		return fmt.Errorf("inspect Memory root %s failed: %w", m.root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Memory root is not a directory: %s", m.root)
	}
	refs, err := m.collectReferencedMedia()
	if err != nil {
		return err
	}
	m.referenced = refs
	if err := m.preflightClassifiedEntries(); err != nil {
		return err
	}

	var conflicts []string
	if err := m.archiveKnowledgeDrafts(false); err != nil {
		conflicts = append(conflicts, err.Error())
	}
	if err := m.splitSchedules(false); err != nil {
		conflicts = append(conflicts, err.Error())
	}
	for _, spec := range m.moveSpecs() {
		if err := m.move(spec, false); err != nil {
			conflicts = append(conflicts, err.Error())
		}
	}
	if err := m.moveUploads(false); err != nil {
		conflicts = append(conflicts, err.Error())
	}
	if err := m.removeRebuildableContent(false); err != nil {
		conflicts = append(conflicts, err.Error())
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("memory migration preflight failed:\n- %s", strings.Join(conflicts, "\n- "))
	}
	return nil
}

func (m *migrator) moveSpecs() []moveSpec {
	return []moveSpec{
		{sourceRel: "agent/prompts", targetRel: "quartet/config/prompts"},
		{sourceRel: "agent/templates", targetRel: "quartet/config/templates"},
		{sourceRel: "agent/graph_workflows", targetRel: "quartet/config/graph-workflows"},
		{sourceRel: "agent/models.json", targetRel: "quartet/data/models.json"},
		{sourceRel: "agent/settings.json", targetRel: "quartet/data/settings.json"},
		{sourceRel: "agent/usage_stats", targetRel: "quartet/data/usage-stats"},
		{sourceRel: "agent/recent_dirs.json", targetRel: "var/quartet/state/recent-dirs.json"},
		{sourceRel: "agent/acp_probe_cache.json", targetRel: "var/quartet/cache/acp-probe.json"},
		{sourceRel: "agent/tmp/shell", targetRel: "var/quartet/tmp/shell"},
		{sourceRel: "workspaces", targetRel: "quartet/data/workspaces"},
		{sourceRel: "im", targetRel: "quartet/data/im"},
		{sourceRel: "user_input", targetRel: "quartet/data/user-input"},
		{sourceRel: "wechat", targetRel: "quartet/data/wechat"},
		{sourceRel: ".sandbox/compose", targetRel: "var/quartet/state/sandbox/compose"},
		{sourceRel: "qrcode_auth.png", targetRel: "archive/quartet/qrcode_auth.png"},

		{sourceRel: "fanlv/fanlv.md", targetRel: "personal/career/resume.md"},
		{sourceRel: "fanlv/team.md", targetRel: "personal/career/current-team.md"},
		{sourceRel: "fanlv/target.md", targetRel: "personal/planning/financial-freedom.md"},
		{sourceRel: "fanlv/finance/2005-2006", targetRel: "personal/finance/2025-2026"},
		{sourceRel: "fanlv/study/english", targetRel: "personal/learning/english"},
		{sourceRel: "fanlv/oc_bf0bd3f7175d48ba910b98750c6546f3.txt", targetRel: "datasets/work-chats/agent-group/messages.txt"},
		{sourceRel: "knowledge/career/interview", targetRel: "personal/career/interviews/2026"},
		{sourceRel: "knowledge/env-setup/docker_proxy_setup.md", targetRel: "personal/dev-environment/docker-proxy.md"},
		{sourceRel: "oncall", targetRel: "datasets/work-chats/oncall"},
		{sourceRel: "knowledge/articles-and-blogs/ai-blog/raw", targetRel: "datasets/web-archives/ai-blogs"},
		{sourceRel: "knowledge/articles-and-blogs/ai-blog", targetRel: "knowledge/articles/ai", exclude: map[string]bool{"raw": true}},
		{sourceRel: "knowledge/articles-and-blogs/read/web", targetRel: "knowledge/reading/web"},
		{sourceRel: "knowledge/articles-and-blogs/read/llm/talk.md", targetRel: "datasets/transcripts/talks/2026-07-08-lark-fireside-chat-88.md"},
		{sourceRel: "knowledge/ai-and-agents/claude-code", targetRel: "datasets/software-releases/claude-code"},
		{sourceRel: "knowledge/ai-and-agents/eino/wiki", targetRel: "datasets/documentation-mirrors/eino"},
		{sourceRel: "knowledge/ai-and-agents/manage-agents", targetRel: "knowledge/ai-and-agents/agent-architecture"},
		{sourceRel: "knowledge/ai-and-agents/agent-ui-protol", targetRel: "knowledge/ai-and-agents/agent-ui-protocol"},

		{sourceRel: "shell/lib", targetRel: "automation/shared"},
		{sourceRel: "shell/auto_commit_memory/auto_commit_memory.sh", targetRel: "automation/maintenance/memory-git-backup/auto_commit_memory.sh"},
		{sourceRel: "shell/auto_commit_memory/.last_run_date", targetRel: "var/automation/memory-git-backup/state/last-run-date"},
		{sourceRel: "shell/auto_commit_memory/auto_commit_memory.log", targetRel: "var/automation/memory-git-backup/logs/auto-commit.log"},
		{sourceRel: "shell/auto_update_claude_code_key/auto_update_claude_code_key.sh", targetRel: "automation/maintenance/claude-code-key-rotation/auto_update_claude_code_key.sh"},
		{sourceRel: "shell/auto_update_claude_code_key/.last_run_date", targetRel: "var/automation/claude-code-key-rotation/state/last-run-date"},
		{sourceRel: "shell/auto_update_claude_code_key/.last_force_switch_date", targetRel: "var/automation/claude-code-key-rotation/state/last-force-switch-date"},
		{sourceRel: "shell/auto_update_claude_code_key/.last_usage_snapshot", targetRel: "var/automation/claude-code-key-rotation/state/last-usage-snapshot"},
		{sourceRel: "shell/auto_update_claude_code_key/auto_update.log", targetRel: "var/automation/claude-code-key-rotation/logs/auto-update.log"},
		{sourceRel: "shell/claude-code/download-claude-code.sh", targetRel: "automation/collectors/claude-code-release/download-claude-code.sh"},
		{sourceRel: "shell/claude-code/write-download-claude-code-success-marker.sh", targetRel: "automation/collectors/claude-code-release/write-download-claude-code-success-marker.sh"},
		{sourceRel: "shell/claude-code/.data/download-claude-code.success", targetRel: "var/automation/claude-code-release/state/download-claude-code.success"},
		{sourceRel: "shell/get_oncall", targetRel: "automation/collectors/oncall"},
		{sourceRel: "shell/sites", targetRel: "automation/collectors/ai-sites"},
		{sourceRel: "shell/.data/anthropic-engineering-state.json", targetRel: "var/automation/ai-sites/state/anthropic-engineering-state.json"},
		{sourceRel: "shell/.data/cursor-blog-state.json", targetRel: "var/automation/ai-sites/state/cursor-blog-state.json"},
		{sourceRel: "shell/.data/follow-builders-podcasts-state.json", targetRel: "var/automation/ai-sites/state/follow-builders-podcasts-state.json"},
		{sourceRel: "shell/.data/follow-builders-x-state.json", targetRel: "var/automation/ai-sites/state/follow-builders-x-state.json"},
		{sourceRel: "shell/.data/langchain-blog-state.json", targetRel: "var/automation/ai-sites/state/langchain-blog-state.json"},
		{sourceRel: "shell/.data/manus-blog-state.json", targetRel: "var/automation/ai-sites/state/manus-blog-state.json"},
		{sourceRel: "shell/.data/openai-developers-blog-state.json", targetRel: "var/automation/ai-sites/state/openai-developers-blog-state.json"},
		{sourceRel: "shell/.data/download-claude-code.success", targetRel: "archive/automation/claude-code-release/download-claude-code-legacy.success"},
		{sourceRel: "shell/skills/article-poster", targetRel: "automation/skill-sync/article-poster"},
		{sourceRel: "shell/skills/deep-research", targetRel: "automation/skill-sync/deep-research"},
		{sourceRel: "shell/skills/insight-diagram", targetRel: "automation/skill-sync/insight-diagram"},
		{sourceRel: "shell/skills/skills_instal.sh", targetRel: "automation/skill-sync/install_skills.sh"},
		{sourceRel: "shell/skills/skills_instal.log", targetRel: "var/automation/skill-sync/logs/install-skills.log"},
		{sourceRel: "shell/xiaoyuzhou/scripts", targetRel: "automation/collectors/xiaoyuzhou/scripts"},
		{sourceRel: "shell/xiaoyuzhou/docs", targetRel: "automation/collectors/xiaoyuzhou/docs"},
		{sourceRel: "shell/xiaoyuzhou/README.md", targetRel: "automation/collectors/xiaoyuzhou/README.md"},
		{sourceRel: "shell/xiaoyuzhou/podcasts.txt", targetRel: "automation/collectors/xiaoyuzhou/podcasts.txt"},
		{sourceRel: "shell/xiaoyuzhou/output", targetRel: "datasets/podcasts/xiaoyuzhou"},
		{sourceRel: "shell/xiaoyuzhou/state/processed.json", targetRel: "var/automation/xiaoyuzhou/state/processed.json"},
		{sourceRel: "shell/youtube/scripts", targetRel: "automation/collectors/youtube/scripts"},
		{sourceRel: "shell/youtube/README.md", targetRel: "automation/collectors/youtube/README.md"},
		{sourceRel: "shell/youtube/videos.txt", targetRel: "automation/collectors/youtube/videos.txt"},
		{sourceRel: "shell/youtube/output", targetRel: "datasets/videos/youtube"},
		{sourceRel: "shell/youtube/state/processed.json", targetRel: "var/automation/youtube/state/processed.json"},

		{sourceRel: "tools/asr/app", targetRel: "apps/asr/app"},
		{sourceRel: "tools/asr/docs", targetRel: "apps/asr/docs"},
		{sourceRel: "tools/asr/README.md", targetRel: "apps/asr/README.md"},
		{sourceRel: "tools/asr/run.sh", targetRel: "apps/asr/run.sh"},
		{sourceRel: "tools/asr/.gitignore", targetRel: "apps/asr/.gitignore"},
		{sourceRel: "tools/asr/tmp", targetRel: "var/apps/asr/tmp"},
	}
}

func (m *migrator) preflightClassifiedEntries() error {
	var covered []string
	for _, spec := range m.moveSpecs() {
		covered = append(covered, filepath.Clean(filepath.FromSlash(spec.sourceRel)))
	}
	covered = append(covered,
		filepath.Clean("agent/schedules"),
		filepath.Clean("uploads"),
		filepath.Clean("shell/xiaoyuzhou/.venv"),
		filepath.Clean("shell/xiaoyuzhou/state/audio"),
		filepath.Clean("shell/youtube/.venv"),
		filepath.Clean("tools/asr/.venv"),
	)
	legacyRoots := []string{
		"agent", "workspaces", "im", "uploads", "user_input", "wechat", "shell",
		"tools", "fanlv", "oncall", ".sandbox", "bin", ".agents", ".codex", ".trae",
		"qrcode_auth.png", "knowledge/career", "knowledge/env-setup",
		"knowledge/articles-and-blogs", "knowledge/ai-and-agents/claude-code",
		"knowledge/ai-and-agents/eino/wiki", "knowledge/ai-and-agents/manage-agents",
		"knowledge/ai-and-agents/agent-ui-protol",
	}
	var unclassified []string
	for _, legacyRoot := range legacyRoots {
		absolute := filepath.Join(m.root, legacyRoot)
		if _, err := os.Lstat(absolute); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		err := filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Name() == ".gitkeep" {
				return nil
			}
			rel, err := filepath.Rel(m.root, path)
			if err != nil {
				return err
			}
			for _, prefix := range covered {
				if rel == prefix || pathWithin(rel, prefix) {
					return nil
				}
			}
			unclassified = append(unclassified, rel)
			return nil
		})
		if err != nil {
			return fmt.Errorf("classify legacy entries under %s failed: %w", absolute, err)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		return fmt.Errorf("unclassified legacy files block migration:\n- %s", strings.Join(unclassified, "\n- "))
	}
	m.record("CLASSIFICATION", m.root, "", "complete", "all legacy files have an explicit move, archive, or rebuild decision")
	return nil
}

func (m *migrator) move(spec moveSpec, execute bool) error {
	source := filepath.Join(m.root, filepath.FromSlash(spec.sourceRel))
	target := filepath.Join(m.root, filepath.FromSlash(spec.targetRel))
	if _, err := os.Lstat(source); err != nil {
		if os.IsNotExist(err) {
			if _, targetErr := os.Lstat(target); targetErr == nil {
				m.record("MOVE", source, target, "already-applied", "source absent and target exists")
			} else {
				m.record("MOVE", source, target, "not-present", "source absent")
			}
			return nil
		}
		return fmt.Errorf("inspect migration source %s failed: %w", source, err)
	}
	if err := m.preflightMoveTree(source, target, spec.exclude, ""); err != nil {
		return err
	}
	if !execute {
		m.record("MOVE", source, target, "planned", "conflict check passed")
		return nil
	}
	if err := m.moveTree(source, target, spec.exclude, ""); err != nil {
		return err
	}
	m.record("MOVE", source, target, "moved", "content and metadata preserved")
	return nil
}

func (m *migrator) preflightMoveTree(source, target string, exclude map[string]bool, relative string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if relative != "" {
		top := strings.Split(filepath.ToSlash(relative), "/")[0]
		if exclude[top] {
			return nil
		}
	}
	targetInfo, targetErr := os.Lstat(target)
	if targetErr != nil && !os.IsNotExist(targetErr) {
		return fmt.Errorf("inspect migration target %s failed: %w", target, targetErr)
	}
	if !info.IsDir() {
		if targetErr == nil {
			equal, compareErr := sameEntry(source, target)
			if compareErr != nil {
				return compareErr
			}
			if !equal {
				return fmt.Errorf("migration target conflict: source=%s target=%s", source, target)
			}
		}
		return nil
	}
	if targetErr == nil && !targetInfo.IsDir() {
		return fmt.Errorf("migration target conflict: source directory=%s target is not directory=%s", source, target)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("list migration source %s failed: %w", source, err)
	}
	for _, entry := range entries {
		childRel := entry.Name()
		if relative != "" {
			childRel = filepath.Join(relative, entry.Name())
		}
		if err := m.preflightMoveTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name()), exclude, childRel); err != nil {
			return err
		}
	}
	return nil
}

func (m *migrator) moveTree(source, target string, exclude map[string]bool, relative string) error {
	info, err := os.Lstat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if relative != "" {
		top := strings.Split(filepath.ToSlash(relative), "/")[0]
		if exclude[top] {
			return nil
		}
	}
	if !info.IsDir() {
		if targetInfo, targetErr := os.Lstat(target); targetErr == nil {
			if targetInfo.IsDir() {
				return fmt.Errorf("migration target conflict: source file=%s target directory=%s", source, target)
			}
			equal, compareErr := sameEntry(source, target)
			if compareErr != nil {
				return compareErr
			}
			if !equal {
				return fmt.Errorf("migration target conflict: source=%s target=%s", source, target)
			}
			return os.Remove(source)
		} else if !os.IsNotExist(targetErr) {
			return targetErr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create migration target parent %s failed: %w", filepath.Dir(target), err)
		}
		if err := os.Rename(source, target); err != nil {
			return fmt.Errorf("move %s to %s failed: %w", source, target, err)
		}
		return nil
	}

	if len(exclude) == 0 {
		if _, targetErr := os.Lstat(target); os.IsNotExist(targetErr) {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Rename(source, target); err == nil {
				return nil
			}
		}
	}
	if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
		return fmt.Errorf("create migration target directory %s failed: %w", target, err)
	}
	if err := os.Chmod(target, info.Mode().Perm()); err != nil {
		return fmt.Errorf("preserve directory mode on %s failed: %w", target, err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		childRel := entry.Name()
		if relative != "" {
			childRel = filepath.Join(relative, entry.Name())
		}
		if err := m.moveTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name()), exclude, childRel); err != nil {
			return err
		}
	}
	entries, err = os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("list migrated source %s failed: %w", source, err)
	}
	if timeErr := os.Chtimes(target, info.ModTime(), info.ModTime()); timeErr != nil {
		return fmt.Errorf("preserve directory timestamp on %s failed: %w", target, timeErr)
	}
	if len(entries) == 0 {
		if err := os.Remove(source); err != nil {
			return fmt.Errorf("remove empty migration source %s failed: %w", source, err)
		}
	}
	return nil
}

func sameEntry(a, b string) (bool, error) {
	ai, err := os.Lstat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Lstat(b)
	if err != nil {
		return false, err
	}
	if ai.Mode()&os.ModeSymlink != 0 || bi.Mode()&os.ModeSymlink != 0 {
		if ai.Mode()&os.ModeSymlink == 0 || bi.Mode()&os.ModeSymlink == 0 {
			return false, nil
		}
		at, err := os.Readlink(a)
		if err != nil {
			return false, err
		}
		bt, err := os.Readlink(b)
		return at == bt, err
	}
	if ai.IsDir() || bi.IsDir() {
		return ai.IsDir() && bi.IsDir(), nil
	}
	if ai.Size() != bi.Size() {
		return false, nil
	}
	ah, err := hashFile(a)
	if err != nil {
		return false, err
	}
	bh, err := hashFile(b)
	return ah == bh, err
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m *migrator) splitSchedules(execute bool) error {
	sourceDir := filepath.Join(m.root, "agent", "schedules")
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			m.record("SCHEDULE_SPLIT", sourceDir, "", "not-present", "legacy schedule directory absent")
			return nil
		}
		return fmt.Errorf("list legacy schedules %s failed: %w", sourceDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return fmt.Errorf("unexpected directory in legacy schedules: %s", filepath.Join(sourceDir, entry.Name()))
		}
		source := filepath.Join(sourceDir, entry.Name())
		switch filepath.Ext(entry.Name()) {
		case ".claim":
			target := filepath.Join(m.root, "archive", "quartet", "legacy-schedule-claims", entry.Name())
			if err := m.preflightMoveTree(source, target, nil, ""); err != nil {
				return err
			}
			if execute {
				if err := m.moveTree(source, target, nil, ""); err != nil {
					return err
				}
			}
			status := "planned"
			if execute {
				status = "archived"
			}
			m.record("ARCHIVE_CLAIM", source, target, status, "legacy claim is no longer read by Quartet")
		case ".json":
			sourceInfo, err := entry.Info()
			if err != nil {
				return fmt.Errorf("inspect legacy schedule %s failed: %w", source, err)
			}
			data, err := os.ReadFile(source)
			if err != nil {
				return fmt.Errorf("read legacy schedule %s failed: %w", source, err)
			}
			var task model.ScheduledTask
			if err := json.Unmarshal(data, &task); err != nil {
				return fmt.Errorf("parse legacy schedule %s failed: %w", source, err)
			}
			fileID := strings.TrimSuffix(entry.Name(), ".json")
			if task.ID == "" || task.ID != fileID {
				return fmt.Errorf("legacy schedule ID mismatch in %s: file=%s content=%s", source, fileID, task.ID)
			}
			task.Workdir = m.replacePaths(task.Workdir)
			definition := model.ScheduleDefinition{
				ID:              task.ID,
				Name:            task.Name,
				Enabled:         task.Enabled,
				CronExpr:        task.CronExpr,
				CreatedAt:       task.CreatedAt,
				UpdatedAt:       task.UpdatedAt,
				GraphWorkflowID: task.GraphWorkflowID,
				WorkspaceID:     task.WorkspaceID,
				Workdir:         task.Workdir,
				MaxConcurrent:   task.MaxConcurrent,
				Timeout:         task.Timeout,
			}
			state := model.ScheduleState{
				ID:               task.ID,
				LastRunAt:        task.LastRunAt,
				LastRunJobID:     task.LastRunJobID,
				LastStatus:       task.LastStatus,
				LastTriggerError: task.LastTriggerError,
				NextRunAt:        task.NextRunAt,
				RunCount:         task.RunCount,
				UpdatedAt:        task.UpdatedAt,
			}
			definitionPath := filepath.Join(m.root, "quartet", "config", "schedules", entry.Name())
			statePath := filepath.Join(m.root, "var", "quartet", "state", "schedules", entry.Name())
			definitionData, _ := json.MarshalIndent(definition, "", "  ")
			stateData, _ := json.MarshalIndent(state, "", "  ")
			if err := preflightGeneratedFile(definitionPath, append(definitionData, '\n')); err != nil {
				return err
			}
			if err := preflightGeneratedFile(statePath, append(stateData, '\n')); err != nil {
				return err
			}
			if execute {
				if err := atomicWrite(definitionPath, append(definitionData, '\n'), 0o644); err != nil {
					return err
				}
				if err := atomicWrite(statePath, append(stateData, '\n'), 0o644); err != nil {
					return err
				}
				for _, generatedPath := range []string{definitionPath, statePath} {
					if err := os.Chtimes(generatedPath, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
						return fmt.Errorf("preserve schedule timestamp on %s failed: %w", generatedPath, err)
					}
				}
				if err := os.Remove(source); err != nil {
					return fmt.Errorf("remove migrated legacy schedule %s failed: %w", source, err)
				}
			}
			status := "planned"
			if execute {
				status = "split"
			}
			m.record("SCHEDULE_SPLIT", source, definitionPath, status, "runtime state target="+statePath)
		default:
			return fmt.Errorf("unclassified file in legacy schedule directory: %s", source)
		}
	}
	return nil
}

func preflightGeneratedFile(path string, expected []byte) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read generated target %s failed: %w", path, err)
	}
	if !bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace(expected)) {
		return fmt.Errorf("generated target conflict: %s", path)
	}
	return nil
}

func (m *migrator) collectReferencedMedia() (map[string]bool, error) {
	mediaDir := filepath.Join(m.root, "uploads", "im-media")
	entries, err := os.ReadDir(mediaDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("list legacy IM media %s failed: %w", mediaDir, err)
	}
	all := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("unexpected directory in legacy IM media: %s", filepath.Join(mediaDir, entry.Name()))
		}
		all[entry.Name()] = false
	}
	if len(all) == 0 {
		return all, nil
	}

	err = filepath.WalkDir(m.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			base := entry.Name()
			if path == mediaDir || base == ".git" || base == ".venv" || base == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 128<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for name := range all {
			if bytes.Contains(data, []byte(name)) {
				all[name] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan persisted IM media references failed: %w", err)
	}
	return all, nil
}

func (m *migrator) moveUploads(execute bool) error {
	sourceDir := filepath.Join(m.root, "uploads")
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			m.record("UPLOADS", sourceDir, "", "not-present", "legacy uploads directory absent")
			return nil
		}
		return err
	}
	targetDir := filepath.Join(m.root, "quartet", "data", "uploads")
	for _, entry := range entries {
		source := filepath.Join(sourceDir, entry.Name())
		if entry.Name() != "im-media" {
			target := filepath.Join(targetDir, entry.Name())
			if err := m.preflightMoveTree(source, target, nil, ""); err != nil {
				return err
			}
			if execute {
				if err := m.moveTree(source, target, nil, ""); err != nil {
					return err
				}
			}
			continue
		}
		mediaEntries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, mediaEntry := range mediaEntries {
			mediaSource := filepath.Join(source, mediaEntry.Name())
			if m.referenced[mediaEntry.Name()] {
				mediaTarget := filepath.Join(targetDir, "im-media", mediaEntry.Name())
				if err := m.preflightMoveTree(mediaSource, mediaTarget, nil, ""); err != nil {
					return err
				}
				if execute {
					if err := m.moveTree(mediaSource, mediaTarget, nil, ""); err != nil {
						return err
					}
				}
				status := "planned"
				if execute {
					status = "persisted"
				}
				m.record("IM_MEDIA", mediaSource, mediaTarget, status, "referenced by a durable record")
			} else {
				status := "planned-delete"
				if execute {
					if err := os.Remove(mediaSource); err != nil {
						return fmt.Errorf("remove unreferenced IM cache file %s failed: %w", mediaSource, err)
					}
					status = "deleted"
				}
				m.record("IM_MEDIA_CACHE", mediaSource, "", status, "not referenced by any persisted record")
			}
		}
	}
	return nil
}

func (m *migrator) archiveKnowledgeDrafts(execute bool) error {
	knowledgeRoot := filepath.Join(m.root, "knowledge")
	var drafts []string
	err := filepath.WalkDir(knowledgeRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		if strings.HasSuffix(stem, " copy") || strings.HasSuffix(name, ".bak") ||
			strings.Contains(name, ".before-") || strings.HasSuffix(name, ".~1~") {
			drafts = append(drafts, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("scan knowledge drafts failed: %w", err)
	}
	sort.Strings(drafts)
	for _, source := range drafts {
		relative, _ := filepath.Rel(knowledgeRoot, source)
		target := filepath.Join(m.root, "archive", "knowledge-drafts", relative)
		if err := m.preflightMoveTree(source, target, nil, ""); err != nil {
			return err
		}
		if execute {
			if err := m.moveTree(source, target, nil, ""); err != nil {
				return err
			}
		}
		status := "planned"
		if execute {
			status = "archived"
		}
		m.record("KNOWLEDGE_DRAFT", source, target, status, "original relative path retained")
	}
	return nil
}

func (m *migrator) removeRebuildableContent(execute bool) error {
	paths := []string{
		"shell/xiaoyuzhou/.venv",
		"shell/xiaoyuzhou/state/audio",
		"shell/youtube/.venv",
		"tools/asr/.venv",
	}
	scanErr := filepath.WalkDir(m.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == "__pycache__" {
			paths = append(paths, path)
			return filepath.SkipDir
		}
		if entry.IsDir() && entry.Name() == ".venv" {
			return filepath.SkipDir
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		return nil
	})
	if scanErr != nil {
		return fmt.Errorf("scan rebuildable Python caches failed: %w", scanErr)
	}
	for _, rel := range paths {
		path := rel
		if !filepath.IsAbs(path) {
			path = filepath.Join(m.root, filepath.FromSlash(rel))
		}
		if _, err := os.Lstat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		status := "planned-delete"
		if execute {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove rebuildable content %s failed: %w", path, err)
			}
			status = "deleted"
		}
		m.record("REBUILDABLE", path, "", status, "virtual environment or disposable cache will be rebuilt")
	}
	return nil
}

func (m *migrator) removeGitkeepFiles(execute bool) error {
	var files []string
	err := filepath.WalkDir(m.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.IsDir() && entry.Name() == ".gitkeep" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range files {
		if execute {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		status := "planned-delete"
		if execute {
			status = "deleted"
		}
		m.record("GITKEEP", path, "", status, "content directory no longer needs a placeholder")
	}
	return nil
}

func (m *migrator) ensureFinalDirectories() error {
	dirs := []string{
		"quartet/config/prompts",
		"quartet/config/templates",
		"quartet/config/graph-workflows",
		"quartet/config/schedules",
		"quartet/data/usage-stats",
		"quartet/data/workspaces",
		"quartet/data/im",
		"quartet/data/uploads/im-media",
		"quartet/data/user-input",
		"quartet/data/wechat/accounts",
		"knowledge/ai-and-agents",
		"knowledge/articles/ai",
		"knowledge/reading/web",
		"personal/career/interviews/2026",
		"personal/finance",
		"personal/learning/english",
		"personal/planning",
		"personal/dev-environment",
		"automation/maintenance",
		"automation/collectors",
		"automation/shared",
		"automation/skill-sync",
		"apps/asr",
		"datasets/work-chats/oncall",
		"datasets/work-chats/agent-group",
		"datasets/web-archives/ai-blogs",
		"datasets/software-releases/claude-code",
		"datasets/documentation-mirrors/eino",
		"datasets/transcripts/talks",
		"datasets/podcasts/xiaoyuzhou",
		"datasets/videos/youtube",
		"var/quartet/state/schedules",
		"var/quartet/state/sandbox/compose",
		"var/quartet/cache/im-media",
		"var/quartet/tmp/shell",
		"var/automation",
		"var/apps/asr/state",
		"var/apps/asr/logs",
		"var/apps/asr/cache",
		"var/apps/asr/tmp",
		"var/apps/asr/venv",
		"archive/quartet/legacy-schedule-claims",
		"archive/knowledge-drafts",
		"archive/automation/claude-code-release",
	}
	for _, component := range []string{
		"memory-git-backup", "claude-code-key-rotation", "claude-code-release",
		"oncall", "ai-sites", "skill-sync", "xiaoyuzhou", "youtube",
	} {
		for _, lifecycle := range []string{"state", "logs", "cache", "tmp", "venv"} {
			dirs = append(dirs, filepath.Join("var", "automation", component, lifecycle))
		}
	}
	for _, rel := range dirs {
		path := filepath.Join(m.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create final Memory directory %s failed: %w", path, err)
		}
	}
	return nil
}

func (m *migrator) writeMemoryReadme() error {
	content := `# Memory

此目录是个人 Memory 的稳定入口，所有组件通过 LOCAL_MEMORY 引用这里。

- quartet/config：Quartet 人工配置，由根 Git 管理。
- quartet/data：Quartet 持久业务数据，使用数据快照备份。
- knowledge：长期知识成品。
- personal：个人资料。
- automation：自动化源码与必要配置。
- apps：独立应用源码。
- datasets：原始采集内容与长期数据。
- var：运行状态、日志、缓存、临时文件与虚拟环境；仅 state 需要备份。
- archive：停用但需要保留的只读内容。

文件浏览和工作区选择仍以整个 Memory 根目录为范围。不要在根目录重新创建旧的 agent、workspaces、shell、tools 等入口。
`
	path := filepath.Join(m.root, "README.md")
	if err := atomicWrite(path, []byte(content), 0o644); err != nil {
		return err
	}
	m.record("WRITE", "", path, "written", "Memory ownership and lifecycle boundaries documented")
	return nil
}

func (m *migrator) writeMemoryGitignore() error {
	content := `# Quartet persistent data, raw datasets, runtime data and archives use snapshot backups.
/quartet/data/
/datasets/
/var/
/archive/

# Local editor and private notes.
**/.obsidian/
*.local.md

# Python caches and local environments.
**/__pycache__/
*.pyc
**/.venv/

# Compiled artifacts.
*.exe
*.exe~
*.dll
*.so
*.dylib
*.test
*.out
`
	path := filepath.Join(m.root, ".gitignore")
	if err := atomicWrite(path, []byte(content), 0o644); err != nil {
		return err
	}
	m.record("WRITE", "", path, "written", "new version-control boundaries activated")
	return nil
}

func (m *migrator) rewriteAutomationLayout() error {
	files := map[string][][2]string{
		"automation/maintenance/memory-git-backup/auto_commit_memory.sh": {
			{`# 1. 暂存所有变更
git add -A`, `# 1. 只暂存根 Git 明确管理的人工配置、知识、个人资料和源码。
VERSIONED_PATHS=(README.md AGENTS.md CLAUDE.md .gitignore .claude quartet/config knowledge personal automation apps)
git add -A -- "${VERSIONED_PATHS[@]}"`},
			{`LOG_FILE="${SCRIPT_DIR}/auto_commit_memory.log"`, `RUNTIME_DIR="${LOCAL_MEMORY}/var/automation/memory-git-backup"\nLOG_FILE="${RUNTIME_DIR}/logs/auto-commit.log"`},
			{`DATE_STAMP="${SCRIPT_DIR}/.last_run_date"`, `DATE_STAMP="${RUNTIME_DIR}/state/last-run-date"\nmkdir -p "$(dirname "$LOG_FILE")" "$(dirname "$DATE_STAMP")"`},
		},
		"automation/maintenance/claude-code-key-rotation/auto_update_claude_code_key.sh": {
			{`LOG_FILE="${SCRIPT_DIR}/auto_update.log"`, `LOCAL_MEMORY="${LOCAL_MEMORY:-${HOME}/memory}"\nRUNTIME_DIR="${LOCAL_MEMORY}/var/automation/claude-code-key-rotation"\nLOG_FILE="${RUNTIME_DIR}/logs/auto-update.log"`},
			{`DATE_STAMP="${SCRIPT_DIR}/.last_run_date"`, `DATE_STAMP="${RUNTIME_DIR}/state/last-run-date"`},
			{`FORCE_SWITCH_DATE_STAMP="${SCRIPT_DIR}/.last_force_switch_date"`, `FORCE_SWITCH_DATE_STAMP="${RUNTIME_DIR}/state/last-force-switch-date"`},
			{`USAGE_SNAPSHOT_FILE="${SCRIPT_DIR}/.last_usage_snapshot"`, `USAGE_SNAPSHOT_FILE="${RUNTIME_DIR}/state/last-usage-snapshot"\nmkdir -p "$(dirname "$LOG_FILE")" "$(dirname "$DATE_STAMP")"`},
		},
		"automation/collectors/claude-code-release/download-claude-code.sh": {
			{`ANALYSIS_OUTPUT_DIR="${LOCAL_MEMORY}/knowledge/ai-and-agents/claude-code/${LATEST_VERSION}"`, `ANALYSIS_OUTPUT_DIR="${LOCAL_MEMORY}/datasets/software-releases/claude-code/${LATEST_VERSION}"`},
			{`SUCCESS_MARK_FILE="${SCRIPT_DIR}/.data/download-claude-code.success"`, `SUCCESS_MARK_FILE="${LOCAL_MEMORY}/var/automation/claude-code-release/state/download-claude-code.success"`},
		},
		"automation/collectors/claude-code-release/write-download-claude-code-success-marker.sh": {
			{`SUCCESS_MARK_FILE="${SCRIPT_DIR}/.data/download-claude-code.success"`, `LOCAL_MEMORY="${LOCAL_MEMORY:-${HOME}/memory}"\nSUCCESS_MARK_FILE="${LOCAL_MEMORY}/var/automation/claude-code-release/state/download-claude-code.success"`},
		},
		"automation/collectors/xiaoyuzhou/scripts/run.sh": {
			{`PY="$DIR/.venv/bin/python"`, `LOCAL_MEMORY="${LOCAL_MEMORY:-${HOME}/memory}"\nPY="${LOCAL_MEMORY}/var/automation/xiaoyuzhou/venv/bin/python"\nexport XIAOYUZHOU_STATE_DIR="${LOCAL_MEMORY}/var/automation/xiaoyuzhou/state"\nexport XIAOYUZHOU_CACHE_DIR="${LOCAL_MEMORY}/var/automation/xiaoyuzhou/cache"\nexport XIAOYUZHOU_OUTPUT_DIR="${LOCAL_MEMORY}/datasets/podcasts/xiaoyuzhou"`},
			{`$DIR/.venv`, `${LOCAL_MEMORY}/var/automation/xiaoyuzhou/venv`},
		},
		"automation/collectors/xiaoyuzhou/scripts/pipeline.py": {
			{`STATE_FILE = os.path.join(ROOT, "state", "processed.json")`, `LOCAL_MEMORY = os.environ.get("LOCAL_MEMORY", os.path.expanduser("~/memory"))\nSTATE_DIR = os.environ.get("XIAOYUZHOU_STATE_DIR", os.path.join(LOCAL_MEMORY, "var", "automation", "xiaoyuzhou", "state"))\nSTATE_FILE = os.path.join(STATE_DIR, "processed.json")`},
			{`OUTPUT_DIR = os.path.join(ROOT, "output")`, `OUTPUT_DIR = os.environ.get("XIAOYUZHOU_OUTPUT_DIR", os.path.join(LOCAL_MEMORY, "datasets", "podcasts", "xiaoyuzhou"))`},
			{`AUDIO_DIR = os.path.join(ROOT, "state", "audio")`, `AUDIO_DIR = os.path.join(os.environ.get("XIAOYUZHOU_CACHE_DIR", os.path.join(LOCAL_MEMORY, "var", "automation", "xiaoyuzhou", "cache")), "audio")`},
		},
		"automation/collectors/youtube/scripts/run.sh": {
			{`PY="$DIR/.venv/bin/python"`, `LOCAL_MEMORY="${LOCAL_MEMORY:-${HOME}/memory}"\nPY="${LOCAL_MEMORY}/var/automation/youtube/venv/bin/python"\nexport YOUTUBE_STATE_DIR="${LOCAL_MEMORY}/var/automation/youtube/state"\nexport YOUTUBE_OUTPUT_DIR="${LOCAL_MEMORY}/datasets/videos/youtube"\nexport YOUTUBE_YTDLP="${LOCAL_MEMORY}/var/automation/youtube/venv/bin/yt-dlp"`},
			{`$DIR/.venv`, `${LOCAL_MEMORY}/var/automation/youtube/venv`},
		},
		"automation/collectors/youtube/scripts/queue.py": {
			{`STATE_FILE = os.path.join(ROOT, "state", "processed.json")`, `LOCAL_MEMORY = os.environ.get("LOCAL_MEMORY", os.path.expanduser("~/memory"))\nSTATE_FILE = os.path.join(os.environ.get("YOUTUBE_STATE_DIR", os.path.join(LOCAL_MEMORY, "var", "automation", "youtube", "state")), "processed.json")`},
		},
		"automation/collectors/youtube/scripts/subs.py": {
			{`YTDLP = os.path.join(ROOT, ".venv", "bin", "yt-dlp")`, `LOCAL_MEMORY = os.environ.get("LOCAL_MEMORY", os.path.expanduser("~/memory"))\nYTDLP = os.environ.get("YOUTUBE_YTDLP", os.path.join(LOCAL_MEMORY, "var", "automation", "youtube", "venv", "bin", "yt-dlp"))`},
			{`DEFAULT_OUT = os.path.join(ROOT, "output")`, `DEFAULT_OUT = os.environ.get("YOUTUBE_OUTPUT_DIR", os.path.join(LOCAL_MEMORY, "datasets", "videos", "youtube"))`},
		},
		"apps/asr/run.sh": {
			{`PY="$DIR/.venv/bin/python"`, `LOCAL_MEMORY="${LOCAL_MEMORY:-${HOME}/memory}"\nPY="${LOCAL_MEMORY}/var/apps/asr/venv/bin/python"\nexport ASR_TMP_DIR="${LOCAL_MEMORY}/var/apps/asr/tmp"`},
			{`$DIR/.venv`, `${LOCAL_MEMORY}/var/apps/asr/venv`},
		},
		"apps/asr/app/server.py": {
			{`TMP_DIR = os.path.join(ROOT_DIR, "tmp")`, `LOCAL_MEMORY = os.environ.get("LOCAL_MEMORY", os.path.expanduser("~/memory"))\nTMP_DIR = os.environ.get("ASR_TMP_DIR", os.path.join(LOCAL_MEMORY, "var", "apps", "asr", "tmp"))`},
		},
		"apps/asr/app/transcribe_engine.py": {
			{`shell/xiaoyuzhou/scripts/transcribe.py`, `automation/collectors/xiaoyuzhou/scripts/transcribe.py`},
		},
		"automation/skill-sync/install_skills.sh": {
			{`skills_instal.sh`, `install_skills.sh`},
		},
	}
	for rel, replacements := range files {
		path := filepath.Join(m.root, filepath.FromSlash(rel))
		if err := replaceFileStrings(path, replacements); err != nil {
			return err
		}
	}

	aiSitesDir := filepath.Join(m.root, "automation", "collectors", "ai-sites")
	if err := filepath.WalkDir(aiSitesDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".sh" {
			return nil
		}
		return replaceFileStrings(path, [][2]string{
			{`ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"`, `ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"`},
			{`${ROOT_DIR}/lib`, `${ROOT_DIR}/shared`},
			{`${ROOT_DIR}/.data`, `${LOCAL_MEMORY:-${HOME}/memory}/var/automation/ai-sites/state`},
		})
	}); err != nil && !os.IsNotExist(err) {
		return err
	}

	oncallDir := filepath.Join(m.root, "automation", "collectors", "oncall")
	if err := filepath.WalkDir(oncallDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".sh" {
			return nil
		}
		return replaceFileStrings(path, [][2]string{
			{`${SCRIPT_DIR}/../lib`, `${SCRIPT_DIR}/../../shared`},
			{`$LOCAL_MEMORY/oncall`, `$LOCAL_MEMORY/datasets/work-chats/oncall`},
			{`${LOCAL_MEMORY}/oncall`, `${LOCAL_MEMORY}/datasets/work-chats/oncall`},
		})
	}); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (m *migrator) rewriteActiveReferences() error {
	fullTextRoots := []string{
		"automation",
		"apps",
		"quartet/config",
	}
	for _, rel := range fullTextRoots {
		root := filepath.Join(m.root, filepath.FromSlash(rel))
		if err := m.rewriteTextTree(root); err != nil {
			return err
		}
	}
	for _, rel := range []string{
		"AGENTS.md",
		"CLAUDE.md",
		".claude/settings.local.json",
		".git/config",
		".git/ai/git_hooks_state.json",
		"quartet/data/models.json",
		"quartet/data/settings.json",
		"personal/career/current-team.md",
		"personal/planning/financial-freedom.md",
	} {
		path := filepath.Join(m.root, filepath.FromSlash(rel))
		if err := m.rewriteWholeFile(path); err != nil {
			return err
		}
	}

	dataRoot := filepath.Join(m.root, "quartet", "data")
	if err := filepath.WalkDir(dataRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if path == filepath.Join(dataRoot, "models.json") || path == filepath.Join(dataRoot, "settings.json") {
			return nil
		}
		switch filepath.Ext(path) {
		case ".json":
			return m.rewriteStructuredJSON(path, false)
		case ".jsonl":
			return m.rewriteStructuredJSONL(path)
		default:
			return nil
		}
	}); err != nil {
		return fmt.Errorf("rewrite persistent structured references failed: %w", err)
	}

	stateRoot := filepath.Join(m.root, "var")
	if err := filepath.WalkDir(stateRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "logs" || entry.Name() == "cache" || entry.Name() == "tmp" || entry.Name() == "venv" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".json" {
			return m.rewriteStructuredJSON(path, true)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("rewrite runtime state references failed: %w", err)
	}
	return nil
}

func (m *migrator) rewriteTextTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".venv" || entry.Name() == "__pycache__" || entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 64<<20 {
			return nil
		}
		return m.rewriteWholeFile(path)
	})
}

func (m *migrator) rewriteWholeFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil
	}
	rewritten := m.replacePaths(string(data))
	if rewritten == string(data) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, []byte(rewritten), info.Mode().Perm()); err != nil {
		return err
	}
	m.record("REWRITE", path, path, "updated", "active path references migrated")
	return nil
}

func (m *migrator) rewriteStructuredJSON(path string, active bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !m.containsLegacyPath(data) {
		return nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("parse structured path file %s failed: %w", path, err)
	}
	changed := m.rewriteJSONValue(&value, "", active)
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, append(out, '\n'), info.Mode().Perm()); err != nil {
		return err
	}
	m.record("REWRITE_JSON", path, path, "updated", "only active or structured path fields changed")
	return nil
}

func (m *migrator) rewriteStructuredJSONL(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !m.containsLegacyPath(data) {
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 64<<20)
	var out bytes.Buffer
	changed := false
	line := 0
	for scanner.Scan() {
		line++
		raw := append([]byte(nil), scanner.Bytes()...)
		if len(bytes.TrimSpace(raw)) == 0 {
			out.WriteByte('\n')
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("parse JSONL path field %s line %d failed: %w", path, line, err)
		}
		lineChanged := m.rewriteJSONValue(&value, "", false)
		if lineChanged {
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			out.Write(encoded)
			changed = true
		} else {
			out.Write(raw)
		}
		out.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan JSONL %s failed: %w", path, err)
	}
	if !changed {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, out.Bytes(), info.Mode().Perm()); err != nil {
		return err
	}
	m.record("REWRITE_JSONL", path, path, "updated", "structured path fields migrated without rewriting historical text")
	return nil
}

func (m *migrator) rewriteJSONValue(value *any, key string, active bool) bool {
	switch current := (*value).(type) {
	case map[string]any:
		changed := false
		for childKey, child := range current {
			childActive := active || isActiveConfigKey(childKey) || isPathKey(childKey)
			if m.rewriteJSONValue(&child, childKey, childActive) {
				current[childKey] = child
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for i := range current {
			child := current[i]
			if m.rewriteJSONValue(&child, key, active) {
				current[i] = child
				changed = true
			}
		}
		return changed
	case string:
		var rewritten string
		if active || isPathKey(key) {
			rewritten = m.replacePaths(current)
		} else if strings.EqualFold(key, "content") {
			rewritten = m.replacePersistentMediaTargets(current)
		} else {
			return false
		}
		if rewritten != current {
			*value = rewritten
			return true
		}
	}
	return false
}

func isActiveConfigKey(key string) bool {
	switch strings.ToLower(key) {
	case "loopconfig", "graphconfig", "workflow", "nodes", "flow", "rounds":
		return true
	default:
		return false
	}
}

func isPathKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
	switch normalized {
	case "path", "paths", "filepath", "filepaths", "localpath", "workdir", "workingdir",
		"imageurl", "imageurls", "attachment", "attachments", "inputpath", "outputpath",
		"inputdir", "outputdir", "scriptpath", "command", "cwd", "directory", "directories":
		return true
	default:
		return strings.HasSuffix(normalized, "path") || strings.HasSuffix(normalized, "dir")
	}
}

func (m *migrator) containsLegacyPath(data []byte) bool {
	return bytes.Contains(data, []byte("/home/fanlv/memory")) ||
		bytes.Contains(data, []byte("/data00/home/fanlv/memory")) ||
		bytes.Contains(data, []byte("LOCAL_MEMORY"))
}

func (m *migrator) replacePersistentMediaTargets(value string) string {
	for _, oldRoot := range []string{"/home/fanlv/memory", "/data00/home/fanlv/memory"} {
		value = strings.ReplaceAll(value,
			oldRoot+"/uploads/im-media/",
			canonicalMemoryRoot+"/quartet/data/uploads/im-media/",
		)
	}
	return value
}

func (m *migrator) replacePaths(value string) string {
	pairs := m.pathPairs()
	for _, pair := range pairs {
		for _, oldRoot := range []string{"/data00/home/fanlv/memory", "/home/fanlv/memory"} {
			value = strings.ReplaceAll(value, oldRoot+"/"+pair[0], canonicalMemoryRoot+"/"+pair[1])
		}
		value = strings.ReplaceAll(value, "${LOCAL_MEMORY}/"+pair[0], "${LOCAL_MEMORY}/"+pair[1])
		value = strings.ReplaceAll(value, "$LOCAL_MEMORY/"+pair[0], "$LOCAL_MEMORY/"+pair[1])
	}
	value = strings.ReplaceAll(value, "/data00/home/fanlv/memory", canonicalMemoryRoot)
	return value
}

func (m *migrator) pathPairs() [][2]string {
	pairs := [][2]string{
		{"knowledge/manage-agents", "knowledge/ai-and-agents/agent-architecture"},
		{"knowledge/agent-ui-protol", "knowledge/ai-and-agents/agent-ui-protocol"},
		{"knowledge/read/web", "knowledge/reading/web"},
		{"knowledge/llm", "knowledge/ai-and-agents/llm"},
		{"knowledge/cowork", "knowledge/ai-and-agents/cowork"},
		{"knowledge/articles-and-blogs/ai-blog/raw", "datasets/web-archives/ai-blogs"},
		{"knowledge/articles-and-blogs/ai-blog", "knowledge/articles/ai"},
		{"knowledge/articles-and-blogs/read/web", "knowledge/reading/web"},
		{"knowledge/articles-and-blogs/read/llm/talk.md", "datasets/transcripts/talks/2026-07-08-lark-fireside-chat-88.md"},
		{"knowledge/ai-and-agents/claude-code", "datasets/software-releases/claude-code"},
		{"knowledge/ai-and-agents/eino/wiki", "datasets/documentation-mirrors/eino"},
		{"knowledge/ai-and-agents/manage-agents", "knowledge/ai-and-agents/agent-architecture"},
		{"knowledge/ai-and-agents/agent-ui-protol", "knowledge/ai-and-agents/agent-ui-protocol"},
		{"knowledge/career/interview", "personal/career/interviews/2026"},
		{"knowledge/env-setup/docker_proxy_setup.md", "personal/dev-environment/docker-proxy.md"},
		{"shell/xiaoyuzhou/output", "datasets/podcasts/xiaoyuzhou"},
		{"shell/xiaoyuzhou/state", "var/automation/xiaoyuzhou/state"},
		{"shell/xiaoyuzhou/.venv", "var/automation/xiaoyuzhou/venv"},
		{"shell/xiaoyuzhou", "automation/collectors/xiaoyuzhou"},
		{"shell/youtube/output", "datasets/videos/youtube"},
		{"shell/youtube/state", "var/automation/youtube/state"},
		{"shell/youtube/.venv", "var/automation/youtube/venv"},
		{"shell/youtube", "automation/collectors/youtube"},
		{"tools/asr/tmp", "var/apps/asr/tmp"},
		{"tools/asr/.venv", "var/apps/asr/venv"},
		{"tools/asr", "apps/asr"},
		{"agent/graph_workflows", "quartet/config/graph-workflows"},
		{"agent/usage_stats", "quartet/data/usage-stats"},
		{"agent/acp_probe_cache.json", "var/quartet/cache/acp-probe.json"},
		{"agent/recent_dirs.json", "var/quartet/state/recent-dirs.json"},
		{"agent/prompts", "quartet/config/prompts"},
		{"agent/templates", "quartet/config/templates"},
		{"agent/schedules", "quartet/config/schedules"},
		{"agent/models.json", "quartet/data/models.json"},
		{"agent/settings.json", "quartet/data/settings.json"},
		{"uploads", "quartet/data/uploads"},
		{"workspaces", "quartet/data/workspaces"},
		{"user_input", "quartet/data/user-input"},
		{"wechat", "quartet/data/wechat"},
		{"im", "quartet/data/im"},
		{"oncall", "datasets/work-chats/oncall"},
		{"fanlv/finance/2005-2006", "personal/finance/2025-2026"},
		{"fanlv/study/english", "personal/learning/english"},
		{"shell/get_oncall", "automation/collectors/oncall"},
		{"shell/skills/skills_instal.sh", "automation/skill-sync/install_skills.sh"},
		{"shell/skills/skills_instal.log", "var/automation/skill-sync/logs/install-skills.log"},
		{"shell/claude-code", "automation/collectors/claude-code-release"},
		{"shell/sites", "automation/collectors/ai-sites"},
		{"shell/lib", "automation/shared"},
		{"shell/skills", "automation/skill-sync"},
		{"shell/auto_commit_memory", "automation/maintenance/memory-git-backup"},
		{"shell/auto_update_claude_code_key", "automation/maintenance/claude-code-key-rotation"},
		{"fanlv/fanlv.md", "personal/career/resume.md"},
		{"fanlv/team.md", "personal/career/current-team.md"},
		{"fanlv/target.md", "personal/planning/financial-freedom.md"},
		{"fanlv/oc_bf0bd3f7175d48ba910b98750c6546f3.txt", "datasets/work-chats/agent-group/messages.txt"},
		{".sandbox/compose", "var/quartet/state/sandbox/compose"},
	}
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i][0]) > len(pairs[j][0]) })
	return pairs
}

func (m *migrator) cleanupLegacyEntries() error {
	for _, rel := range []string{
		"agent", "workspaces", "im", "uploads", "user_input", "wechat", "oncall",
		".sandbox", "fanlv", "shell", "tools", "knowledge/career",
		"knowledge/env-setup", "knowledge/articles-and-blogs/read/llm",
		"knowledge/articles-and-blogs/read", "knowledge/articles-and-blogs",
	} {
		path := filepath.Join(m.root, filepath.FromSlash(rel))
		if err := removeEmptyTree(path); err != nil {
			return err
		}
	}
	for _, rel := range []string{"bin", ".agents", ".codex", ".trae"} {
		path := filepath.Join(m.root, rel)
		entries, err := os.ReadDir(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("legacy root entry is not empty and cannot be removed safely: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		m.record("DELETE_EMPTY", path, "", "deleted", "confirmed empty and has no runtime dependency")
	}
	return nil
}

func removeEmptyTree(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := removeEmptyTree(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	entries, err = os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return os.Remove(path)
	}
	return nil
}

func (m *migrator) assertNoLegacyEntries() error {
	legacy := []string{
		"agent", "workspaces", "im", "uploads", "user_input", "wechat",
		"shell", "tools", "fanlv", "oncall", ".sandbox", "bin", ".agents", ".codex", ".trae",
	}
	var problems []string
	for _, rel := range legacy {
		path := filepath.Join(m.root, rel)
		if _, err := os.Lstat(path); err == nil {
			entries, listErr := listTree(path, 40)
			if listErr != nil {
				problems = append(problems, fmt.Sprintf("legacy entry remains: %s (list failed: %v)", path, listErr))
			} else {
				problems = append(problems, fmt.Sprintf("legacy entry remains: %s\n  %s", path, strings.Join(entries, "\n  ")))
			}
		} else if !os.IsNotExist(err) {
			problems = append(problems, fmt.Sprintf("inspect legacy entry %s failed: %v", path, err))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("unclassified or non-empty legacy entries remain:\n- %s", strings.Join(problems, "\n- "))
	}
	return nil
}

func listTree(root string, limit int) ([]string, error) {
	var result []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		result = append(result, rel)
		if len(result) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	return result, err
}

func (m *migrator) writeLayoutManifest(status string) error {
	manifest := model.MemoryLayoutManifest{
		Version:     model.CurrentMemoryLayoutVersion,
		Status:      status,
		BatchID:     m.batchID,
		CompletedAt: time.Now(),
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.root, "quartet", "layout.json")
	if err := atomicWrite(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write layout manifest %s failed: %w", path, err)
	}
	m.record("LAYOUT_MARKER", "", path, status, "layout version marker updated")
	return nil
}

func replaceFileStrings(path string, replacements [][2]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	updated := string(data)
	for _, replacement := range replacements {
		replacementText := strings.ReplaceAll(replacement[1], `\n`, "\n")
		updated = strings.ReplaceAll(updated, replacement[0], replacementText)
	}
	if updated == string(data) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return atomicWrite(path, []byte(updated), info.Mode().Perm())
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent for %s failed: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-layout-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s failed: %w", path, err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("activate file %s failed: %w", path, err)
	}
	cleanup = false
	return nil
}

func (m *migrator) record(kind, source, target, status, detail string) {
	m.operations = append(m.operations, operation{
		Kind: kind, Source: source, Target: target, Status: status, Detail: detail,
	})
}

func (m *migrator) writeReport(mode string, runErr error) error {
	sort.SliceStable(m.operations, func(i, j int) bool {
		a := m.operations[i]
		b := m.operations[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Target < b.Target
	})
	report := migrationReport{
		BatchID:         m.batchID,
		Root:            m.root,
		Mode:            mode,
		StartedAt:       m.startedAt,
		FinishedAt:      time.Now(),
		BeforeInventory: m.before,
		AfterInventory:  m.after,
		Operations:      m.operations,
	}
	if runErr != nil {
		report.Error = runErr.Error()
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(m.reportPath, append(data, '\n'), 0o644)
}

func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel))
}
