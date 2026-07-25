package app

import (
	"context"
	"os"

	acpconn "github.com/eino-contrib/acp/conn"
	"github.com/eino-contrib/acp/transport/stdio"
	"github.com/fanlv/quartet/einocli/logger"
)

// maxMessageSize caps a single inbound JSON-RPC message. Prompts may carry
// large text payloads (pasted logs, base64-free image tags are paths, not
// data), so 64MB matches the canonical wiring from the SDK example.
const maxMessageSize = 64 * 1024 * 1024

// ServeACP runs the ACP agent over stdio until the connection terminates
// (stdin EOF / client gone). stdout carries ONLY protocol messages; all logs
// go to stderr via einocli/logger.
func ServeACP(ctx context.Context, version string) error {
	agent := NewAgent(version)
	transport := stdio.NewTransport(os.Stdin, os.Stdout, stdio.WithMaxMessageSize(maxMessageSize))
	conn := acpconn.NewAgentConnectionFromTransport(agent, transport)
	agent.SetClientConnection(conn)

	if err := conn.Start(ctx); err != nil {
		return err
	}
	logger.Infof(ctx, "eino-cli acp server ready (pid=%d)", os.Getpid())

	<-conn.Done()
	agent.closeAllRuntimes()
	return nil
}
