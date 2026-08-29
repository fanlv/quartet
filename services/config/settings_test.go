package config

import (
	"reflect"
	"sync"
	"testing"

	"github.com/fanlv/quartet/types/model"
)

type fakeSettingsRepo struct {
	mu       sync.Mutex
	settings model.Settings
}

func (r *fakeSettingsRepo) Get() (*model.Settings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := cloneSettingsForTest(&r.settings)
	return clone, nil
}

func (r *fakeSettingsRepo) Save(s *model.Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings = *cloneSettingsForTest(s)
	return nil
}

func cloneSettingsForTest(s *model.Settings) *model.Settings {
	if s == nil {
		return nil
	}
	clone := *s
	clone.WeChatAdminIDs = append([]string(nil), s.WeChatAdminIDs...)
	if s.ACPEnvVars != nil {
		clone.ACPEnvVars = make(map[string][]model.ACPEnvVarEntry, len(s.ACPEnvVars))
		for k, v := range s.ACPEnvVars {
			clone.ACPEnvVars[k] = append([]model.ACPEnvVarEntry(nil), v...)
		}
	}
	if s.ACPEnvVersions != nil {
		clone.ACPEnvVersions = make(map[string]int64, len(s.ACPEnvVersions))
		for k, v := range s.ACPEnvVersions {
			clone.ACPEnvVersions[k] = v
		}
	}
	return &clone
}

func TestGetACPEnvVarsUsesStableAndCommandKeys(t *testing.T) {
	repo := &fakeSettingsRepo{settings: model.Settings{
		ACPEnvVars: map[string][]model.ACPEnvVarEntry{
			"codex": {
				{Key: "http_proxy", Value: "http://stable", Enabled: true},
				{Key: "all_proxy", Value: "http://disabled", Enabled: false},
			},
			"codex-acp": {
				{Key: "no_proxy", Value: "example.com", Enabled: true},
			},
		},
	}}
	svc := &settingsServiceImpl{repo: repo}

	// A serve command ("codex-acp") resolves to the bin key ("codex"), and
	// env vars saved under either key are merged; disabled entries drop out.
	got := svc.GetACPEnvVars("codex-acp")
	want := map[string]string{
		"http_proxy": "http://stable",
		"no_proxy":   "example.com",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetACPEnvVars() = %v, want %v", got, want)
	}
}

func TestSaveSettingsPreservesOwnedACPEnvVars(t *testing.T) {
	repo := &fakeSettingsRepo{settings: model.Settings{
		ACPEnvVars: map[string][]model.ACPEnvVarEntry{
			"claude": {
				{Key: "https_proxy", Value: "http://stable", Enabled: true},
			},
		},
	}}
	svc := &settingsServiceImpl{repo: repo}

	err := svc.SaveSettings(&model.Settings{
		Username: "updated",
		ACPEnvVars: map[string][]model.ACPEnvVarEntry{
			"claude-agent-acp": {
				{Key: "https_proxy", Value: "http://command", Enabled: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}

	got, err := svc.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings() error = %v", err)
	}
	if got.Username != "updated" {
		t.Fatalf("Username = %q, want updated", got.Username)
	}
	want := map[string][]model.ACPEnvVarEntry{
		"claude": {
			{Key: "https_proxy", Value: "http://stable", Enabled: true},
		},
	}
	if !reflect.DeepEqual(got.ACPEnvVars, want) {
		t.Fatalf("ACPEnvVars = %v, want %v", got.ACPEnvVars, want)
	}
}

func TestSaveACPEnvVarsOnlyChangesVersionWhenEntriesChange(t *testing.T) {
	entries := []model.ACPEnvVarEntry{{
		Key: "https_proxy", Value: "http://proxy", Enabled: true,
	}}
	repo := &fakeSettingsRepo{settings: model.Settings{
		ACPEnvVars:     map[string][]model.ACPEnvVarEntry{"codex": entries},
		ACPEnvVersions: map[string]int64{"codex": 7},
	}}
	svc := &settingsServiceImpl{repo: repo}

	version, changed, err := svc.SaveACPEnvVars("codex", entries)
	if err != nil {
		t.Fatalf("SaveACPEnvVars unchanged failed: %v", err)
	}
	if changed || version != 7 {
		t.Fatalf("unchanged save = version %d changed %t, want version 7 changed false", version, changed)
	}

	updated := []model.ACPEnvVarEntry{{
		Key: "https_proxy", Value: "http://new-proxy", Enabled: true,
	}}
	version, changed, err = svc.SaveACPEnvVars("codex", updated)
	if err != nil {
		t.Fatalf("SaveACPEnvVars changed failed: %v", err)
	}
	if !changed || version != 8 {
		t.Fatalf("changed save = version %d changed %t, want version 8 changed true", version, changed)
	}
}

func TestSaveSettingsPreservesConcurrentWeChatAdminAdd(t *testing.T) {
	repo := &fakeSettingsRepo{settings: model.Settings{WeChatAdminIDs: []string{"alice"}}}
	svc := &settingsServiceImpl{repo: repo}

	staleWebSnapshot := &model.Settings{Username: "updated", WeChatAdminIDs: []string{"alice"}}
	if err := svc.AddWeChatAdminID("bob"); err != nil {
		t.Fatalf("AddWeChatAdminID() error = %v", err)
	}
	if err := svc.SaveSettings(staleWebSnapshot); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}

	got, err := svc.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings() error = %v", err)
	}
	if got.Username != "updated" {
		t.Fatalf("Username = %q, want %q", got.Username, "updated")
	}
	wantAdmins := []string{"alice", "bob"}
	if !reflect.DeepEqual(got.WeChatAdminIDs, wantAdmins) {
		t.Fatalf("WeChatAdminIDs = %v, want %v", got.WeChatAdminIDs, wantAdmins)
	}
}

func TestSaveSettingsPreservesConcurrentWeChatAdminRemove(t *testing.T) {
	repo := &fakeSettingsRepo{settings: model.Settings{WeChatAdminIDs: []string{"alice", "bob"}}}
	svc := &settingsServiceImpl{repo: repo}

	staleWebSnapshot := &model.Settings{Username: "updated", WeChatAdminIDs: []string{"alice", "bob"}}
	if err := svc.RemoveWeChatAdminID("bob"); err != nil {
		t.Fatalf("RemoveWeChatAdminID() error = %v", err)
	}
	if err := svc.SaveSettings(staleWebSnapshot); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}

	got, err := svc.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings() error = %v", err)
	}
	if got.Username != "updated" {
		t.Fatalf("Username = %q, want %q", got.Username, "updated")
	}
	wantAdmins := []string{"alice"}
	if !reflect.DeepEqual(got.WeChatAdminIDs, wantAdmins) {
		t.Fatalf("WeChatAdminIDs = %v, want %v", got.WeChatAdminIDs, wantAdmins)
	}
}
