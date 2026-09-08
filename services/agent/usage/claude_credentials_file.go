package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func readClaudeCodeCredentialsFile() (claudeCodeCredentials, string, error) {
	var credentials claudeCodeCredentials
	home, err := os.UserHomeDir()
	if err != nil {
		return credentials, "Claude Code credentials file", fmt.Errorf("get home dir failed: %w", err)
	}
	credentialsPath := filepath.Join(home, ".claude", ".credentials.json")
	raw, err := os.ReadFile(credentialsPath)
	if err != nil {
		return credentials, credentialsPath, fmt.Errorf("read %s failed: %w", credentialsPath, err)
	}
	if err := json.Unmarshal(raw, &credentials); err != nil {
		return credentials, credentialsPath, fmt.Errorf("parse %s failed: %w", credentialsPath, err)
	}
	return credentials, credentialsPath, nil
}
