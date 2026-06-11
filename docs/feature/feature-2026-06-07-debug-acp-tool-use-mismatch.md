# Debug Session: acp-tool-use-mismatch

Status: [OPEN]

## Symptom

`acp prompt failed: rpc error -32603: Internal error: API Error: 400 due to tool use concurrency issues., data: {"errorKind":"invalid_request"}`

The error recurs during ACP-backed Claude Code runs.

## Hypotheses

1. Quartet sends overlapping `session/prompt` requests on the same ACP connection, causing upstream tool-use state corruption.
2. Quartet records or replays malformed tool-call history into ACP, causing Claude Code to send invalid `tool_use` / `tool_result` sequences.
3. The `github.com/eino-contrib/acp` SDK misroutes or reorders `session/update` / `session/prompt` messages.
4. `claude-agent-acp` / Claude Code normalizes its internal transcript incorrectly after image/tool results, causing `tool_use_mismatch`.
5. The upstream Claude API rejects a valid-looking Claude Code request because of beta/tool-use limitations under long context or image blocks.

## Evidence Plan

- Check the latest Quartet backend error timestamp and job/session ids.
- Correlate with ACP session lifecycle logs.
- Inspect Claude Code telemetry around the same timestamp for `tengu_api_error`, `tool_use_mismatch`, and offending tool ids.
- Compare with Quartet persisted `messages.jsonl` to see whether malformed tool sequences were written by Quartet or only inside Claude Code's own transcript.

## Findings

- Backend log shows repeated failures in `session/prompt`, for example:
  - `2026-06-07 17:58:13` job `job-20260607-175057-756242-52029d91`
  - `2026-06-07 17:58:47` same job
- The ACP session for that job was `7b1123b6-a351-4765-bb96-523cf793b029`.
- Claude Code transcript for that ACP session shows a valid local tool sequence before the failure:
  - assistant `tool_use` Read `/tmp/cp_rows.png`
  - user `tool_result` image for the same tool id
  - assistant text: `API Error: 400 due to tool use concurrency issues.`
- Repeating "继续" caused the same pattern again:
  - assistant `tool_use` Read `/tmp/cp_rows.png`
  - user `tool_result` image
  - assistant text: `API Error: 400 due to tool use concurrency issues.`
- Earlier telemetry for the same class of failure explicitly reported:
  - `tengu_api_error`
  - `status=400`
  - `errorType=tool_use_mismatch`
  - `tengu_tool_use_tool_result_mismatch_error`

## Current Conclusion

The failure originates inside `claude-agent-acp` / Claude Code when it sends the next Claude API request after an image `Read` tool result. Quartet's ACP layer only receives and wraps the JSON-RPC failure as `acp prompt failed`.

## Source Inspection

- `/home/fanlv/claude-agent-acp` is source for `@agentclientprotocol/claude-agent-acp@0.40.0`, depending on `@anthropic-ai/claude-agent-sdk@0.3.160`.
- The actual npx runtime cache uses `@agentclientprotocol/claude-agent-acp@0.42.0`, depending on `@anthropic-ai/claude-agent-sdk@0.3.165`.
- NPM latest `@anthropic-ai/claude-agent-sdk` is `0.3.168`, corresponding to Claude Code `2.1.168`.
- `claude-agent-acp/src/acp-agent.ts` only:
  - converts ACP prompt chunks to SDK user messages in `promptToClaude`
  - pushes them into a `Pushable`
  - starts `query({ prompt: input, options })`
  - streams SDK messages back into ACP session updates
  - wraps `message.is_error` results as `RequestError.internalError`
- `claude-agent-acp` does not construct Anthropic API `messages` directly and does not pair `tool_use` with `tool_result`; that state is owned by `@anthropic-ai/claude-agent-sdk` / Claude Code.

## Upgrade Assessment

Upgrading `@anthropic-ai/claude-agent-sdk` from `0.3.165` to `0.3.168` may help because it upgrades the embedded Claude Code runtime from `2.1.165` to `2.1.168`, where the tool transcript/state-machine code lives. It is not guaranteed without reproducing after the upgrade, because the public package metadata does not list a specific fix for `tool_use_mismatch` / `tool use concurrency issues`.

## Quartet-side Decision (2026-06-07)

Confirmed against backend logs that this is an upstream defect, not a Quartet bug:

- The same ACP session succeeds repeatedly before the first failure, so the session is not permanently corrupted by Quartet.
- After the failure, resending the same message reproduces the same 400 every time, because the offending tool-use history is replayed unchanged.

Given that, Quartet does NOT:

- Rebuild the ACP session on this error — that would discard conversation context and still re-trigger the defect on the next image Read.
- Auto-retry — the same history fails identically and only wastes tokens.

Quartet DOES:

- Detect this specific error class on the prompt failure path and return an actionable message telling the user it is a known upstream (coco / claude-agent-acp) issue, that retrying the same message will fail the same way, and that starting a new conversation is the way to continue. The raw RPC error is still included in full.

The real fix must land upstream (or via a verified `claude-agent-sdk` upgrade); this only improves how the failure is surfaced.
