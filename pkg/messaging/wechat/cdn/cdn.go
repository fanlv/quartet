// Package cdn talks to the WeChat C2C CDN: encrypts files locally with
// AES-128-ECB, hands them to the upload endpoint, and inverts the flow on the
// download side. It exists as its own package so the listener layer doesn't
// have to carry the iLink-specific crypto helpers, and so tests can target the
// CDN protocol without spinning up the full long-poll listener.
package cdn

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"
)

// BaseURL is declared as a var (not const) so tests can override it
// with an httptest.Server. In production it stays pinned to the live CDN.
var BaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

// maxDownloadBytes caps how much encrypted CDN payload we are willing to
// buffer into memory. WeChat clients cap images at 10 MB and files/videos at
// 25 MB; 50 MiB leaves room for encryption padding/protocol overhead while
// keeping malformed CDN responses from causing excessive memory pressure.
const maxDownloadBytes = 50 * 1024 * 1024 // 50 MiB

// downloadTimeout bounds a single CDN download. Downloads run synchronously
// inside the handler goroutine, holding one of the 32 dispatch sem slots for
// the entire duration; 32 slow downloads would otherwise block the long-poll
// loop (and every other incoming message, including plain text) until the
// timer fires. 20s is generous for WeChat's CDN — a 25 MB video at 2 MB/s
// finishes in ~12s and images/files are almost always much smaller.
const downloadTimeout = 20 * time.Second

// httpClient is reused across CDN upload/download calls so the underlying
// Transport can pool TCP + TLS connections. http.Client is documented as safe
// for concurrent use and is meant to be created once and reused — allocating
// a fresh one per call would throw away the idle connection pool every time.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// UploadedFile holds the result of a CDN upload.
type UploadedFile struct {
	DownloadParam string // encrypted query param for download
	AESKeyHex     string // hex-encoded AES key
	FileSize      int    // plaintext size
	CipherSize    int    // ciphertext size
}

// Upload encrypts and uploads a file to the WeChat CDN.
func Upload(ctx context.Context, client *ilink.Client, data []byte, toUserID string, mediaType int) (*UploadedFile, error) {
	filekey := make([]byte, 16)
	aeskey := make([]byte, 16)
	if _, err := rand.Read(filekey); err != nil {
		return nil, fmt.Errorf("generate filekey: %w", err)
	}
	if _, err := rand.Read(aeskey); err != nil {
		return nil, fmt.Errorf("generate aeskey: %w", err)
	}

	filekeyHex := hex.EncodeToString(filekey)
	aeskeyHex := hex.EncodeToString(aeskey)

	hash := md5.Sum(data)
	rawMD5 := hex.EncodeToString(hash[:])

	cipherSize := PaddedSize(len(data))

	uploadReq := &ilink.GetUploadURLRequest{
		FileKey:     filekeyHex,
		MediaType:   mediaType,
		ToUserID:    toUserID,
		RawSize:     len(data),
		RawFileMD5:  rawMD5,
		FileSize:    cipherSize,
		NoNeedThumb: true,
		AESKey:      aeskeyHex,
		BaseInfo:    ilink.BaseInfo{},
	}

	uploadResp, err := client.GetUploadURL(ctx, uploadReq)
	if err != nil {
		return nil, fmt.Errorf("get upload URL: %w", err)
	}
	if uploadResp.Ret != 0 {
		return nil, fmt.Errorf("get upload URL failed: ret=%d errmsg=%s", uploadResp.Ret, uploadResp.ErrMsg)
	}

	encrypted, err := Encrypt(data, aeskey)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}

	cdnURL := strings.TrimSpace(uploadResp.UploadFullURL)
	if cdnURL == "" {
		if uploadResp.UploadParam == "" {
			return nil, fmt.Errorf("getuploadurl returned no upload URL (need upload_full_url or upload_param)")
		}
		cdnURL = fmt.Sprintf("%s/upload?encrypted_query_param=%s&filekey=%s",
			BaseURL, url.QueryEscape(uploadResp.UploadParam), url.QueryEscape(filekeyHex))
	}

	downloadParam, err := postEncrypted(ctx, encrypted, cdnURL)
	if err != nil {
		return nil, fmt.Errorf("CDN upload: %w", err)
	}

	return &UploadedFile{
		DownloadParam: downloadParam,
		AESKeyHex:     aeskeyHex,
		FileSize:      len(data),
		CipherSize:    cipherSize,
	}, nil
}

// HexStringAsBase64 treats the hex key's ASCII characters as opaque bytes and
// base64-encodes them — i.e. base64("abcdef012345...") → "YWJjZGVmMDEyMzQ1..."
// so the receiver will base64-decode back to the hex string, then hex-decode
// to the 16-byte key. This is the encoding iLink expects for outbound
// file/voice/video AES keys (per ParseAESKey's 32-char-hex branch).
//
// Pair with HexDecodedAsBase64 below; the names spell out the difference
// because the older `aesKeyToBase64` form hid the "treat-as-string" semantics.
func HexStringAsBase64(hexKey string) string {
	return base64.StdEncoding.EncodeToString([]byte(hexKey))
}

// HexDecodedAsBase64 hex-decodes the key first, then base64-encodes the raw
// 16 bytes — i.e. hex → 16 bytes → base64. This is the encoding iLink uses
// for inbound image AES keys (per ParseAESKey's 16-byte branch).
//
// Falls back to the HexStringAsBase64 encoding when the input isn't valid
// hex, matching upstream behaviour: some legacy payloads put the base64
// form directly in the hex field.
func HexDecodedAsBase64(hexKey string) string {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return base64.StdEncoding.EncodeToString([]byte(hexKey))
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// Download downloads and decrypts a file from the WeChat CDN.
func Download(ctx context.Context, encryptQueryParam, aesKeyBase64 string) ([]byte, error) {
	aesKey, err := ParseAESKey(aesKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("parse AES key: %w", err)
	}

	downloadURL := fmt.Sprintf("%s/download?encrypted_query_param=%s",
		BaseURL, url.QueryEscape(encryptQueryParam))

	reqCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download from CDN: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		errMsg := resp.Header.Get("X-Error-Message")
		if errMsg == "" {
			errMsg = string(body)
		}
		return nil, fmt.Errorf("CDN download HTTP %d: %s", resp.StatusCode, errMsg)
	}

	if resp.ContentLength > maxDownloadBytes {
		return nil, fmt.Errorf("CDN response too large: %d bytes (max %d)",
			resp.ContentLength, maxDownloadBytes)
	}

	encrypted, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read CDN response: %w", err)
	}
	if len(encrypted) > maxDownloadBytes {
		return nil, fmt.Errorf("CDN response exceeds %d bytes", maxDownloadBytes)
	}

	return Decrypt(encrypted, aesKey)
}

// ParseAESKey decodes a base64-encoded AES key. Two formats:
//   - base64(raw 16 bytes)           → images (aes_key from media field)
//   - base64(32-char hex string)     → file / voice / video
func ParseAESKey(aesKeyBase64 string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(aesKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	if len(decoded) == 16 {
		return decoded, nil
	}
	if len(decoded) == 32 {
		raw, err := hex.DecodeString(string(decoded))
		if err != nil {
			return nil, fmt.Errorf("hex decode: %w", err)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("unexpected AES key length: %d bytes (expected 16 or 32)", len(decoded))
}

// Decrypt decrypts AES-128-ECB data and strips PKCS7 padding.
//
// SECURITY: ECB mode is insecure for general-purpose encryption because
// identical plaintext blocks produce identical ciphertext blocks. This helper
// exists only for WeChat iLink CDN protocol compatibility. Do not reuse it for
// local storage, tokens, secrets, or user data encryption.
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext is not a multiple of block size")
	}

	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}

	if len(plaintext) == 0 {
		return plaintext, nil
	}
	padLen := int(plaintext[len(plaintext)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(plaintext) {
		return nil, fmt.Errorf("invalid PKCS7 padding: padLen=%d len=%d", padLen, len(plaintext))
	}
	// PKCS7 requires every trailing byte to equal padLen — check all of them
	// so a corrupted ciphertext surfaces here instead of silently returning
	// garbage bytes to downstream consumers.
	for i := len(plaintext) - padLen; i < len(plaintext); i++ {
		if int(plaintext[i]) != padLen {
			return nil, fmt.Errorf("invalid PKCS7 padding: padLen=%d but trailing byte mismatch", padLen)
		}
	}
	return plaintext[:len(plaintext)-padLen], nil
}

func postEncrypted(ctx context.Context, encrypted []byte, cdnURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cdnURL, bytes.NewReader(encrypted))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return "", fmt.Errorf("CDN upload HTTP %d: %s", resp.StatusCode, string(body))
	}

	downloadParam := resp.Header.Get("X-Encrypted-Param")
	if downloadParam == "" {
		return "", fmt.Errorf("CDN upload: missing X-Encrypted-Param header")
	}

	return downloadParam, nil
}

// Encrypt encrypts data using AES-128-ECB with PKCS7 padding.
//
// SECURITY: ECB mode is insecure for general-purpose encryption because
// identical plaintext blocks produce identical ciphertext blocks. This helper
// exists only for WeChat iLink CDN protocol compatibility. Do not reuse it for
// local storage, tokens, secrets, or user data encryption.
func Encrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	padLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	encrypted := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(encrypted[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}

	return encrypted, nil
}

// PaddedSize returns the size of the AES-128-ECB ciphertext for a plaintext of
// the supplied length, including the mandatory PKCS7 padding block.
func PaddedSize(plaintextSize int) int {
	return (plaintextSize/aes.BlockSize + 1) * aes.BlockSize
}
