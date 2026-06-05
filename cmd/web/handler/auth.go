package handler

import (
	"crypto/subtle"
	"os"
	"strings"

	"github.com/fanlv/quartet/types/consts"
)

// CheckAgentAuth reports whether token matches the comma-separated secrets
// configured in EnvKeyAgentAuth.
//
// Quartet is designed to run on the operator's own machine or sandbox, so
// the default is open: when EnvKeyAgentAuth is unset every token is accepted.
// Set EnvKeyAgentAuth to one or more tokens to enable token-based
// authorization (required when binding to a non-loopback interface).
//
// Pure boolean result: callers decide whether to log, at which level, and
// with what request context. Reusing this function in non-gating contexts
// (e.g. probing a JobEnable flag) is therefore safe.
func CheckAgentAuth(token string) bool {
	if !IsAuthRequired() {
		return true
	}
	raw := os.Getenv(consts.EnvKeyAgentAuth)
	for _, secret := range strings.Split(raw, ",") {
		if s := strings.TrimSpace(secret); s != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s)) == 1 {
			return true
		}
	}
	return false
}

// IsAuthRequired reports whether the server has at least one non-empty token
// configured in EnvKeyAgentAuth, i.e. whether requests to /api/v1/* must
// carry a matching X-AGENT-AUTH header. Exposed so the unauthenticated
// /api/v1/health probe can advertise the requirement to the frontend, which
// uses the flag to decide whether to render the token-entry gate before
// firing any protected request.
func IsAuthRequired() bool {
	raw := os.Getenv(consts.EnvKeyAgentAuth)
	if raw == "" {
		return false
	}
	for _, secret := range strings.Split(raw, ",") {
		if strings.TrimSpace(secret) != "" {
			return true
		}
	}
	return false
}
