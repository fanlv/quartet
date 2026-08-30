package acp

import (
	"context"

	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/services/agent/runtimeenv"
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

// loadEnvFromSettings resolves per-agent environment variables from the user
// settings repository, then adds runtime-only defaults for built-in adapters.
// pkg/acp injects these on NewConn; the repository and catalog dependencies
// belong here at the service layer rather than in pkg/acp.
func loadEnvFromSettings(agentType string) []pkgacp.EnvVar {
	// Provider callbacks are invoked deep inside pkg/acp.NewConn and do
	// not receive a caller context. Background is the right choice for
	// the init-once settings read: logging must not depend on a
	// request-scoped ctx that may already be cancelled.
	ctx := context.Background()
	repo, err := repository.NewSettingsRepo()
	if err != nil {
		logger.Errorf(ctx, "[acp.env] init settings repo failed: agentType=%s err=%v", agentType, err)
		return resolveRuntimeEnv(agentType, nil)
	}
	settings, err := repo.Get()
	if err != nil {
		logger.Errorf(ctx, "[acp.env] load settings failed: agentType=%s err=%v", agentType, err)
		return resolveRuntimeEnv(agentType, nil)
	}
	if settings == nil || len(settings.ACPEnvVars) == 0 {
		return resolveRuntimeEnv(agentType, nil)
	}
	type envValue struct {
		value    string
		enabled  bool
		priority int
	}
	envMap := make(map[string]envValue)
	for _, key := range probe.ACPAgentEnvLookupKeys(agentType) {
		_, priority := probe.ACPAgentEnvKeyPriority(key)
		for _, e := range settings.ACPEnvVars[key] {
			if e.Key == "" {
				continue
			}
			current, ok := envMap[e.Key]
			if !ok || priority > current.priority {
				envMap[e.Key] = envValue{value: e.Value, enabled: e.Enabled, priority: priority}
			}
		}
	}
	configured := make(map[string]string, len(envMap))
	for key, value := range envMap {
		if !value.enabled {
			continue
		}
		configured[key] = value.value
	}
	return resolveRuntimeEnv(agentType, configured)
}

func resolveRuntimeEnv(agentType string, configured map[string]string) []pkgacp.EnvVar {
	effective := (runtimeenv.Resolver{}).Effective(agentType, configured)
	out := make([]pkgacp.EnvVar, 0, len(effective))
	for key, value := range effective {
		out = append(out, pkgacp.EnvVar{Key: key, Value: value})
	}
	return out
}
