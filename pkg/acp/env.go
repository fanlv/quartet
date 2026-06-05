package acp

import "sync"

// EnvVar represents a KEY=VALUE environment variable pair injected into the
// ACP agent subprocess at startup.
type EnvVar struct {
	Key   string
	Value string
}

// EnvProvider resolves extra environment variables for a given agent command.
// Set once at startup from the service layer (e.g. reading user settings);
// pkg/acp consults it inside NewConn but does not own the lookup logic.
type EnvProvider func(agentType string) []EnvVar

var (
	envProviderMu sync.RWMutex
	envProvider   EnvProvider
)

// SetEnvProvider installs a provider that NewConn consults when populating
// the subprocess environment. Passing nil clears the provider.
func SetEnvProvider(p EnvProvider) {
	envProviderMu.Lock()
	envProvider = p
	envProviderMu.Unlock()
}

func resolveExtraEnv(agentType string) []EnvVar {
	envProviderMu.RLock()
	p := envProvider
	envProviderMu.RUnlock()
	if p == nil {
		return nil
	}
	return p(agentType)
}
