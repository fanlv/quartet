package prompt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

type stubPromptRepo struct {
	data map[string]string
}

func (r *stubPromptRepo) Get(_ context.Context, key string) (string, error) {
	return r.data[key], nil
}

func (r *stubPromptRepo) Save(_ context.Context, key string, content string) error {
	if r.data == nil {
		r.data = make(map[string]string)
	}
	r.data[key] = content
	return nil
}

type stubWorkspaceService struct {
	ws *model.Workspace
}

func (s *stubWorkspaceService) Create(ws *model.Workspace) error { return nil }

func (s *stubWorkspaceService) Get(id string) (*model.Workspace, bool) {
	if s.ws != nil && s.ws.ID == id {
		return s.ws, true
	}
	return nil, false
}

func (s *stubWorkspaceService) List() []*model.Workspace { return nil }

func (s *stubWorkspaceService) Update(id string, title, description, workdir string) (*model.Workspace, error) {
	return nil, nil
}

func (s *stubWorkspaceService) MarkDeleted(id string) error { return nil }

func (s *stubWorkspaceService) Delete(id string) error { return nil }

func (s *stubWorkspaceService) EnsureDefault() error { return nil }

func (s *stubWorkspaceService) RegenerateAllColors() ([]*model.Workspace, error) { return nil, nil }

func (s *stubWorkspaceService) SetSandboxRef(id string, ref *model.SandboxRef) error { return nil }

func (s *stubWorkspaceService) Revision() uint64 { return 0 }

func (s *stubWorkspaceService) TrustedFileWorkspaceRoots() []string { return nil }

func (s *stubWorkspaceService) DefaultWorkdir() string { return "" }

func TestWriteHomeAgentsMDMirrorsExpandedContent(t *testing.T) {
	workdir := t.TempDir()
	localAgentsPath := filepath.Join(workdir, localAgentsFileName)
	localClaudePath := filepath.Join(workdir, localClaudeFileName)
	if err := os.WriteFile(localAgentsPath, []byte("local-agents"), 0o644); err != nil {
		t.Fatalf("seed local agents: %v", err)
	}
	if err := os.WriteFile(localClaudePath, []byte("local-claude"), 0o644); err != nil {
		t.Fatalf("seed local claude: %v", err)
	}

	repo := &stubPromptRepo{data: map[string]string{
		"SOUL": "Soul Content",
	}}
	svc := &serviceImpl{
		repo:      repo,
		wsService: &stubWorkspaceService{ws: &model.Workspace{ID: consts.DefaultWorkspaceID, Workdir: workdir}},
	}

	input := "header\n{{SOUL.MD}}\nfooter"
	if err := svc.SavePrompt(context.Background(), homeAgentsMDKey, input); err != nil {
		t.Fatalf("SavePrompt(homeAgentsMDKey) error = %v", err)
	}

	// Raw template (with placeholder) is stored in the repo; the
	// mirrored files carry the expanded content.
	if got := repo.data[homeAgentsMDKey]; got != input {
		t.Fatalf("repo[homeAgentsMDKey] = %q, want %q (raw template preserved)", got, input)
	}

	wantExpanded := "header\nSoul Content\nfooter"
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: filepath.Join(workdir, agentsFileName), want: wantExpanded},
		{path: filepath.Join(workdir, claudeFileName), want: wantExpanded},
		{path: localAgentsPath, want: "local-agents"},
		{path: localClaudePath, want: "local-claude"},
	} {
		data, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", tc.path, err)
		}
		if got := string(data); got != tc.want {
			t.Fatalf("file %s = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestSaveReferenceKeyRefreshesHomeAgentsMDMirror(t *testing.T) {
	workdir := t.TempDir()
	agentsPath := filepath.Join(workdir, agentsFileName)
	claudePath := filepath.Join(workdir, claudeFileName)

	repo := &stubPromptRepo{data: map[string]string{
		"SOUL":          "Old Soul",
		homeAgentsMDKey: "header\n{{SOUL.MD}}\nfooter",
	}}
	svc := &serviceImpl{
		repo:      repo,
		wsService: &stubWorkspaceService{ws: &model.Workspace{ID: consts.DefaultWorkspaceID, Workdir: workdir}},
	}

	// Seed the mirror so we can observe the refresh overwriting it.
	if err := os.WriteFile(agentsPath, []byte("header\nOld Soul\nfooter"), 0o644); err != nil {
		t.Fatalf("seed agents: %v", err)
	}
	if err := os.WriteFile(claudePath, []byte("header\nOld Soul\nfooter"), 0o644); err != nil {
		t.Fatalf("seed claude: %v", err)
	}

	if err := svc.SavePrompt(context.Background(), "SOUL", "New Soul"); err != nil {
		t.Fatalf("SavePrompt(SOUL) error = %v", err)
	}

	want := "header\nNew Soul\nfooter"
	for _, path := range []string{agentsPath, claudePath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		if got := string(data); got != want {
			t.Fatalf("mirror %s = %q, want %q", path, got, want)
		}
	}
}

func TestSaveReferenceKeySkipsMirrorWhenHomeAgentsMDUnset(t *testing.T) {
	workdir := t.TempDir()
	agentsPath := filepath.Join(workdir, agentsFileName)

	repo := &stubPromptRepo{data: map[string]string{}}
	svc := &serviceImpl{
		repo:      repo,
		wsService: &stubWorkspaceService{ws: &model.Workspace{ID: consts.DefaultWorkspaceID, Workdir: workdir}},
	}

	if err := svc.SavePrompt(context.Background(), "SOUL", "Any Soul"); err != nil {
		t.Fatalf("SavePrompt(SOUL) error = %v", err)
	}

	if _, err := os.Stat(agentsPath); !os.IsNotExist(err) {
		t.Fatalf("AGENTS.md should not be materialized when home template is unset, got err = %v", err)
	}
}

func TestGetHomeAgentsMDReturnsRawTemplate(t *testing.T) {
	workdir := t.TempDir()

	repo := &stubPromptRepo{data: map[string]string{
		"SOUL":          "Soul Content",
		homeAgentsMDKey: "intro\n{{SOUL.MD}}\nend",
	}}
	svc := &serviceImpl{
		repo:      repo,
		wsService: &stubWorkspaceService{ws: &model.Workspace{ID: consts.DefaultWorkspaceID, Workdir: workdir}},
	}

	got, err := svc.GetPrompt(context.Background(), homeAgentsMDKey)
	if err != nil {
		t.Fatalf("GetPrompt(homeAgentsMDKey) error = %v", err)
	}
	want := "intro\n{{SOUL.MD}}\nend"
	if got != want {
		t.Fatalf("GetPrompt(homeAgentsMDKey) = %q, want %q", got, want)
	}
}

func TestGetHomeAgentsMDFallsBackToFileWhenRepoEmpty(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, agentsFileName), []byte("global-agents"), 0o644); err != nil {
		t.Fatalf("seed global agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, localAgentsFileName), []byte("local-agents"), 0o644); err != nil {
		t.Fatalf("seed local agents: %v", err)
	}

	svc := &serviceImpl{
		repo:      &stubPromptRepo{},
		wsService: &stubWorkspaceService{ws: &model.Workspace{ID: consts.DefaultWorkspaceID, Workdir: workdir}},
	}

	got, err := svc.GetPrompt(context.Background(), homeAgentsMDKey)
	if err != nil {
		t.Fatalf("GetPrompt(homeAgentsMDKey) error = %v", err)
	}
	if got != "global-agents" {
		t.Fatalf("GetPrompt(homeAgentsMDKey) = %q, want %q", got, "global-agents")
	}
}

func TestGetHomeAgentsMDFallsBackToLocalWhenGlobalMissing(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, localAgentsFileName), []byte("local-agents"), 0o644); err != nil {
		t.Fatalf("seed local agents: %v", err)
	}

	svc := &serviceImpl{
		repo:      &stubPromptRepo{},
		wsService: &stubWorkspaceService{ws: &model.Workspace{ID: consts.DefaultWorkspaceID, Workdir: workdir}},
	}

	got, err := svc.GetPrompt(context.Background(), homeAgentsMDKey)
	if err != nil {
		t.Fatalf("GetPrompt(homeAgentsMDKey) error = %v", err)
	}
	if got != "local-agents" {
		t.Fatalf("GetPrompt(homeAgentsMDKey) = %q, want %q", got, "local-agents")
	}
}

func TestSavePromptStoresRawTemplate(t *testing.T) {
	repo := &stubPromptRepo{data: map[string]string{
		"SOUL":   "Soul Content",
		"USER":   "User Content",
		"MEMORY": "Memory Content",
	}}
	svc := &serviceImpl{repo: repo}

	input := "prefix\n{{SOUL.MD}}\n{{USER.md}}\n{MEMORY.md}\nsuffix"
	if err := svc.SavePrompt(context.Background(), "system_prompt", input); err != nil {
		t.Fatalf("SavePrompt(system_prompt) error = %v", err)
	}

	// Raw placeholders must be preserved in storage so the UI can
	// round-trip without loss and runtime picks up the latest
	// SOUL / USER / MEMORY content via ResolvePrompt.
	if got := repo.data["system_prompt"]; got != input {
		t.Fatalf("saved system_prompt = %q, want %q", got, input)
	}
}

func TestGetPromptReturnsRawTemplate(t *testing.T) {
	stored := "prefix\n{{SOUL.MD}}\n{{USER.md}}\n{MEMORY.md}\nsuffix"
	repo := &stubPromptRepo{data: map[string]string{
		"SOUL":          "Soul Content",
		"USER":          "User Content",
		"MEMORY":        "Memory Content",
		"system_prompt": stored,
	}}
	svc := &serviceImpl{repo: repo}

	got, err := svc.GetPrompt(context.Background(), "system_prompt")
	if err != nil {
		t.Fatalf("GetPrompt(system_prompt) error = %v", err)
	}
	if got != stored {
		t.Fatalf("GetPrompt(system_prompt) = %q, want %q", got, stored)
	}
}

func TestResolvePromptExpandsReferences(t *testing.T) {
	repo := &stubPromptRepo{data: map[string]string{
		"SOUL":          "Soul Content",
		"USER":          "User Content",
		"MEMORY":        "Memory Content",
		"system_prompt": "prefix\n{{SOUL.MD}}\n{{USER.md}}\n{MEMORY.md}\nsuffix",
	}}
	svc := &serviceImpl{repo: repo}

	got, err := svc.ResolvePrompt(context.Background(), "system_prompt")
	if err != nil {
		t.Fatalf("ResolvePrompt(system_prompt) error = %v", err)
	}
	want := "prefix\nSoul Content\nUser Content\nMemory Content\nsuffix"
	if got != want {
		t.Fatalf("ResolvePrompt(system_prompt) = %q, want %q", got, want)
	}
}

func TestResolvePromptReflectsLatestReferenceContent(t *testing.T) {
	repo := &stubPromptRepo{data: map[string]string{
		"SOUL":          "Soul Content",
		"system_prompt": "{{SOUL.MD}}",
	}}
	svc := &serviceImpl{repo: repo}

	first, err := svc.ResolvePrompt(context.Background(), "system_prompt")
	if err != nil {
		t.Fatalf("ResolvePrompt first error = %v", err)
	}
	if first != "Soul Content" {
		t.Fatalf("first resolve = %q, want %q", first, "Soul Content")
	}

	// Update SOUL and re-resolve without touching system_prompt —
	// the new soul value must propagate through without any save.
	repo.data["SOUL"] = "Updated Soul"
	second, err := svc.ResolvePrompt(context.Background(), "system_prompt")
	if err != nil {
		t.Fatalf("ResolvePrompt second error = %v", err)
	}
	if second != "Updated Soul" {
		t.Fatalf("second resolve = %q, want %q", second, "Updated Soul")
	}
}

func TestResolvePromptLiteralTextIsNotCollapsed(t *testing.T) {
	// A user whose system prompt happens to contain the literal bytes of
	// SOUL.md content must get them through ResolvePrompt unchanged. The
	// previous content-based collapse would silently rewrite the inner
	// "Soul Content" into {{SOUL.MD}} on read.
	repo := &stubPromptRepo{data: map[string]string{
		"SOUL":          "Soul Content",
		"system_prompt": "quote from SOUL: Soul Content (end quote)",
	}}
	svc := &serviceImpl{repo: repo}

	got, err := svc.GetPrompt(context.Background(), "system_prompt")
	if err != nil {
		t.Fatalf("GetPrompt error = %v", err)
	}
	if got != "quote from SOUL: Soul Content (end quote)" {
		t.Fatalf("GetPrompt = %q, want literal preserved", got)
	}
}

func TestGetPromptDoesNotCollapseSelfReferenceFile(t *testing.T) {
	repo := &stubPromptRepo{data: map[string]string{
		"SOUL":   "Soul Content",
		"USER":   "User Content",
		"MEMORY": "Memory Content",
	}}
	svc := &serviceImpl{repo: repo}

	got, err := svc.GetPrompt(context.Background(), "SOUL")
	if err != nil {
		t.Fatalf("GetPrompt(SOUL) error = %v", err)
	}
	if got != "Soul Content" {
		t.Fatalf("GetPrompt(SOUL) = %q, want %q", got, "Soul Content")
	}
}

func TestSavePromptSelfReferenceIsStoredRaw(t *testing.T) {
	repo := &stubPromptRepo{data: map[string]string{
		"SOUL":   "Soul Content",
		"USER":   "User Content",
		"MEMORY": "Memory Content",
	}}
	svc := &serviceImpl{repo: repo}

	input := "literal {{SOUL.MD}} and {{USER.MD}}"
	if err := svc.SavePrompt(context.Background(), "SOUL", input); err != nil {
		t.Fatalf("SavePrompt(SOUL) error = %v", err)
	}

	// Raw storage: every placeholder (including self references) is
	// preserved verbatim.
	if got := repo.data["SOUL"]; got != input {
		t.Fatalf("saved SOUL = %q, want %q", got, input)
	}
}

func TestResolvePromptSelfReferenceKeepsSelfPlaceholder(t *testing.T) {
	// The self-reference rule applies at resolve time too: SOUL must not
	// recursively expand itself, but it should expand USER / MEMORY as
	// normal.
	repo := &stubPromptRepo{data: map[string]string{
		"SOUL":   "literal {{SOUL.MD}} and {{USER.MD}}",
		"USER":   "User Content",
		"MEMORY": "Memory Content",
	}}
	svc := &serviceImpl{repo: repo}

	got, err := svc.ResolvePrompt(context.Background(), "SOUL")
	if err != nil {
		t.Fatalf("ResolvePrompt(SOUL) error = %v", err)
	}
	want := "literal {{SOUL.MD}} and User Content"
	if got != want {
		t.Fatalf("ResolvePrompt(SOUL) = %q, want %q", got, want)
	}
}
