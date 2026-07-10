package acp

import (
	"context"

	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/agent/probe"
)

// InitEnvProvider wires the service-layer settings lookup into pkg/acp so
// NewConn can inject per-agent environment variables without the pkg/acp
// package owning settings repository access. Must be called once at
// startup (cmd/web/main.go) before any ACP subprocess is launched.
//
// Made explicit (rather than an init()) so startup ordering is visible
// in the entrypoint and tests can wire a different provider by calling
// pkgacp.SetEnvProvider directly without import-ordering subtleties.
func InitEnvProvider() {
	pkgacp.SetEnvProvider(loadEnvFromSettings)
}

// loadEnvFromSettings resolves per-agent environment variables from the
// user settings repository. pkg/acp injects these on NewConn; the
// repository dependency belongs here at the service layer rather than in
// pkg/acp.
func loadEnvFromSettings(agentType string) []pkgacp.EnvVar {
	// Provider callbacks are invoked deep inside pkg/acp.NewConn and do
	// not receive a caller context. Background is the right choice for
	// the init-once settings read: logging must not depend on a
	// request-scoped ctx that may already be cancelled.
	ctx := context.Background()
	repo, err := repository.NewSettingsRepo()
	if err != nil {
		logger.Errorf(ctx, "[acp.env] init settings repo failed: agentType=%s err=%v", agentType, err)
		return nil
	}
	settings, err := repo.Get()
	if err != nil {
		logger.Errorf(ctx, "[acp.env] load settings failed: agentType=%s err=%v", agentType, err)
		return nil
	}
	if len(settings.ACPEnvVars) == 0 {
		return nil
	}
	type envValue struct {
		value    string
		priority int
	}
	envMap := make(map[string]envValue)
	for _, key := range probe.ACPAgentEnvLookupKeys(agentType) {
		_, priority := probe.ACPAgentEnvKeyPriority(key)
		for _, e := range settings.ACPEnvVars[key] {
			if !e.Enabled || e.Key == "" {
				continue
			}
			current, ok := envMap[e.Key]
			if !ok || priority > current.priority {
				envMap[e.Key] = envValue{value: e.Value, priority: priority}
			}
		}
	}
	if len(envMap) == 0 {
		return nil
	}
	out := make([]pkgacp.EnvVar, 0, len(envMap))
	for key, value := range envMap {
		out = append(out, pkgacp.EnvVar{Key: key, Value: value.value})
	}
	return out
}
