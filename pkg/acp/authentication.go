package acp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	acp "github.com/eino-contrib/acp"
)

// AuthenticationRequiredError preserves the original protocol error and all
// diagnostics while allowing management code to distinguish missing login.
type AuthenticationRequiredError struct {
	Err     error
	Methods []acp.AuthMethod
}

func (e *AuthenticationRequiredError) Error() string { return e.Err.Error() }
func (e *AuthenticationRequiredError) Unwrap() error { return e.Err }

// AuthMethods is connection-scoped and immutable after initialize.
func (c *Conn) AuthMethods() []acp.AuthMethod {
	return append([]acp.AuthMethod(nil), c.authMethods...)
}

// NewAuthenticationConn only initializes the protocol. Authentication itself
// is an explicit, separately cancellable action. Terminal methods are executed
// by the user in an interactive terminal on the backend host; they are never
// incorrectly sent to the authenticate RPC.
func NewAuthenticationConn(ctx context.Context, agentType, workdir string, output func(string), link func(string, string)) (*Conn, error) {
	return newConn(ctx, agentType, workdir, connOptions{noBrowser: true, onOutput: output, onAuthLink: link})
}

func (c *Conn) Authenticate(ctx context.Context, methodID string) error {
	found := false
	for _, method := range c.authMethods {
		if v, ok := method.AsAgentVariant(); ok && string(v.ID) == methodID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("ACP agent did not advertise an agent authentication method %q", methodID)
	}
	_, err := c.conn.Authenticate(ctx, acp.AuthenticateRequest{MethodID: acp.AuthMethodID(methodID)})
	if err != nil {
		return fmt.Errorf("acp authenticate failed: method=%q: %w, stderr: %s", methodID, err, c.Stderr())
	}
	return nil
}

// ProbeSession refuses interactive login. Some servers start OAuth from
// session/new instead of returning -32000; stop that request once they report
// a login prompt, with the full stderr preserved for the user.
func (c *Conn) ProbeSession(ctx context.Context, workdir string) (*SessionResponse, error) {
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-probeCtx.Done():
				return
			case <-ticker.C:
				if loginPrompt(c.Stderr()) || c.client.loginRequired.Load() {
					cancel()
					return
				}
			}
		}
	}()
	response, err := c.NewSession(probeCtx, workdir)
	if err == nil {
		return response, nil
	}
	var authErr *AuthenticationRequiredError
	var rpcErr *acp.RPCError
	if errors.As(err, &authErr) {
		return nil, err
	}
	if (errors.As(err, &rpcErr) && rpcErr.Code == int(acp.ErrorCodeAuthenticationRequired)) ||
		loginPrompt(c.Stderr()) || c.client.loginRequired.Load() {
		return nil, &AuthenticationRequiredError{Err: fmt.Errorf("authentication required; interactive login is disabled during ACP validation: %w", err), Methods: c.AuthMethods()}
	}
	return nil, err
}

func loginPrompt(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range []string{
		"launching browser login flow", "open the following link to authenticate",
		"authentication required", "please log in", "please login", "not logged in",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
