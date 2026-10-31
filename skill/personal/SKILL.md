---
name: quartet-personal
description: Search Quartet personal Agent session history, discover business Agents and delegate independent asynchronous tasks using quartet-cli chat. Use when a Quartet conversation needs previous decisions, past session details or work by another business Agent.
---

# Quartet Personal Agent

A business Agent is a configured role. A Harness Client (Codex, Claude Code, etc.) is its ACP execution backend. Discover them separately. The current working directory is the user's home directory.

## History

- `quartet-cli chat sessions --limit 25 --offset 0` lists recent business Sessions and their summaries; add `--agent <business-agent-id>` to filter.
- `quartet-cli chat search --query '中文词或短语' --limit 25 --offset 0` searches user messages, assistant content and session cards, including recent content not yet summarized. Card-only hits have role `summary`; verify them in the source conversation. Add `--tools` only when tool arguments or results are relevant. Space-separated terms must all match.
- `quartet-cli chat history --session <session-id> --message <message-id> --offset 0 --limit 25` reads source messages around a hit. Omit `--message` to read a Session from the beginning. Add `--tools` to retrieve tool arguments and results alongside source messages. Paginate rather than assuming the initial results cover all history.
- Important decisions must be checked against the actual user messages. An assistant's suggestion is not the user's confirmed decision. Internal prompts do not appear in default search results.

## Waiting for clarification

Before ending a turn that needs a user answer, call `quartet-cli chat wait --parent-run <current-run-id> --content '<question>'`. The task keeps its Session while waiting, including beyond the normal idle timeout. The next user message continues it. Internal task results wait in the same queue.

## Asynchronous delegation

1. Run `quartet-cli chat agents` to discover business Agent IDs. Run `quartet-cli harness list` only when execution backend selection matters.
2. Run `quartet-cli chat delegate --parent-run <current-run-id> --agent <business-agent-id> --id <stable-intent-id> --content '<task, relevant context, expected result>'`. The current run ID is provided in the conversation's system reminder. Optional `--harness` and `--model` select the child's execution backend.
3. The command returns an acceptance receipt immediately. Do not poll or wait synchronously. Acknowledge dispatch and continue the conversation.
4. The child has an independent task and Session. Its success or failure will return to the original parent task as an internal input. Summarize it when it arrives; do not dispatch the same work again.
5. Reuse `--id` and exactly the same request when retrying a network failure. New content requires a new intent ID.

Inside a running Quartet ACP Session, quartet-cli receives a short-lived, execution-bound tool credential through its environment. Do not print or copy the credential. It allows only history lookup and delegation under the current user's permissions; do not try to authenticate a browser or modify storage files with it. Full API errors must remain visible.
