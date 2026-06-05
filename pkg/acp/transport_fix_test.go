package acp

import (
	"encoding/json"
	"testing"
)

func TestFixLocationsLineType_StringToInt(t *testing.T) {
	// Simulates a session/update notification with "line" as string
	msg := json.RawMessage(`{
		"jsonrpc": "2.0",
		"method": "session/update",
		"params": {
			"sessionId": "abc",
			"update": {
				"type": "tool_call",
				"toolCallId": "tc_1",
				"title": "read_file",
				"locations": [
					{"path": "/foo/bar.go", "line": "42"}
				]
			}
		}
	}`)

	result := fixLocationsLineType(msg)

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	params := parsed["params"].(map[string]any)
	update := params["update"].(map[string]any)
	locs := update["locations"].([]any)
	loc := locs[0].(map[string]any)
	line := loc["line"]

	// After fix, line should be a number (float64 in Go's JSON)
	lineNum, ok := line.(float64)
	if !ok {
		t.Fatalf("expected line to be float64, got %T: %v", line, line)
	}
	if lineNum != 42 {
		t.Fatalf("expected line=42, got %v", lineNum)
	}
}

func TestFixLocationsLineType_AlreadyInt(t *testing.T) {
	msg := json.RawMessage(`{
		"jsonrpc": "2.0",
		"method": "session/update",
		"params": {
			"sessionId": "abc",
			"update": {
				"type": "tool_call",
				"toolCallId": "tc_1",
				"title": "read_file",
				"locations": [
					{"path": "/foo/bar.go", "line": 42}
				]
			}
		}
	}`)

	result := fixLocationsLineType(msg)

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	params := parsed["params"].(map[string]any)
	update := params["update"].(map[string]any)
	locs := update["locations"].([]any)
	loc := locs[0].(map[string]any)
	line := loc["line"]

	lineNum, ok := line.(float64)
	if !ok {
		t.Fatalf("expected line to be float64, got %T: %v", line, line)
	}
	if lineNum != 42 {
		t.Fatalf("expected line=42, got %v", lineNum)
	}
}

func TestFixLocationsLineType_NotSessionUpdate(t *testing.T) {
	msg := json.RawMessage(`{
		"jsonrpc": "2.0",
		"method": "other/method",
		"params": {"locations": [{"line": "99"}]}
	}`)

	result := fixLocationsLineType(msg)

	// Should not be modified
	if string(result) != string(msg) {
		t.Fatalf("non session/update message should not be modified")
	}
}

func TestFixLocationsLineType_NoLocations(t *testing.T) {
	msg := json.RawMessage(`{
		"jsonrpc": "2.0",
		"method": "session/update",
		"params": {
			"sessionId": "abc",
			"update": {"type": "agent_message_chunk", "content": {"type": "text", "text": "hello"}}
		}
	}`)

	result := fixLocationsLineType(msg)

	// Should not be modified (no locations field)
	var original, modified map[string]any
	json.Unmarshal(msg, &original)
	json.Unmarshal(result, &modified)

	origBytes, _ := json.Marshal(original)
	modBytes, _ := json.Marshal(modified)
	if string(origBytes) != string(modBytes) {
		t.Fatalf("message without locations should not be modified")
	}
}
