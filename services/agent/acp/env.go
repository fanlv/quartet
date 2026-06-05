package acp

import (
	"context"

	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
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
	entries := settings.ACPEnvVars[agentType]
	if len(entries) == 0 {
		return nil
	}
	out := make([]pkgacp.EnvVar, 0, len(entries))
	for _, e := range entries {
		if !e.Enabled || e.Key == "" {
			continue
		}
		out = append(out, pkgacp.EnvVar{Key: e.Key, Value: e.Value})
	}
	return out
}
