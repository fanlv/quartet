package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/fanlv/quartet/pkg/logger"

	acptransport "github.com/eino-contrib/acp/transport"
)

// fixLineTypeTransport wraps an ACP Transport and repairs known type
// mismatches in inbound JSON-RPC messages before they reach the SDK's
// strict unmarshaller.
//
// Known issue: some ACP agent subprocesses serialize ToolCallLocation.line
// as a JSON string (e.g. "42") instead of a number. The SDK expects *int64,
// causing the entire session/update notification to be dropped silently.
// This wrapper converts any string-typed "line" values within "locations"
// arrays to integers in-place.
type fixLineTypeTransport struct {
	inner     acptransport.Transport
	agentType string
}

func newFixLineTypeTransport(inner acptransport.Transport, agentType string) acptransport.Transport {
	return &fixLineTypeTransport{inner: inner, agentType: agentType}
}

func (t *fixLineTypeTransport) ReadMessage(ctx context.Context) (json.RawMessage, error) {
	start := time.Now()
	msg, err := t.inner.ReadMessage(ctx)
	if err != nil {
		return msg, err
	}
	elapsed := time.Since(start)
	// Log when ReadMessage blocks for a long time — indicates the subprocess
	// is slow to produce output or there's pipe-level backpressure. This helps
	// pinpoint whether "deliveryStall" delays originate from the subprocess I/O
	// layer vs. the SDK's internal ordered queue.
	//
	// In agent long-thinking / long-tool-execution scenarios, waits of several
	// minutes are expected. Use INFO (not WARN) to avoid polluting alertable
	// log channels, and skip message preview to reduce noise.
	if elapsed > 120*time.Second {
		logger.Infof(ctx, "[ACP-transport] ReadMessage blocked %v waiting for subprocess output (expected during long thinking/tool-exec): agentType=%s msgLen=%d", elapsed, t.agentType, len(msg))
	} else if elapsed > 5*time.Second {
		logger.Debugf(ctx, "[ACP-transport] ReadMessage waited %v for next message (normal during extended thinking): agentType=%s", elapsed, t.agentType)
	}
	// Fast path: only process messages that look like session/update
	// notifications (contain "locations" with a string "line" value).
	if bytes.Contains(msg, []byte(`"locations"`)) {
		msg = fixLocationsLineType(msg)
	}
	return msg, nil
}

func (t *fixLineTypeTransport) WriteMessage(ctx context.Context, data json.RawMessage) error {
	return t.inner.WriteMessage(ctx, data)
}

func (t *fixLineTypeTransport) Close() error {
	return t.inner.Close()
}

// fixLocationsLineType finds "line":"<number>" patterns inside "locations"
// arrays in session/update notifications and rewrites them to "line":<number>.
func fixLocationsLineType(msg json.RawMessage) json.RawMessage {
	var envelope struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(msg, &envelope); err != nil {
		return msg
	}
	// Only fix session/update notifications
	if envelope.Method != "session/update" {
		return msg
	}

	// Parse the full message as a generic map to preserve all fields
	var full map[string]any
	if err := json.Unmarshal(msg, &full); err != nil {
		return msg
	}

	params, ok := full["params"].(map[string]any)
	if !ok {
		return msg
	}

	update, ok := params["update"].(map[string]any)
	if !ok {
		return msg
	}

	// The ToolCall update type has locations at the top level
	fixed := fixLocationsInMap(update)

	// Also check nested content structures
	if content, ok := update["content"].(map[string]any); ok {
		fixed = fixLocationsInMap(content) || fixed
	}

	if !fixed {
		return msg
	}

	out, err := json.Marshal(full)
	if err != nil {
		return msg
	}
	return out
}

// fixLocationsInMap looks for a "locations" array in the given map and
// converts any string "line" values to integers. Returns true if any fix
// was applied.
func fixLocationsInMap(m map[string]any) bool {
	locs, ok := m["locations"]
	if !ok {
		return false
	}
	locsArr, ok := locs.([]any)
	if !ok {
		return false
	}

	fixed := false
	for _, loc := range locsArr {
		locMap, ok := loc.(map[string]any)
		if !ok {
			continue
		}
		lineVal, exists := locMap["line"]
		if !exists {
			continue
		}
		// If line is already a number (float64 from JSON), no fix needed
		if _, ok := lineVal.(float64); ok {
			continue
		}
		// If line is a string, try to convert to int
		if s, ok := lineVal.(string); ok {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				locMap["line"] = n
				fixed = true
			} else {
				// ParseInt failed (empty string, "33:1", etc.) — fallback to 0
				// to prevent SDK unmarshal failure on string-typed line field
				logger.Warnf(context.Background(), "[ACP-transport-fix] locations.line ParseInt failed, fallback to 0: raw=%q err=%v", s, err)
				locMap["line"] = int64(0)
				fixed = true
			}
		}
	}
	return fixed
}
