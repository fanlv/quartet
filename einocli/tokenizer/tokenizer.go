package tokenizer

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/einocli/types/msgextra"
	"github.com/tiktoken-go/tokenizer"
	_ "golang.org/x/image/webp"
)

const tokenCountExtraKey = msgextra.KeyTextTokenCountCacheV2

const (
	imagePatchSize      = 28
	imageMaxLongEdge    = 2576
	imageMaxPatchTokens = 4784
)

func MessagesTokenCounter(ctx context.Context, msgs []*schema.Message) int {
	var total int
	for _, msg := range msgs {
		total += MessageTokenCounter(ctx, msg)
	}
	return total
}

func MessageTokenCounter(ctx context.Context, msg *schema.Message) int {
	if msg == nil {
		return 0
	}

	textTokens, cached := getCachedTokenCount(msg)
	if !cached {
		var sb strings.Builder
		sb.WriteString(string(msg.Role))
		sb.WriteString("\n")
		sb.WriteString(msg.ReasoningContent)
		sb.WriteString("\n")
		if msg.Content != "" {
			content, _ := leadingLocalImagePaths(msg.Content)
			sb.WriteString(content)
			sb.WriteString("\n")
		} else {
			for _, content := range msg.UserInputMultiContent {
				sb.WriteString(content.Text)
				sb.WriteString("\n")
			}
		}
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				sb.WriteString(tc.Function.Name)
				sb.WriteString("\n")
				sb.WriteString(tc.Function.Arguments)
			}
		}

		var err error
		textTokens, err = estimateTokenCount(sb.String())
		if err != nil {
			textTokens = fallbackEstimateTokenCount(sb.String())
		}
		setCachedTokenCount(msg, textTokens)
	}
	return textTokens + messageImageTokenCount(msg)
}

func messageImageTokenCount(msg *schema.Message) int {
	if msg == nil {
		return 0
	}
	total := 0
	seenPaths := make(map[string]struct{})
	for _, content := range msg.UserInputMultiContent {
		if content.Type != schema.ChatMessagePartTypeImageURL || content.Image == nil {
			continue
		}
		total += imageTokenCount(content.Image)
		if path := localImagePath(content.Image); path != "" {
			seenPaths[path] = struct{}{}
		}
	}
	_, paths := leadingLocalImagePaths(msg.Content)
	for _, path := range paths {
		if _, exists := seenPaths[path]; exists {
			continue
		}
		pathCopy := path
		total += imageTokenCount(&schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Extra: map[string]any{msgextra.KeyLocalPath: pathCopy}}})
		seenPaths[path] = struct{}{}
	}
	return total
}

func leadingLocalImagePaths(content string) (string, []string) {
	rest := content
	var paths []string
	for {
		line, tail, found := strings.Cut(rest, "\n")
		if !found {
			tail = ""
		}
		if !strings.HasPrefix(line, "![image](") || !strings.HasSuffix(line, ")") {
			break
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(line, "![image]("), ")")
		path := localPathFromReference(raw)
		if path == "" {
			break
		}
		paths = append(paths, path)
		rest = tail
		if !found {
			break
		}
	}
	return rest, paths
}

func imageTokenCount(input *schema.MessageInputImage) int {
	reader, closeReader := imageReader(input)
	if reader == nil {
		return 0
	}
	if closeReader != nil {
		defer closeReader()
	}
	config, _, err := image.DecodeConfig(reader)
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0
	}
	width, height := scaledImageDimensions(config.Width, config.Height)
	return ceilDiv(width, imagePatchSize) * ceilDiv(height, imagePatchSize)
}

func imageReader(input *schema.MessageInputImage) (io.Reader, func()) {
	if input == nil {
		return nil, nil
	}
	if input.Base64Data != nil && *input.Base64Data != "" {
		if data, err := base64.StdEncoding.DecodeString(*input.Base64Data); err == nil {
			return bytes.NewReader(data), nil
		}
	}
	path := localImagePath(input)
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	return f, func() { _ = f.Close() }
}

func localImagePath(input *schema.MessageInputImage) string {
	if input.URL != nil && *input.URL != "" {
		if path := localPathFromReference(*input.URL); path != "" {
			return path
		}
	}
	if input.Extra != nil {
		if path, ok := input.Extra[msgextra.KeyLocalPath].(string); ok {
			return strings.TrimSpace(path)
		}
	}
	return ""
}

func localPathFromReference(ref string) string {
	ref = strings.TrimSpace(ref)
	if filepath.IsAbs(ref) {
		return filepath.Clean(ref)
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return ""
	}
	path, err := url.PathUnescape(u.Path)
	if err != nil {
		return ""
	}
	path = filepath.FromSlash(path)
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == filepath.Separator && path[2] == ':' {
		path = path[1:]
	}
	if !filepath.IsAbs(path) {
		return ""
	}
	return filepath.Clean(path)
}

func scaledImageDimensions(width, height int) (int, int) {
	if width <= imageMaxLongEdge && height <= imageMaxLongEdge &&
		ceilDiv(width, imagePatchSize)*ceilDiv(height, imagePatchSize) <= imageMaxPatchTokens {
		return width, height
	}

	// Match Claude's integer target-size solver using the largest supported image
	// budget: orient the image so the long edge is searched, then choose the largest
	// integer width that satisfies both the long-edge and 28px-patch limits. A
	// floating-point area scale can undershoot by a pixel around patch boundaries
	// and produce a different count.
	if height > width {
		scaledHeight, scaledWidth := scaledImageDimensions(height, width)
		return scaledWidth, scaledHeight
	}
	aspect := float64(width) / float64(height)
	low, high := 1, width
	for {
		if low+1 == high {
			return low, max(1, int(math.Round(float64(low)/aspect)))
		}
		candidateWidth := (low + high) / 2
		candidateHeight := max(1, int(math.Round(float64(candidateWidth)/aspect)))
		patches := ceilDiv(candidateWidth, imagePatchSize) * ceilDiv(candidateHeight, imagePatchSize)
		if candidateWidth <= imageMaxLongEdge && patches <= imageMaxPatchTokens {
			low = candidateWidth
		} else {
			high = candidateWidth
		}
	}
}

func ceilDiv(value, divisor int) int {
	return (value + divisor - 1) / divisor
}

func getCachedTokenCount(msg *schema.Message) (int, bool) {
	if msg == nil || msg.Extra == nil {
		return 0, false
	}
	v, ok := msg.Extra[tokenCountExtraKey]
	if !ok {
		return 0, false
	}
	switch vv := v.(type) {
	case int:
		return vv, true
	case int64:
		return int(vv), true
	case float64:
		return int(vv), true
	default:
		return 0, false
	}
}

func setCachedTokenCount(msg *schema.Message, n int) {
	setMessageExtra(msg, tokenCountExtraKey, n)
}

func setMessageExtra[T any](msg *schema.Message, k string, v T) {
	if msg == nil {
		return
	}
	if msg.Extra == nil {
		msg.Extra = map[string]any{}
	}

	newExtra := make(map[string]any, len(msg.Extra)+1)
	for kk, vv := range msg.Extra {
		newExtra[kk] = vv
	}

	newExtra[k] = v
	msg.Extra = newExtra
}

func estimateTokenCount(text string) (int, error) {
	if text == "" {
		return 0, nil
	}

	enc, err := tokenizer.ForModel(tokenizer.GPT4o)
	if err != nil {
		return 0, err
	}

	tokens, _, err := enc.Encode(text)
	if err != nil {
		return 0, err
	}

	return len(tokens), nil

}

func fallbackEstimateTokenCount(text string) int {
	return (len(text) + 3) / 4
}
