package prompt

import (
	"context"
	"testing"
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
