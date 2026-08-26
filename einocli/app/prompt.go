package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	acp "github.com/eino-contrib/acp"
	"github.com/fanlv/quartet/einocli/config"
	einojson "github.com/fanlv/quartet/einocli/json"
	"github.com/fanlv/quartet/einocli/logger"
	"github.com/fanlv/quartet/einocli/modelbuilder"
	"github.com/fanlv/quartet/einocli/runtime"
	"github.com/fanlv/quartet/einocli/types/msgextra"
	"github.com/google/uuid"
)

// maxImageBytes caps a single image read. Anything larger is treated as
// unreadable: the tag stays in the text (silent degrade).
const maxImageBytes = 32 * 1024 * 1024

// imageTagRe matches the `![image](<path>)` tags the quartet client embeds in
// prompt text. The captured path is trimmed before use.
var imageTagRe = regexp.MustCompile(`!\[image\]\(([^)]+)\)`)

const promptErrorUsageKey = "quartetPromptUsage"

// Prompt runs one prompt turn: build the user message (text + images),
// get-or-create the session runtime, stream the run as session/update
// notifications, and report the terminal stop reason.
func (a *Agent) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	st, err := a.getOrLoadState(string(req.SessionID))
	if err != nil {
		return acp.PromptResponse{}, err
	}

	text := extractPromptText(req.Prompt)
	msg := buildUserMessage(text)

	rt, err := a.runtimeFor(ctx, st)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	// Run under a cancellable child ctx: session/cancel (a notification that
	// arrives concurrently on another dispatch worker) fires promptCancel,
	// which cancels the run; we then answer stopReason=cancelled. A client
	// disconnect cancels ctx itself and lands on the same path.
	promptCtx, cancel := context.WithCancel(ctx)
	st.mu.Lock()
	st.promptCancel = cancel
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		st.promptCancel = nil
		st.mu.Unlock()
		cancel()
	}()

	// The translator's send ctx is detached from promptCtx so terminal cleanup
	// events (interrupted placeholders, final usage) still flow after cancel.
	translator := newEventTranslator(context.WithoutCancel(promptCtx), a.agentConn, st.meta.SessionID)

	usage, runErr := rt.RunWithUsage(promptCtx, []*schema.Message{msg}, translator)
	promptUsage, usageErr := addSessionPromptUsage(st, usage)
	if promptCtx.Err() != nil || errors.Is(runErr, context.Canceled) {
		if usageErr != nil {
			logger.Errorf(context.WithoutCancel(ctx), "[acp] persist cumulative usage after cancelled prompt failed: session=%s err=%v", st.meta.SessionID, usageErr)
		}
		return acp.PromptResponse{
			StopReason: acp.StopReasonCancelled,
			Usage:      promptUsage,
		}, nil
	}
	if runErr != nil {
		if usageErr != nil {
			runErr = fmt.Errorf("%w; additionally, %v", runErr, usageErr)
		}
		// Full error text: the client shows it to the user verbatim.
		// JSON-RPC error responses cannot also carry PromptResponse.Usage. Put
		// any partial provider accounting in error.data so the Quartet ACP
		// client can recover it without weakening or replacing the original
		// error message. Other ACP clients safely ignore this vendor field.
		if promptUsage != nil {
			return acp.PromptResponse{}, acp.ErrInternalError(runErr.Error(), map[string]any{
				promptErrorUsageKey: promptUsage,
			})
		}
		return acp.PromptResponse{}, acp.ErrInternalError(runErr.Error(), nil)
	}
	if usageErr != nil {
		return acp.PromptResponse{}, acp.ErrInternalError(usageErr.Error(), map[string]any{
			promptErrorUsageKey: promptUsage,
		})
	}
	return acp.PromptResponse{
		StopReason: acp.StopReasonEndTurn,
		Usage:      promptUsage,
	}, nil
}

func addSessionPromptUsage(st *sessionState, usage *runtime.ProviderUsage) (*acp.Usage, error) {
	if usage == nil {
		return nil, nil
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	previousPersisted := st.meta.ProviderUsage
	previous := previousPersisted
	if previous == nil {
		previous = &sessionProviderUsage{}
	}
	next := &sessionProviderUsage{
		InputTokens:      saturatingUsageAdd(previous.InputTokens, usage.InputTokens),
		OutputTokens:     saturatingUsageAdd(previous.OutputTokens, usage.OutputTokens),
		TotalTokens:      saturatingUsageAdd(previous.TotalTokens, usage.TotalTokens),
		CachedReadTokens: saturatingUsageAdd(previous.CachedReadTokens, usage.CachedReadTokens),
		ThoughtTokens:    saturatingUsageAdd(previous.ThoughtTokens, usage.ThoughtTokens),
	}
	st.meta.ProviderUsage = next
	st.meta.UpdatedAt = time.Now().Unix()
	promptUsage := &acp.Usage{
		InputTokens:      next.InputTokens,
		OutputTokens:     next.OutputTokens,
		TotalTokens:      next.TotalTokens,
		CachedReadTokens: optionalCount(next.CachedReadTokens),
		ThoughtTokens:    optionalCount(next.ThoughtTokens),
	}
	if err := writeMetaLocked(st.dir, st.meta); err != nil {
		// Keep the in-memory counter monotonic so a transient disk failure does
		// not make the next response go backwards. The caller still surfaces
		// the persistence error, while returning this cumulative sample in
		// error.data so Quartet can advance its per-session cursor exactly once.
		return promptUsage, fmt.Errorf("persist cumulative prompt usage failed: %w", err)
	}
	return promptUsage, nil
}

func saturatingUsageAdd(left, right int64) int64 {
	left = max(0, left)
	right = max(0, right)
	if left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func optionalCount(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

// extractPromptText concatenates the text of every text block, ignoring
// non-text blocks.
func extractPromptText(blocks []acp.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if t, ok := b.AsText(); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

// buildUserMessage turns the raw prompt text into the eino user message.
// Readable image tags are stripped from the text and become image parts
// (text part last); unreadable tags stay in the text untouched.
func buildUserMessage(text string) *schema.Message {
	text, imageParts := extractImageParts(text)

	var msg *schema.Message
	if len(imageParts) > 0 {
		if strings.TrimSpace(text) == "" {
			text = "[image]"
		}
		parts := append(imageParts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: text,
		})
		msg = &schema.Message{Role: schema.User, UserInputMultiContent: parts}
	} else {
		msg = schema.UserMessage(text)
	}

	// Stable id + receive timestamp, mirroring cmd/web/handler/job_message.go
	// so history reload renders the same identity the live stream used.
	msg.Extra = map[string]any{
		msgextra.KeyMsgID:     uuid.NewString(),
		msgextra.KeyStartedAt: time.Now().UnixMilli(),
	}
	return msg
}

// extractImageParts finds every `![image](<path>)` tag, reads each file, and
// returns the remaining text plus one image part per readable file. A file
// that fails to read leaves its tag EXACTLY as-is in the text (silent
// degrade — never an error). When a readable tag sits alone on its line, the
// now-empty line is collapsed.
func extractImageParts(text string) (string, []schema.MessageInputPart) {
	matches := imageTagRe.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}

	// Process matches in reverse so earlier match indices stay valid as the
	// text shrinks.
	parts := make([]schema.MessageInputPart, 0, len(matches))
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		path := strings.TrimSpace(text[m[2]:m[3]])
		part, ok := readImagePart(path)
		if !ok {
			continue
		}

		start, end := m[0], m[1]
		// Collapse the now-empty line when the tag is the line's only
		// content: remove the whole line plus one of its newlines.
		lineStart := strings.LastIndexByte(text[:start], '\n') + 1
		lineEnd := len(text)
		if nl := strings.IndexByte(text[end:], '\n'); nl >= 0 {
			lineEnd = end + nl
		}
		if strings.TrimSpace(text[lineStart:start]) == "" && strings.TrimSpace(text[end:lineEnd]) == "" {
			start = lineStart
			end = lineEnd
			if end < len(text) && text[end] == '\n' {
				end++
			} else if start > 0 && text[start-1] == '\n' {
				start--
			}
		}
		text = text[:start] + text[end:]
		parts = append(parts, part)
	}

	// Reverse-order processing appended parts back-to-front.
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return text, parts
}

// readImagePart reads path and builds the image input part (base64 data +
// sniffed MIME + the local path in Extra, mirroring the quartet handler's
// multi-content construction).
func readImagePart(path string) (schema.MessageInputPart, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > maxImageBytes {
		return schema.MessageInputPart{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return schema.MessageInputPart{}, false
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return schema.MessageInputPart{
		Type: schema.ChatMessagePartTypeImageURL,
		Image: &schema.MessageInputImage{
			MessagePartCommon: schema.MessagePartCommon{
				Base64Data: &encoded,
				MIMEType:   sniffImageMIME(path, data),
				Extra:      map[string]any{msgextra.KeyLocalPath: path},
			},
		},
	}, true
}

// sniffImageMIME sniffs the content type from the first 512 bytes, falling
// back to the extension for formats the sniffer does not know, defaulting to
// image/png.
func sniffImageMIME(path string, data []byte) string {
	const sniffLen = 512
	head := data
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	if ct := http.DetectContentType(head); strings.HasPrefix(ct, "image/") {
		return ct
	}
	ext := ""
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		ext = strings.ToLower(path[i:])
	}
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	}
	return "image/png"
}

// runtimeKeyInput is the fingerprint input for the cached runtime. The eino
// runtime binds its chat model, workdir and system prompt at New() time, so
// any change here must rebuild it.
type runtimeKeyInput struct {
	Workdir      string                    `json:"workdir"`
	ModelCfg     *modelbuilder.ModelConfig `json:"model_cfg"`
	SystemPrompt string                    `json:"system_prompt"`
}

// runtimeFor returns the session's cached runtime, building it on first use
// and rebuilding it when the fingerprint of its inputs (workdir + model
// config + system prompt) no longer matches — typically after a model or
// thought_level switch. A fingerprint mismatch against a still-running
// runtime is rejected: the caller retries after the turn finishes.
func (a *Agent) runtimeFor(ctx context.Context, st *sessionState) (*runtime.Agent, error) {
	modelCfg, err := modelConfigFor(st)
	if err != nil {
		return nil, err
	}
	systemPrompt, err := config.GetSystemPrompt()
	if err != nil {
		return nil, acp.ErrInternalError(fmt.Sprintf("load system prompt failed: %v", err), nil)
	}

	key := einojson.String(runtimeKeyInput{Workdir: st.meta.Cwd, ModelCfg: modelCfg, SystemPrompt: systemPrompt})

	// Held across the check-and-create so a concurrent prompt on the same
	// session cannot build two runtimes. Runtime construction is one-time
	// per key, and prompts on one session are client-serialized, so the
	// hold window is acceptable.
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.rt != nil && st.rtKey == key {
		return st.rt, nil
	}
	if st.rt != nil {
		if st.rt.IsRunning() {
			return nil, acp.ErrInternalError("session is running; retry after it finishes", nil)
		}
		st.rt.Close()
		st.rt = nil
		st.rtKey = ""
	}

	rt, err := runtime.New(ctx, st.meta.Cwd, modelCfg,
		runtime.WithSessionID(st.meta.SessionID),
		runtime.WithSessionDir(st.dir),
		runtime.WithSystemPrompt(systemPrompt),
		runtime.WithSessionToucher(metaToucher{st: st}),
	)
	if err != nil {
		return nil, acp.ErrInternalError(err.Error(), nil)
	}
	st.rt = rt
	st.rtKey = key
	logger.Infof(ctx, "[acp] runtime built: session=%s model=%s", st.meta.SessionID, st.meta.ModelID)
	return rt, nil
}

// modelConfigFor resolves the session's model selection into a ModelConfig.
// The session-level thought_level override wins over the model's own
// thinking_type.
func modelConfigFor(st *sessionState) (*modelbuilder.ModelConfig, error) {
	st.mu.Lock()
	modelID := st.meta.ModelID
	override := st.meta.ThinkingOverride
	st.mu.Unlock()

	if modelID == "" {
		return nil, acp.ErrInternalError("no model configured for session; run `eino-cli models add` or select a model", nil)
	}
	m, err := config.GetModel(modelID)
	if err != nil {
		return nil, acp.ErrInternalError(fmt.Sprintf("no model configured for session (model %q not in catalog); run `eino-cli models add` or select a model", modelID), nil)
	}
	return m.ToModelConfig(override), nil
}
