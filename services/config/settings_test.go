package config

import (
	"reflect"
	"sync"
	"testing"

	"github.com/fanlv/quartet/repository"
)

type fakeSettingsRepo struct {
	mu       sync.Mutex
	settings repository.Settings
}

func (r *fakeSettingsRepo) Get() (*repository.Settings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := cloneSettingsForTest(&r.settings)
	return clone, nil
}

func (r *fakeSettingsRepo) Save(s *repository.Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings = *cloneSettingsForTest(s)
	return nil
}

func cloneSettingsForTest(s *repository.Settings) *repository.Settings {
	if s == nil {
		return nil
	}
	clone := *s
	clone.WeChatAdminIDs = append([]string(nil), s.WeChatAdminIDs...)
	if s.ACPEnvVars != nil {
		clone.ACPEnvVars = make(map[string][]repository.ACPEnvVarEntry, len(s.ACPEnvVars))
		for k, v := range s.ACPEnvVars {
			clone.ACPEnvVars[k] = append([]repository.ACPEnvVarEntry(nil), v...)
		}
	}
	return &clone
}

func TestGetACPEnvVarsUsesStableAndCommandKeys(t *testing.T) {
	repo := &fakeSettingsRepo{settings: repository.Settings{
		ACPEnvVars: map[string][]repository.ACPEnvVarEntry{
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

func TestSaveSettingsNormalizesACPEnvVarsToStableKeys(t *testing.T) {
	repo := &fakeSettingsRepo{}
	svc := &settingsServiceImpl{repo: repo}

	err := svc.SaveSettings(&repository.Settings{
		ACPEnvVars: map[string][]repository.ACPEnvVarEntry{
			"claude": {
				{Key: "https_proxy", Value: "http://stable", Enabled: true},
			},
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
	if _, ok := got.ACPEnvVars["claude-agent-acp"]; ok {
		t.Fatalf("claude-agent-acp command key should have been normalized to bin key: %v", got.ACPEnvVars)
	}
	want := map[string][]repository.ACPEnvVarEntry{
		"claude": {
			{Key: "https_proxy", Value: "http://stable", Enabled: true},
		},
	}
	if !reflect.DeepEqual(got.ACPEnvVars, want) {
		t.Fatalf("ACPEnvVars = %v, want %v", got.ACPEnvVars, want)
	}
}

func TestSaveSettingsPreservesConcurrentWeChatAdminAdd(t *testing.T) {
	repo := &fakeSettingsRepo{settings: repository.Settings{WeChatAdminIDs: []string{"alice"}}}
	svc := &settingsServiceImpl{repo: repo}

	staleWebSnapshot := &repository.Settings{Username: "updated", WeChatAdminIDs: []string{"alice"}}
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
	repo := &fakeSettingsRepo{settings: repository.Settings{WeChatAdminIDs: []string{"alice", "bob"}}}
	svc := &settingsServiceImpl{repo: repo}

	staleWebSnapshot := &repository.Settings{Username: "updated", WeChatAdminIDs: []string{"alice", "bob"}}
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
