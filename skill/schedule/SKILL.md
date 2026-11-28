---
name: quartet-schedule
description: Create and manage Quartet timers that run a Prompt or a saved Graph Workflow, view execution history and Sessions, or trigger a timer immediately. Use for one-time reminders or actions, recurring plans, time zones, and end dates.
---

# Quartet timers

Use `quartet-cli schedule` against the user's connected Quartet backend. Plans run on that backend when clients are closed. Inspect CLI help for flags and preserve the complete error if a command fails.

## Resolve the request

- Confirm an ambiguous time, time zone, Prompt, workflow, workspace, or Agent/model before creating or changing the plan. Interpret relative dates in the timer's time zone. If needed, use `quartet-cli workspace list`, `quartet-cli agent list`, and `quartet-cli workflow list` to resolve names to IDs.
- Use `--once YYYY-MM-DDTHH:MM` for a single execution and `--cron 'minute hour day month weekday'` for repetition. The backend provides its local time zone by default. Use `--timezone Area/City` when the user specifies a zone.
- A Prompt uses `--prompt`, optional `--agent` and `--model` (the workspace defaults apply when usable), and optional `--timeout` in minutes. A saved workflow uses `--workflow`.
- A repeating timer may use `--end-date YYYY-MM-DD` (inclusive in its time zone) or `--max-runs N`. If both are set, the first limit ends the plan. `--max-concurrent` sets the parallel run limit.
- Creation defaults to enabled when the account has execution permission. Use `--disabled` to save a paused timer.

## Commands

```sh
quartet-cli schedule create --name 'Weekly changes' --once '2026-12-01T09:00' --timezone Asia/Shanghai --workspace ws-example --agent codex --model '<model-id>' --prompt 'Summarize this week’s changes'
quartet-cli schedule create --name 'Daily report' --cron '0 18 * * *' --timezone Asia/Shanghai --end-date 2026-12-31 --workflow '<workflow-id>'
quartet-cli schedule list --json
quartet-cli schedule get '<timer-id>'
quartet-cli schedule update '<timer-id>' --end-date 2027-01-31
quartet-cli schedule disable '<timer-id>'
quartet-cli schedule enable '<timer-id>' --expected-approval '<approvalPreview>'
quartet-cli schedule run '<timer-id>' --request-id '<unique-submission-id>'
quartet-cli schedule history --timer '<timer-id>'
quartet-cli schedule sessions --execution '<execution-id>'
quartet-cli schedule delete '<timer-id>'
```

For a plan awaiting approval, fetch it with `schedule get`, review the effective configuration, workflow version and time plan with the user, then pass the returned `approvalPreview` to `schedule enable`. When retrying an uncertain immediate run, reuse the same request ID. An accepted manual run consumes an execution opportunity. Poll `schedule history` for its Job and outcome. Pass the returned `nextCursor` as `--cursor` to read further records.

After creating or updating a timer, report its name, ID, state and next automatic trigger when available. Show the time in the timer's time zone. Stop and delete affect future automatic triggers. To stop an existing execution, use its Job from execution history.
