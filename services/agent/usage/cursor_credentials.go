package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

func readCursorAuthFile() (cursorAuthFile, string, error) {
	var auth cursorAuthFile
	authPath := cursorCLIAuthPath()
	if authPath == "" {
		return auth, "cursor-agent auth.json", errors.New("resolve cursor-agent auth file path failed: home dir unavailable")
	}
	raw, err := os.ReadFile(authPath)
	if err != nil {
		return auth, authPath, fmt.Errorf("read %s failed: %w (log in with cursor-agent first)", authPath, err)
	}
	if err := json.Unmarshal(raw, &auth); err != nil {
		return auth, authPath, fmt.Errorf("parse %s failed: %w", authPath, err)
	}
	auth.AccessToken = strings.TrimSpace(auth.AccessToken)
	auth.RefreshToken = strings.TrimSpace(auth.RefreshToken)
	return auth, authPath, nil
}
