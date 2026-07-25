package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/einocli/logger"
	"github.com/fanlv/quartet/einocli/types/msgextra"
)

type ChatContextRepo interface {
	LoadAllMessages(ctx context.Context) ([]*schema.Message, error)
	CountMessage(ctx context.Context) (int, error)
	// MessagesFingerprint returns a content-aware identifier for the
	// current state of messages.jsonl. Used by drift checks to detect
	// cross-path mutations that count alone misses (in-place row
	// rewrites — e.g. ReplacePlaceholderToolResult — or hand edits that
	// keep the row count stable but change content). The returned hash
	// is FNV-64 over the on-disk file contents, which is reproducible
	// when nothing has changed and changes whenever any byte does. Empty
	// / missing file yields a zero-valued fingerprint.
	MessagesFingerprint(ctx context.Context) (MessagesFingerprint, error)
	// AppendMessages appends the chat messages to the file.
	AppendMessages(ctx context.Context, msgs []*schema.Message) error
	// ReplaceMessages rewrites the full message history.
	ReplaceMessages(ctx context.Context, msgs []*schema.Message) error
	// ReplacePlaceholderToolResult swaps a previously-persisted placeholder
	// tool_result (matching toolCallID and carrying the placeholder flag)
	// with the real tool result. Used by the round builder when a terminal
	// event arrives AFTER an eager-flush already wrote a [placeholder]
	// superseded row: without this stitch, the placeholder would stay on
	// disk forever and pollute both the history view and the next round's
	// LLM context.
	//
	// Returns stitched=true iff a matching placeholder row was found and
	// replaced. Returns stitched=false (no error) when the placeholder is
	// gone — e.g. summary compaction already folded it into a summary row.
	// Errors surface I/O failures only.
	ReplacePlaceholderToolResult(ctx context.Context, toolCallID string, real *schema.Message) (bool, error)
	// LoadSummaryMessage loads the summary message from the file.
	LoadSummaryMessage(ctx context.Context) (*SummaryMessage, error)
	// SaveSummaryMessage saves the summary message to the file.
	SaveSummaryMessage(ctx context.Context, msg *SummaryMessage) error
	// ClearSummaryMessage removes the persisted summary so that
	// LoadSummaryMessage returns nil on subsequent calls.
	ClearSummaryMessage(ctx context.Context) error
	// WithLock acquires the per-session write lock and runs fn with a
	// LockedRepo handle whose methods bypass the per-call locking. Used
	// by callers that need a multi-step compound operation (e.g.
	// truncate-then-append) to be atomic across other ChatContextRepo
	// instances pointed at the same session directory. Calling the
	// regular ChatContextRepo methods inside fn would deadlock — use the
	// LockedRepo handle.
	//
	// ctx gates the lock acquisition: a Run whose persist deadline has
	// already expired observes ctx.Err() instead of blocking on a
	// contended lock. Once fn starts running, ctx cancellation no
	// longer interrupts the in-flight critical section — fn is
	// expected to honour ctx itself for its own per-step IO.
	WithLock(ctx context.Context, fn func(tx LockedRepo) error) error
}

// MessagesFingerprint identifies the state of messages.jsonl for drift
// detection. Count alone is insufficient — ReplacePlaceholderToolResult
// rewrites a row in place without changing the count, so two distinct
// on-disk states can share the same Count. The Hash captures the full
// byte content so any mutation (in-place rewrite, hand edit, append +
// truncate that nets to the same count) trips the drift check.
//
// The zero value (Count=0, Hash="") represents "no messages on disk"
// and is returned for missing or empty files. Two zero values compare
// equal, so an empty session is correctly diagnosed as "no drift".
type MessagesFingerprint struct {
	Count int    `json:"count,omitempty"`
	Hash  string `json:"hash,omitempty"`
}

// Equal reports whether two fingerprints describe the same on-disk
// state.
func (f MessagesFingerprint) Equal(other MessagesFingerprint) bool {
	return f.Count == other.Count && f.Hash == other.Hash
}

// LockedRepo exposes the unlocked variants of the repo's read/write
// methods. Returned exclusively through ChatContextRepo.WithLock so the
// caller already holds the per-session write lock; the methods here
// must NEVER take the lock themselves or the WithLock holder would
// deadlock against itself.
//
// ctx is threaded for parity with ChatContextRepo so an inner
// operation can cooperate with cancellation (e.g. log breadcrumbs).
// The lock itself stays held for the full WithLock body — fn is the
// natural cancellation boundary.
type LockedRepo interface {
	LoadAllMessages(ctx context.Context) ([]*schema.Message, error)
	LoadSummaryMessage(ctx context.Context) (*SummaryMessage, error)
	AppendMessages(ctx context.Context, msgs []*schema.Message) error
	ReplaceMessages(ctx context.Context, msgs []*schema.Message) error
}

// SummaryMessage represents a summarized conversation with an index
// indicating how many original messages were summarized.
type SummaryMessage struct {
	// Index is the number of original messages that have been summarized.
	Index int `json:"index"`
	// Message is the summary content.
	Message *schema.Message `json:"message"`
}

// isNotFoundError returns true if the error indicates a missing file/path.
func isNotFoundError(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

// maxPersistedToolCallArgs caps how large a single tool call's Arguments
// blob may be when persisted to messages.jsonl. A degenerate model turn
// (e.g. a Write call whose content recursively nests itself) can produce
// tens of MB of arguments; persisting that verbatim makes the history
// endpoint return multi-MB payloads and stalls page load on reload.
const maxPersistedToolCallArgs = 256 * 1024

// toolCallArgsPreviewLen is how many leading bytes of the original
// arguments are kept inside the truncation marker for debugging.
const toolCallArgsPreviewLen = 4 * 1024

// truncatedToolCallArgsMarker replaces an oversized Arguments payload. It
// keeps Arguments valid JSON so history consumers (frontend renderer,
// LLM context rebuild) can still parse it.
type truncatedToolCallArgsMarker struct {
	Truncated     bool   `json:"__quartet_truncated__"`
	OriginalBytes int    `json:"originalBytes"`
	Preview       string `json:"preview"`
}

// sanitizeMessageForPersist truncates oversized tool call arguments in
// place before the message is written to messages.jsonl. Mutating the
// in-memory copy is intentional: anything holding the same pointer
// (session caches, later LLM rounds) then agrees with what is on disk.
func sanitizeMessageForPersist(msg *schema.Message) {
	if msg == nil {
		return
	}
	for i := range msg.ToolCalls {
		args := msg.ToolCalls[i].Function.Arguments
		if len(args) <= maxPersistedToolCallArgs {
			continue
		}
		preview := args
		if len(preview) > toolCallArgsPreviewLen {
			preview = preview[:toolCallArgsPreviewLen]
		}
		markerBytes, err := json.Marshal(truncatedToolCallArgsMarker{
			Truncated:     true,
			OriginalBytes: len(args),
			Preview:       preview,
		})
		if err != nil {
			continue
		}
		logger.Warnf(context.Background(), "[chatContextRepo] truncate oversized tool call arguments (tool=%s id=%s bytes=%d)", msg.ToolCalls[i].Function.Name, msg.ToolCalls[i].ID, len(args))
		msg.ToolCalls[i].Function.Arguments = string(markerBytes)
	}
}

type chatContextRepo struct {
	sessionDir string
}

// NewChatContextRepo creates a ChatContextRepo rooted at sessionDir (e.g.
// ~/.eino/sessions/<sessionID>/), creating the directory if needed.
//
// Concurrency: every method serialises against other ChatContextRepo
// instances pointed at the same sessionDir via the process-wide lock
// registry in session_locks.go. Per-instance mutexes can't help here
// because separate call sites each construct their own instance for the
// same session.
func NewChatContextRepo(sessionDir string) (ChatContextRepo, error) {
	if sessionDir == "" {
		return nil, fmt.Errorf("session dir is empty")
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("mk dir failed: %w", err)
	}

	return &chatContextRepo{sessionDir: sessionDir}, nil
}

// WithLock acquires the per-session write lock for the duration of fn
// and exposes a LockedRepo whose methods skip the per-call locking.
// Returns ctx.Err() if ctx fires before the lock can be acquired.
func (r *chatContextRepo) WithLock(ctx context.Context, fn func(tx LockedRepo) error) error {
	mu := sessionFileLock(r.sessionDir)
	if err := mu.Lock(ctx); err != nil {
		return err
	}
	defer mu.Unlock()
	return fn(&lockedRepoView{r: r})
}

// lockedRepoView is the LockedRepo handed to WithLock callbacks. It
// dispatches to the same underlying file operations as the public
// methods, but skips the per-call lock acquisition (the caller of
// WithLock already holds the write lock).
type lockedRepoView struct{ r *chatContextRepo }

func (v *lockedRepoView) LoadAllMessages(ctx context.Context) ([]*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return v.r.loadAllMessagesLocked()
}
func (v *lockedRepoView) LoadSummaryMessage(ctx context.Context) (*SummaryMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return v.r.loadSummaryMessageLocked()
}
func (v *lockedRepoView) AppendMessages(ctx context.Context, msgs []*schema.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return v.r.appendMessagesLocked(msgs)
}
func (v *lockedRepoView) ReplaceMessages(ctx context.Context, msgs []*schema.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return v.r.replaceMessagesLocked(msgs)
}

// readFile returns the file content as a string. Missing files surface an
// error matching os.ErrNotExist so callers can use isNotFoundError.
func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// fileStat mirrors the FileStat shape the original sandbox returned:
// missing files yield Exists=false with no error.
type fileStatResult struct {
	Exists      bool
	IsDir       bool
	Size        int64
	ModTimeUnix int64
}

func fileStat(path string) (fileStatResult, error) {
	st, err := os.Stat(path)
	if err != nil {
		if isNotFoundError(err) {
			return fileStatResult{}, nil
		}
		return fileStatResult{}, err
	}
	return fileStatResult{
		Exists:      true,
		IsDir:       st.IsDir(),
		Size:        st.Size(),
		ModTimeUnix: st.ModTime().Unix(),
	}, nil
}

// appendJSONLLines appends each line to the file followed by a newline,
// creating the file if needed. Replaces the sandbox JSONLAppendLine call.
func appendJSONLLines(path string, lines []string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return nil
}

// LoadAllMessages reads all chat messages from {sessionDir}/.meta/messages.jsonl
func (r *chatContextRepo) LoadAllMessages(ctx context.Context) ([]*schema.Message, error) {
	mu := sessionFileLock(r.sessionDir)
	if err := mu.RLock(ctx); err != nil {
		return nil, err
	}
	defer mu.RUnlock()
	return r.loadAllMessagesLocked()
}

func (r *chatContextRepo) loadAllMessagesLocked() ([]*schema.Message, error) {
	filePath := MessagesFilePath(r.sessionDir)

	// Cache fast path: if the file's current (size, mtime) matches a cached
	// entry, return the parsed slice without re-reading/re-parsing. FileStat is
	// a cheap syscall; a missing file (Exists=false) skips the cache and falls
	// through to the normal read below, which returns nil for not-found. Runs
	// under the per-session read lock held by the caller, so no concurrent write
	// can change the file between this stat and the read.
	var statSize, statMTime int64
	haveStat := false
	if st, statErr := fileStat(filePath); statErr == nil && st.Exists && !st.IsDir {
		statSize, statMTime = st.Size, st.ModTimeUnix
		haveStat = true
		if cached, ok := globalMessagesCache.get(filePath, statSize, statMTime); ok {
			return cached, nil
		}
	}

	content, err := readFile(filePath)
	if err != nil {
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read messages file failed: %w", err)
	}

	if content == "" {
		return nil, nil
	}

	var messages []*schema.Message
	lines := strings.Split(strings.TrimSpace(content), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var msg schema.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			logger.Warnf(context.Background(), "[chatContextRepo] skip invalid line (len=%d): %v", len(line), err)
			continue
		}
		messages = append(messages, &msg)
	}

	// Populate the cache only when we have a trustworthy file signature from the
	// stat above. Without it (stat failed) we can't safely key the entry, so we
	// skip caching rather than risk serving content under a wrong signature.
	if haveStat {
		globalMessagesCache.put(filePath, statSize, statMTime, messages, estimateMessagesBytes(messages))
	}

	return messages, nil
}

// CountMessage returns the number of well-formed messages on disk.
//
// "Well-formed" means a JSONL line that successfully parses as
// schema.Message. Empty lines and lines that fail to parse are skipped,
// matching the semantics of LoadAllMessages so that drift detection
// (which compares CountMessage against the count returned by a previous
// Run) and replay-by-LoadAllMessages stay in agreement. Using a raw
// line count would diverge from LoadAllMessages the moment a single
// corrupt line existed.
func (r *chatContextRepo) CountMessage(ctx context.Context) (int, error) {
	mu := sessionFileLock(r.sessionDir)
	if err := mu.RLock(ctx); err != nil {
		return 0, err
	}
	defer mu.RUnlock()
	count, _, err := r.countAndHashLocked()
	return count, err
}

// MessagesFingerprint computes the (count, content-hash) tuple under the
// per-session read lock. Count semantics match CountMessage (well-formed
// rows only); hash is FNV-64 over the on-disk bytes so any mutation
// trips it — including in-place row rewrites by
// ReplacePlaceholderToolResult, hand edits via summary.json, and
// transient corruption mid-write. Done under the read lock so a
// concurrent AppendMessages / ReplaceMessages cannot tear the read.
func (r *chatContextRepo) MessagesFingerprint(ctx context.Context) (MessagesFingerprint, error) {
	mu := sessionFileLock(r.sessionDir)
	if err := mu.RLock(ctx); err != nil {
		return MessagesFingerprint{}, err
	}
	defer mu.RUnlock()
	count, hash, err := r.countAndHashLocked()
	if err != nil {
		return MessagesFingerprint{}, err
	}
	return MessagesFingerprint{Count: count, Hash: hash}, nil
}

// countAndHashLocked reads messages.jsonl once and returns both the
// well-formed-message count and an FNV-64 hex hash of the file content.
// Sharing the read across the two derived signals avoids paying for two
// full file reads when the drift check needs both. Caller must hold the
// per-session lock (read or write).
//
// Hash is computed over the trimmed file content (TrimSpace) so trailing
// newline differences from AtomicWriteFile vs. appendJSONLLines do not
// produce spurious drift on otherwise-identical content.
func (r *chatContextRepo) countAndHashLocked() (int, string, error) {
	filePath := MessagesFilePath(r.sessionDir)
	content, err := readFile(filePath)
	if err != nil {
		if isNotFoundError(err) {
			return 0, "", nil
		}
		return 0, "", fmt.Errorf("read messages file failed: %w", err)
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return 0, "", nil
	}
	count := 0
	for _, line := range strings.Split(trimmed, "\n") {
		if line == "" {
			continue
		}
		var msg schema.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// Mirror LoadAllMessages' tolerance: corrupt lines are
			// skipped here too. A single bad line triggering a drift
			// reset (because counts diverged from LoadAllMessages) was
			// a noisier failure mode than treating the row as missing
			// on both sides.
			logger.Warnf(context.Background(), "[chatContextRepo] skip invalid line during count (len=%d): %v", len(line), err)
			continue
		}
		count++
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(trimmed))
	return count, hex.EncodeToString(h.Sum(nil)), nil
}

// AppendMessages appends chat messages to {sessionDir}/.meta/messages.jsonl
func (r *chatContextRepo) AppendMessages(ctx context.Context, msgs []*schema.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	mu := sessionFileLock(r.sessionDir)
	if err := mu.Lock(ctx); err != nil {
		return err
	}
	defer mu.Unlock()
	return r.appendMessagesLocked(msgs)
}

func (r *chatContextRepo) appendMessagesLocked(msgs []*schema.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	if err := r.ensureMetaDir(); err != nil {
		return fmt.Errorf("ensure meta dir failed: %w", err)
	}

	filePath := MessagesFilePath(r.sessionDir)
	JSONStrings := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		sanitizeMessageForPersist(msg)
		jsonBytes, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal message failed: %w", err)
		}
		JSONStrings = append(JSONStrings, string(jsonBytes))
	}
	if err := appendJSONLLines(filePath, JSONStrings); err != nil {
		return fmt.Errorf("append messages failed: %w", err)
	}
	globalMessagesCache.invalidate(filePath)
	return nil
}

// ReplaceMessages rewrites chat messages to {sessionDir}/.meta/messages.jsonl
// Uses atomic write (write-to-temp + rename) so a crash mid-write cannot corrupt the file.
func (r *chatContextRepo) ReplaceMessages(ctx context.Context, msgs []*schema.Message) error {
	mu := sessionFileLock(r.sessionDir)
	if err := mu.Lock(ctx); err != nil {
		return err
	}
	defer mu.Unlock()
	return r.replaceMessagesLocked(msgs)
}

func (r *chatContextRepo) replaceMessagesLocked(msgs []*schema.Message) error {
	if err := r.ensureMetaDir(); err != nil {
		return fmt.Errorf("ensure meta dir failed: %w", err)
	}

	filePath := MessagesFilePath(r.sessionDir)
	jsonStrings := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		sanitizeMessageForPersist(msg)
		jsonBytes, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal message failed: %w", err)
		}
		jsonStrings = append(jsonStrings, string(jsonBytes))
	}

	content := strings.Join(jsonStrings, "\n")
	if len(content) > 0 {
		content += "\n"
	}

	if err := AtomicWriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("replace messages failed: %w", err)
	}
	globalMessagesCache.invalidate(filePath)
	return nil
}

// ReplacePlaceholderToolResult finds a role=tool row whose ToolCallID ==
// toolCallID AND whose Extra[KeyPlaceholderToolResult] is true, and
// rewrites that row with `real`. Non-placeholder rows (real results or
// unrelated rows) are left alone even if ToolCallID happens to match, so
// this is safe to call even after summary compaction moved things around.
//
// The rewrite is atomic: it reuses the same tempfile + rename path as
// ReplaceMessages, and the read+write pair runs under the per-session
// write lock so a concurrent AppendMessages cannot interleave between
// load and replace.
//
// Bad-line handling: the function operates on raw JSONL lines. Lines
// that fail to parse as schema.Message are scanned but left untouched
// — never re-marshaled, never dropped — so that a localized JSON
// corruption cannot be silently amplified into permanent data loss when
// stitching lands.
func (r *chatContextRepo) ReplacePlaceholderToolResult(ctx context.Context, toolCallID string, real *schema.Message) (bool, error) {
	if toolCallID == "" || real == nil {
		return false, nil
	}

	mu := sessionFileLock(r.sessionDir)
	if err := mu.Lock(ctx); err != nil {
		return false, err
	}
	defer mu.Unlock()

	filePath := MessagesFilePath(r.sessionDir)
	content, err := readFile(filePath)
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("read messages file failed: %w", err)
	}
	if content == "" {
		return false, nil
	}

	// Operate on raw lines so unparseable rows can pass through verbatim.
	rawLines := strings.Split(strings.TrimSpace(content), "\n")
	replaceIdx := -1
	var oldExtra map[string]any
	for i, line := range rawLines {
		if line == "" {
			continue
		}
		var msg schema.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// Tolerate corruption: skip the line for matching purposes
			// but preserve it in the output. Rewriting the whole file
			// from only-the-parsed messages used to permanently delete
			// the bad row, which made one localized failure cascade
			// into history loss.
			logger.Warnf(context.Background(), "[chatContextRepo] preserve invalid line during stitch (idx=%d len=%d): %v", i, len(line), err)
			continue
		}
		if msg.Role == schema.Tool && msg.ToolCallID == toolCallID && isPlaceholderTool(&msg) {
			replaceIdx = i
			oldExtra = msg.Extra
			break
		}
	}
	if replaceIdx < 0 {
		return false, nil
	}

	// Preserve the placeholder's timing window if the caller didn't supply
	// its own — the placeholder carried started_at/finished_at from the
	// original round, and losing them would make the history reload drop
	// the duration badge on the stitched tool bubble.
	merged := real
	if merged.Extra == nil {
		merged.Extra = map[string]any{}
	}
	if oldExtra != nil {
		if _, ok := merged.Extra[msgextra.KeyStartedAt]; !ok {
			if v, ok := oldExtra[msgextra.KeyStartedAt]; ok {
				merged.Extra[msgextra.KeyStartedAt] = v
			}
		}
		if _, ok := merged.Extra[msgextra.KeyFinishedAt]; !ok {
			if v, ok := oldExtra[msgextra.KeyFinishedAt]; ok {
				merged.Extra[msgextra.KeyFinishedAt] = v
			}
		}
	}
	mergedBytes, err := json.Marshal(merged)
	if err != nil {
		return false, fmt.Errorf("marshal stitched message failed: %w", err)
	}
	rawLines[replaceIdx] = string(mergedBytes)

	if err := r.ensureMetaDir(); err != nil {
		return false, fmt.Errorf("ensure meta dir failed: %w", err)
	}
	newContent := strings.Join(rawLines, "\n")
	if len(newContent) > 0 {
		newContent += "\n"
	}
	if err := AtomicWriteFile(filePath, []byte(newContent), 0644); err != nil {
		return false, fmt.Errorf("rewrite messages file failed: %w", err)
	}
	globalMessagesCache.invalidate(filePath)
	return true, nil
}

// isPlaceholderTool reports whether a role=tool message is a synthetic
// placeholder (written when a round flushed without a real terminal).
func isPlaceholderTool(m *schema.Message) bool {
	if m == nil || m.Extra == nil {
		return false
	}
	v, ok := m.Extra[msgextra.KeyPlaceholderToolResult]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// ensureMetaDir ensures dir exists: {sessionDir}/.meta/
func (r *chatContextRepo) ensureMetaDir() error {
	return os.MkdirAll(MetaDir(r.sessionDir), 0o755)
}

// LoadSummaryMessage reads summary from {sessionDir}/.meta/summary.json
func (r *chatContextRepo) LoadSummaryMessage(ctx context.Context) (*SummaryMessage, error) {
	mu := sessionFileLock(r.sessionDir)
	if err := mu.RLock(ctx); err != nil {
		return nil, err
	}
	defer mu.RUnlock()
	return r.loadSummaryMessageLocked()
}

func (r *chatContextRepo) loadSummaryMessageLocked() (*SummaryMessage, error) {
	filePath := SummaryFilePath(r.sessionDir)

	content, err := readFile(filePath)
	if err != nil {
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read summary file failed: %w", err)
	}

	if content == "" {
		return nil, nil
	}

	var msg SummaryMessage
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return nil, fmt.Errorf("unmarshal summary message failed: %w", err)
	}

	return &msg, nil
}

// SaveSummaryMessage writes summary to {sessionDir}/.meta/summary.json
func (r *chatContextRepo) SaveSummaryMessage(ctx context.Context, msg *SummaryMessage) error {
	if msg == nil {
		return nil
	}

	// Serialise against Append / Replace / ClearSummary so a summary write
	// can't interleave with a chat-log rewrite of the same session.
	mu := sessionFileLock(r.sessionDir)
	if err := mu.Lock(ctx); err != nil {
		return err
	}
	defer mu.Unlock()

	if err := r.ensureMetaDir(); err != nil {
		return fmt.Errorf("ensure meta dir failed: %w", err)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal summary message failed: %w", err)
	}

	filePath := SummaryFilePath(r.sessionDir)
	if err := AtomicWriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("write summary file failed: %w", err)
	}

	return nil
}

// ClearSummaryMessage deletes the summary file so that subsequent loads return nil.
func (r *chatContextRepo) ClearSummaryMessage(ctx context.Context) error {
	mu := sessionFileLock(r.sessionDir)
	if err := mu.Lock(ctx); err != nil {
		return err
	}
	defer mu.Unlock()

	filePath := SummaryFilePath(r.sessionDir)
	// os.RemoveAll returns nil for non-existent paths, matching the local
	// sandbox semantics of the original FileDelete call.
	if err := os.RemoveAll(filePath); err != nil {
		return fmt.Errorf("delete summary file failed: %w", err)
	}
	return nil
}
