package middlewares

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// hasFailureEntry returns true iff the registry holds at least one entry —
// the tool_wrap tests only ever store under a single callID, so any
// entry indicates the failure leak the fix targets.
func hasFailureEntry(m *sync.Map) bool {
	found := false
	m.Range(func(k, v any) bool {
		found = true
		return false
	})
	return found
}

// TestWrapInvokableToolCall_UnrecoverableError_DoesNotRecord proves the
// fix for the toolFailures leak: when an invokable tool returns
// context.Canceled or context.DeadlineExceeded, the middleware returns
// the error directly without producing a tool terminal — so the round
// adapter's LoadAndDelete will never run for that callID. Recording
// the failure here would leak the entry forever in the agent-level
// sync.Map. The middleware must skip recordFailure on these errors.
func TestWrapInvokableToolCall_UnrecoverableError_DoesNotRecord(t *testing.T) {
	cases := []struct {
		name          string
		err           error
		unrecoverable bool
	}{
		{name: "canceled", err: context.Canceled, unrecoverable: true},
		{name: "deadline exceeded", err: context.DeadlineExceeded, unrecoverable: true},
		// Non-canonical wrapping (no errors.Is chain) — control case to
		// confirm a regular error string still goes through the record
		// path even if it textually mentions cancel.
		{name: "non-canonical text", err: errors.New("rpc: context canceled"), unrecoverable: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failures := &sync.Map{}
			mw := &toolWrapMiddleware{failures: failures}

			endpoint := func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
				return "", tc.err
			}

			wrapped, err := mw.WrapInvokableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: "shell", CallID: "call-1"})
			if err != nil {
				t.Fatalf("WrapInvokableToolCall: %v", err)
			}

			result, callErr := wrapped(context.Background(), "{}")

			if tc.unrecoverable {
				// Unrecoverable err must propagate as the call's error,
				// not be folded into result text — otherwise the LLM
				// sees "context canceled" as a tool result and may try
				// to retry around it.
				if !errors.Is(callErr, tc.err) {
					t.Fatalf("unrecoverable err must propagate; got result=%q err=%v", result, callErr)
				}
				if hasFailureEntry(failures) {
					t.Fatalf("toolFailures must NOT record the callID for unrecoverable errors — adapter never gets a terminal to consume it, would leak in long-lived agent")
				}
			} else {
				// Recoverable err: folded into result so the LLM can
				// self-recover, and recorded so the adapter can mark
				// the terminal as Failed at consume time.
				if callErr != nil {
					t.Fatalf("recoverable err must be folded into result, got err=%v", callErr)
				}
				if result != tc.err.Error() {
					t.Fatalf("result must equal err.Error(); got %q", result)
				}
				if !hasFailureEntry(failures) {
					t.Fatalf("recoverable err must record the callID so the round adapter can downgrade Completed to Failed at terminal time")
				}
			}
		})
	}
}

// TestWrapStreamableToolCall_UnrecoverableError_DoesNotRecord mirrors
// the invokable test for the streamable variant; both share the same
// recordFailure-then-return-err pattern and both must skip the record
// for unrecoverable errors.
func TestWrapStreamableToolCall_UnrecoverableError_DoesNotRecord(t *testing.T) {
	failures := &sync.Map{}
	mw := &toolWrapMiddleware{failures: failures}

	endpoint := func(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		return nil, context.Canceled
	}

	wrapped, err := mw.WrapStreamableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: "stream-tool", CallID: "call-stream"})
	if err != nil {
		t.Fatalf("WrapStreamableToolCall: %v", err)
	}

	stream, callErr := wrapped(context.Background(), "{}")
	if !errors.Is(callErr, context.Canceled) {
		t.Fatalf("want context.Canceled to propagate, got stream=%v err=%v", stream, callErr)
	}
	if hasFailureEntry(failures) {
		t.Fatalf("toolFailures must NOT record for unrecoverable streamable errors")
	}
}

// TestWrapInvokableToolCall_RecoverableError_StillRecords is the
// behaviour-preservation guard: ordinary tool errors must still go into
// the registry so the round adapter can render them as failed (red)
// terminals instead of a green "Completed" bubble.
func TestWrapInvokableToolCall_RecoverableError_StillRecords(t *testing.T) {
	failures := &sync.Map{}
	mw := &toolWrapMiddleware{failures: failures}

	toolErr := errors.New("file not found")
	endpoint := func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		return "", toolErr
	}

	wrapped, err := mw.WrapInvokableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: "read", CallID: "call-2"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall: %v", err)
	}

	result, callErr := wrapped(context.Background(), "{}")
	if callErr != nil {
		t.Fatalf("recoverable err must be folded into result text, not returned: result=%q err=%v", result, callErr)
	}
	if result != toolErr.Error() {
		t.Fatalf("result must equal err.Error(); got %q", result)
	}

	_, ok := failures.Load("call-2")
	if !ok {
		t.Fatalf("recoverable err must record the callID so the round adapter can downgrade Completed to Failed at terminal time")
	}
}
