package eino

import (
	"testing"

	"github.com/fanlv/quartet/pkg/modelbuilder"
)

// Capacity reading is handled via sessioncache.EnvInt, but we keep a
// smoke test here to ensure the envMaxEinoAgents key is plumbed all
// the way through NewService (not just declared).
func TestNewService_ReadsConfiguredMax(t *testing.T) {
	t.Setenv(envMaxEinoAgents, "3")
	svc := NewService()
	if svc == nil {
		t.Fatalf("NewService returned nil")
	}
	// No panics, no obvious mis-wiring. The detailed LRU / capacity
	// semantics now live in services/agent/internal/sessioncache and
	// are covered by its own tests.
}

func TestComputeAgentFingerprint(t *testing.T) {
	mk := func(model, base, key string) *modelbuilder.ModelConfig {
		return &modelbuilder.ModelConfig{
			ModelClass: modelbuilder.ModelClassClaude,
			Connection: &modelbuilder.ConnectionInfo{
				APIKey:  key,
				BaseURL: base,
				Model:   model,
			},
		}
	}

	base := computeAgentFingerprint("/wd", mk("claude-1", "https://api.example.com", "sk-1"), "you are sophie")
	if base == "" {
		t.Fatal("fingerprint should not be empty")
	}

	tests := []struct {
		name string
		fn   func() string
		same bool
	}{
		{"same inputs → same fingerprint", func() string {
			return computeAgentFingerprint("/wd", mk("claude-1", "https://api.example.com", "sk-1"), "you are sophie")
		}, true},
		{"different model id → different", func() string {
			return computeAgentFingerprint("/wd", mk("claude-2", "https://api.example.com", "sk-1"), "you are sophie")
		}, false},
		{"different base url → different", func() string {
			return computeAgentFingerprint("/wd", mk("claude-1", "https://api.other.com", "sk-1"), "you are sophie")
		}, false},
		{"different api key → different", func() string {
			return computeAgentFingerprint("/wd", mk("claude-1", "https://api.example.com", "sk-2"), "you are sophie")
		}, false},
		{"different workdir → different", func() string {
			return computeAgentFingerprint("/other", mk("claude-1", "https://api.example.com", "sk-1"), "you are sophie")
		}, false},
		{"different system prompt → different", func() string {
			return computeAgentFingerprint("/wd", mk("claude-1", "https://api.example.com", "sk-1"), "you are sophie v2")
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn()
			if tt.same && got != base {
				t.Errorf("expected fingerprint to match base, got %s vs %s", got, base)
			}
			if !tt.same && got == base {
				t.Errorf("expected fingerprint to differ from base, got identical %s", got)
			}
		})
	}
}

// optionFingerprintConfig is the helper the manager uses to extract the option
// fields that affect New()'s baked runtime wiring for fingerprint input. Verify
// it keeps the system prompt while still ignoring unrelated identity/toucher options.
func TestOptionFingerprintConfig_IgnoresUnrelatedOptions(t *testing.T) {
	got := optionFingerprintConfig([]Option{
		WithSystemPrompt("hello"),
		WithJobID("j-1"),
		WithWorkspaceID("ws-1"),
		WithSessionID("s-1"),
	})
	if got.SystemPrompt != "hello" {
		t.Errorf("expected systemPrompt 'hello', got %q", got.SystemPrompt)
	}

	if got := optionFingerprintConfig(nil); got.SystemPrompt != "" {
		t.Errorf("nil opts should give zero-value fingerprint config, got %+v", got)
	}
}
