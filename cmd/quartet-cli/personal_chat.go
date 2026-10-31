package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"

	"github.com/fanlv/quartet/types/model"
	"github.com/google/uuid"
)

func runChatGroup(args []string) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(os.Stdout, "quartet-cli chat <agents|sessions|search|history|delegate|send|wait> [--agent ID] [--harness ID] [--model ID] [--query TEXT] [--session ID] [--parent-run ID] [--content TEXT] [--id STABLE_ID] [--offset N] [--limit N]")
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: quartet-cli chat <agents|sessions|search|history|delegate|send> [flags]")
	}
	command := args[0]
	fs := flag.NewFlagSet("chat "+command, flag.ContinueOnError)
	agent := fs.String("agent", "", "Business Agent ID")
	after := fs.Int64("after", 0, "Earliest accepted message time (Unix milliseconds)")
	before := fs.Int64("before", 0, "Exclusive latest message time (Unix milliseconds)")
	includeTools := fs.Bool("tools", false, "Include tool arguments/results in search or source history")
	query := fs.String("query", "", "Literal search words or phrase")
	session := fs.String("session", "", "Business Session ID")
	message := fs.String("message", "", "Anchor message ID")
	content := fs.String("content", "", "Task or message text")
	parent := fs.String("parent-run", "", "Current execution ID for delegation")
	harness := fs.String("harness", "", "ACP Harness Client ID")
	modelID := fs.String("model", "", "Model ID supported by the harness")
	offset := fs.Int("offset", 0, "Result offset")
	limit := fs.Int("limit", 25, "Maximum results")
	id := fs.String("id", uuid.NewString(), "Stable idempotency identifier (reuse when retrying)")
	fs.Bool("json", true, "Print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	values := url.Values{"agentId": {*agent}, "offset": {strconv.Itoa(*offset)}, "limit": {strconv.Itoa(*limit)}}
	values.Set("tools", strconv.FormatBool(*includeTools))
	var body []byte
	var err error
	switch command {
	case "agents":
		body, err = newClient().do(context.Background(), "GET", "/api/v1/chat/agents", nil)
	case "sessions":
		body, err = newClient().do(context.Background(), "GET", "/api/v1/chat/sessions?"+values.Encode(), nil)
	case "search":
		values.Set("query", *query)
		values.Set("after", strconv.FormatInt(*after, 10))
		values.Set("before", strconv.FormatInt(*before, 10))
		body, err = newClient().do(context.Background(), "GET", "/api/v1/chat/search?"+values.Encode(), nil)
	case "history":
		values.Set("sessionId", *session)
		values.Set("messageId", *message)
		body, err = newClient().do(context.Background(), "GET", "/api/v1/chat/history?"+values.Encode(), nil)
	case "delegate", "send", "wait":
		req := model.PersonalChatSendRequest{ClientMessageID: *id, AgentID: *agent, Content: *content, HarnessID: *harness, ModelID: *modelID, ParentRunID: *parent}
		path := "/api/v1/chat/messages"
		if command == "delegate" {
			path = "/api/v1/chat/delegate"
		} else if command == "wait" {
			path = "/api/v1/chat/wait"
		}
		body, err = newClient().do(context.Background(), "POST", path, req)
	default:
		return fmt.Errorf("unknown chat command %q", command)
	}
	if err != nil {
		return err
	}
	var value any
	if err = json.Unmarshal(body, &value); err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
