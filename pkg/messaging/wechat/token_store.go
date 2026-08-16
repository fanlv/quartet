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

// userTokensMu serializes read-modify-write operations so updates to different
// bot partitions cannot overwrite one another. Writes still use the sandbox's
// atomic replace, so other processes see either the old or complete new file.
var userTokensMu sync.Mutex

const userTokenStoreVersion = 2

type userTokenStore struct {
	Version int                          `json:"version"`
	Bots    map[string]map[string]string `json:"bots"`
}

// loadUserTokens reads the persisted ContextTokens for one bot account. Legacy
// userID → token files are attributed to the active bot on first read and
// upgraded on the next write.
func loadUserTokens(botID string) map[string]string {
	userTokensMu.Lock()
	defer userTokensMu.Unlock()

	store := readUserTokenStore(botID)
	return cloneUserTokens(store.Bots[botID])
}

func readUserTokenStore(legacyBotID string) userTokenStore {
	store := userTokenStore{
		Version: userTokenStoreVersion,
		Bots:    make(map[string]map[string]string),
	}
	sb := fileserver.GetFileManager()
	readResult, err := sb.FileRead(&fsmodel.FileReadRequest{File: deeppath.WeChatUserTokensFile()})
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("[wechat/tokenstore] read user tokens failed: %v", err)
		}
		return store
	}

	var versioned userTokenStore
	if err := json.Unmarshal([]byte(readResult.Content), &versioned); err == nil && versioned.Bots != nil {
		versioned.Version = userTokenStoreVersion
		return versioned
	}

	var legacy map[string]string
	if err := json.Unmarshal([]byte(readResult.Content), &legacy); err != nil {
		logger.Warn("[wechat/tokenstore] parse user tokens failed: %v", err)
		return store
	}
	if legacyBotID != "" && len(legacy) > 0 {
		store.Bots[legacyBotID] = cloneUserTokens(legacy)
	}
	return store
}

// saveUserTokens atomically replaces one bot account's token map while
// preserving other bot partitions.
func saveUserTokens(botID string, tokens map[string]string) error {
	if botID == "" {
		return fmt.Errorf("bot id is required")
	}

	userTokensMu.Lock()
	defer userTokensMu.Unlock()

	store := readUserTokenStore(botID)
	if len(tokens) == 0 {
		delete(store.Bots, botID)
	} else {
		store.Bots[botID] = cloneUserTokens(tokens)
	}

	sb := fileserver.GetFileManager()
	if err := sb.MkDir(&fsmodel.MkDirRequest{Path: deeppath.WeChatAccountsDir()}); err != nil {
		return fmt.Errorf("create accounts dir: %w", err)
	}
	data, err := json.MarshalIndent(store, "", "  ")
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

// RemoveUserTokens removes one bot account's persisted conversation tokens.
// The whole file is deleted once no bot partitions remain.
func RemoveUserTokens(botID string) error {
	userTokensMu.Lock()
	defer userTokensMu.Unlock()

	sb := fileserver.GetFileManager()
	store := readUserTokenStore(botID)
	delete(store.Bots, botID)
	if len(store.Bots) == 0 {
		if err := sb.FileDelete(&fsmodel.FileDeleteRequest{Path: deeppath.WeChatUserTokensFile()}); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove user tokens: %w", err)
		}
		return nil
	}

	data, err := json.MarshalIndent(store, "", "  ")
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

func cloneUserTokens(tokens map[string]string) map[string]string {
	out := make(map[string]string, len(tokens))
	for userID, token := range tokens {
		if userID != "" && token != "" {
			out[userID] = token
		}
	}
	return out
}
