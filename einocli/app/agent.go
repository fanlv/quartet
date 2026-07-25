// Package app is the eino-cli application layer: the ACP server agent that
// bridges ACP JSON-RPC calls to the eino runtime, plus the session metadata
// store under $EINO_HOME/sessions/.
//
// stdout is the JSON-RPC channel in acp serve mode — nothing in this package
// (or anything it calls) may write to stdout. All logging goes through
// einocli/logger, which writes to stderr.
package app

import (
	"context"
	"sync"

	acp "github.com/eino-contrib/acp"
	acpconn "github.com/eino-contrib/acp/conn"
)

// Agent implements the ACP agent-side RPC interface on top of the eino
// runtime. It embeds acp.BaseAgent so every method not explicitly supported
// fails loudly with method-not-found (client-side fs/*, terminal/*,
// request_permission are never called — the quartet client advertises no
// clientCapabilities).
type Agent struct {
	acp.BaseAgent

	version string

	// agentConn is injected by SetClientConnection (server.ConnectionAwareAgent)
	// and is the only outbound channel: session/update notifications.
	agentConn *acpconn.AgentConnection

	// mu guards sessions. Individual sessionState fields are guarded by the
	// session's own mutex — do not hold mu while touching session internals.
	mu       sync.Mutex
	sessions map[string]*sessionState
}

// NewAgent constructs the ACP agent. version is reported in the initialize
// handshake's agentInfo.
func NewAgent(version string) *Agent {
	return &Agent{
		version:  version,
		sessions: map[string]*sessionState{},
	}
}

// SetClientConnection implements server.ConnectionAwareAgent. The ACP server
// injects the per-connection handle so the agent can emit session/update
// notifications back to the client.
func (a *Agent) SetClientConnection(conn *acpconn.AgentConnection) {
	a.agentConn = conn
}

// Initialize negotiates the protocol. The resume capability is load-bearing:
// the quartet client switches its reconnect path from session/load (history
// replay) to session/resume (no replay) when it is present.
func (a *Agent) Initialize(_ context.Context, _ acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersion(acp.CurrentProtocolVersion),
		AgentInfo: &acp.Implementation{
			Name:    "eino-cli",
			Version: a.version,
		},
		AgentCapabilities: &acp.AgentCapabilities{
			LoadSession: true,
			SessionCapabilities: &acp.SessionCapabilities{
				Resume: &acp.SessionResumeCapabilities{},
			},
		},
	}, nil
}

// closeAllRuntimes best-effort closes every cached runtime. Called when the
// ACP connection dies so in-flight runs don't linger past the client.
func (a *Agent) closeAllRuntimes() {
	a.mu.Lock()
	states := make([]*sessionState, 0, len(a.sessions))
	for _, st := range a.sessions {
		states = append(states, st)
	}
	a.mu.Unlock()
	for _, st := range states {
		st.mu.Lock()
		rt := st.rt
		st.rt = nil
		st.rtKey = ""
		st.mu.Unlock()
		if rt != nil {
			rt.Close()
		}
	}
}
