package wechat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	deeppath "github.com/fanlv/quartet/types/path"
)

// userTokensMu serializes saveUserTokens writers. Reads (loadUserTokens) stay
// lock-free: saves go through the sandbox atomic write, so a reader always
// sees either the old or the complete new file (same reasoning as
// ilink/credentialsMu).
var userTokensMu sync.Mutex

// loadUserTokens reads the persisted fromUserID → ContextToken map. A missing
// or corrupt file yields an empty map (with a warning for the corrupt case) —
// proactive sends simply keep erroring with "no context for user" until the
// user messages the bot again, which is the pre-persistence behavior.
func loadUserTokens() map[string]string {
	tokens := make(map[string]string)
	sb := fileserver.GetFileManager()
	readResult, err := sb.FileRead(&fsmodel.FileReadRequest{File: deeppath.WeChatUserTokensFile()})
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("[wechat/tokenstore] read user tokens failed: %v", err)
		}
		return tokens
	}
	if err := json.Unmarshal([]byte(readResult.Content), &tokens); err != nil {
		logger.Warn("[wechat/tokenstore] parse user tokens failed: %v", err)
		return make(map[string]string)
	}
	return tokens
}

// saveUserTokens atomically persists the full token map (mode 0600 — the
// ContextToken is a conversation credential, same sensitivity class as the
// bot token files next to it).
func saveUserTokens(tokens map[string]string) error {
	userTokensMu.Lock()
	defer userTokensMu.Unlock()

	sb := fileserver.GetFileManager()
	if err := sb.MkDir(&fsmodel.MkDirRequest{Path: deeppath.WeChatAccountsDir()}); err != nil {
		return fmt.Errorf("create accounts dir: %w", err)
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal user tokens: %w", err)
	}
	if err := sb.FileWrite(&fsmodel.FileWriteRequest{
		File:    deeppath.WeChatUserTokensFile(),
		Content: string(data),
		Mode:    0o600,
		Atomic:  true,
	}); err != nil {
		return fmt.Errorf("write user tokens: %w", err)
	}
	return nil
}

// RemoveUserTokens deletes the persisted token file. Called on logout: the
// tokens are scoped to the removed credentials' conversation and useless
// afterwards. Missing file is not an error.
func RemoveUserTokens() error {
	sb := fileserver.GetFileManager()
	if err := sb.FileDelete(&fsmodel.FileDeleteRequest{Path: deeppath.WeChatUserTokensFile()}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove user tokens: %w", err)
	}
	return nil
}
