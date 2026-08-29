package ilink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	deeppath "github.com/fanlv/quartet/types/path"
)

const (
	qrCodeURL   = "https://ilinkai.weixin.qq.com/ilink/bot/get_bot_qrcode?bot_type=3"
	qrStatusURL = "https://ilinkai.weixin.qq.com/ilink/bot/get_qrcode_status?qrcode="
)

// credentialsMu guards SaveCredentials / RemoveCredentials so concurrent
// writers (e.g. multiple Web tabs triggering login) don't race on the same
// file. Reads (LoadAllCredentials) don't take the lock — SaveCredentials
// writes atomically through the local file service, so a reader
// always sees either the old file or the complete new file, never a
// partially-written one. A corrupt file from some other cause is skipped
// via the json.Unmarshal failure path in LoadAllCredentials.
var credentialsMu sync.Mutex

// FetchQRCode retrieves a new QR code for login.
func FetchQRCode(ctx context.Context) (*QRCodeResponse, error) {
	c := newUnauthenticatedClient()
	var resp QRCodeResponse
	if err := c.doGet(ctx, qrCodeURL, &resp); err != nil {
		return nil, fmt.Errorf("fetch QR code: %w", err)
	}
	return &resp, nil
}

// PollQRStatusOnce performs a single long-poll against the iLink status
// endpoint and returns whatever the server reports — wait / scaned /
// confirmed / expired. Callers that want to block until confirmed (e.g. the
// CLI login flow) should use PollQRStatus instead; the Web login API uses
// this to surface intermediate "scaned" states to the browser without
// locking up the handler for the full 90s window.
func PollQRStatusOnce(ctx context.Context, qrcode string) (*QRStatusResponse, error) {
	c := newUnauthenticatedClient()
	// url.QueryEscape guards against `&`/`=`/`#`/`%` in qrcode corrupting
	// the upstream URL structure — the iLink server returns the qrcode as
	// a simple token today but we shouldn't rely on that.
	pollURL := qrStatusURL + url.QueryEscape(qrcode)

	pollCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()

	var resp QRStatusResponse
	if err := c.doGet(pollCtx, pollURL, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// normalizeAccountID converts raw bot ID (e.g. "@abc_123") into a filesystem-
// safe form for use as the credentials filename.
func normalizeAccountID(raw string) string {
	s := raw
	for _, ch := range []string{"@", ".", ":", "/", "\\"} {
		s = strings.ReplaceAll(s, ch, "-")
	}
	s = strings.Trim(s, "-")
	if s == "" {
		s = "default"
	}
	return s
}

// SaveCredentials saves credentials to disk under
// {LOCAL_MEMORY}/quartet/data/wechat/accounts/{normalized_bot_id}.json (mode 0600).
// Writes go through an atomic file write so a crash mid-write can't leave a
// truncated credential file that would need manual cleanup.
func SaveCredentials(creds *Credentials) error {
	if creds == nil {
		return fmt.Errorf("nil credentials")
	}
	if creds.ILinkBotID == "" {
		return fmt.Errorf("missing ilink_bot_id in credentials")
	}

	credentialsMu.Lock()
	defer credentialsMu.Unlock()

	dir := deeppath.WeChatAccountsDir()
	sb := fileserver.GetFileManager()
	if err := sb.MkDir(&fsmodel.MkDirRequest{Path: dir}); err != nil {
		return fmt.Errorf("create accounts dir: %w", err)
	}

	id := normalizeAccountID(creds.ILinkBotID)
	path := filepath.Join(dir, id+".json")

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	if err := sb.FileWrite(&fsmodel.FileWriteRequest{
		File:    path,
		Content: string(data),
		Mode:    0o600,
		Atomic:  true,
	}); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

// LoadAllCredentials returns every saved credential set. A corrupted file is
// skipped silently — only well-formed entries with non-empty BotToken count.
func LoadAllCredentials() ([]*Credentials, error) {
	dir := deeppath.WeChatAccountsDir()
	sb := fileserver.GetFileManager()

	entries, err := sb.FileList(&fsmodel.FileListRequest{Path: dir})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read accounts dir: %w", err)
	}

	var result []*Credentials
	for _, e := range entries.Files {
		if e.IsDir {
			continue
		}
		name := e.Name
		// Skip sync-buf files (from monitor.go) — they share the accounts
		// directory but aren't credential files.
		if strings.HasSuffix(name, ".sync.json") || filepath.Ext(name) != ".json" {
			continue
		}
		readResult, err := sb.FileRead(&fsmodel.FileReadRequest{File: filepath.Join(dir, name)})
		if err != nil {
			// ENOENT is benign — a sibling process / login flow could have
			// removed the file between ReadDir and ReadFile. Anything else
			// (permission flip, NFS hiccup, truncated read) silently dropping
			// credentials would cause the listener to come up with no bots
			// and no visible reason why; log it so ops can find the cause.
			if !errors.Is(err, os.ErrNotExist) {
				logger.Warn("[wechat/auth] read credential file %s failed: %v", name, err)
			}
			continue
		}
		var creds Credentials
		if err := json.Unmarshal([]byte(readResult.Content), &creds); err != nil {
			logger.Warn("[wechat/auth] parse credential file %s failed: %v", name, err)
			continue
		}
		if creds.BotToken != "" && creds.ILinkBotID != "" {
			result = append(result, &creds)
		}
	}
	return result, nil
}

// RemoveCredentials deletes the credential + sync-buf files for a bot ID.
// Missing files are ignored.
func RemoveCredentials(botID string) error {
	if botID == "" {
		return nil
	}

	credentialsMu.Lock()
	defer credentialsMu.Unlock()

	id := normalizeAccountID(botID)
	dir := deeppath.WeChatAccountsDir()
	sb := fileserver.GetFileManager()

	for _, suffix := range []string{".json", ".sync.json"} {
		path := filepath.Join(dir, id+suffix)
		if err := sb.FileDelete(&fsmodel.FileDeleteRequest{Path: path}); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return nil
}
