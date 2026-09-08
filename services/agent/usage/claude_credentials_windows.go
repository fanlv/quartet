//go:build windows

package usage

import "context"

func readClaudeCodeCredentials(_ context.Context) (claudeCodeCredentials, string, error) {
	return readClaudeCodeCredentialsFile()
}
