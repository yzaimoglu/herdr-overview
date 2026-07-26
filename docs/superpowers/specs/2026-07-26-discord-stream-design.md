# Discord Output Streaming Design

## Goal

Give each Herdr Discord thread an explicit output-streaming switch. When streaming is enabled, the bot publishes only newly appended output and never reposts the same output. When disabled, the bot continues tracking output silently without sending agent output messages.

## Scope

This changes Discord output publication only. Prompt acknowledgements, lifecycle/status messages, interrupt acknowledgements, close acknowledgements, thread reconciliation, and agent controls remain unchanged.

The commands are exact trimmed messages:

- `/stream on` enables output streaming for the current agent thread.
- `/stream off` disables output streaming for the current agent thread.

Both commands require the existing guild, forum-thread, and user authorization checks.

## Behavior And State

- Streaming defaults to disabled for every new agent.
- The setting persists per Herdr pane in the existing atomic state file and survives bot restarts.
- `/stream on` fetches the current output before enabling streaming. That snapshot becomes the cursor, so enabling does not replay historical output.
- `/stream off` disables publication and refreshes the cursor silently. Re-enabling does not replay output collected while streaming was disabled.
- Existing state records without the new setting deserialize as streaming disabled.

The record stores the stream-enabled setting and the normalized output cursor alongside the existing pane/thread state. The cursor is advanced after a successful output poll even when streaming is disabled.

## Data Flow

1. Parse `/stream on` and `/stream off` before treating message content as a prompt.
2. Resolve the current Discord thread to its pane using the existing state mapping.
3. For `/stream on`, fetch and normalize output, persist the cursor and enabled setting, then acknowledge success.
4. For `/stream off`, disable streaming, refresh the cursor, persist the setting, then acknowledge success.
5. Reconciliation continues polling output for all applicable agents so state stays current.
6. For an enabled pane, compare the current normalized snapshot with the cursor:
   - If the current snapshot extends the cursor, publish only the new suffix, chunked under Discord's payload limit.
   - If the snapshot is unchanged, publish nothing.
   - If the snapshot is a terminal redraw or replacement without a safe append-only suffix, advance the cursor without publishing it.
7. Persist the updated cursor after the poll. In streaming mode, advance it only after all output messages for the detected suffix are sent successfully; in disabled mode, advance it after the silent poll succeeds.

This intentionally favors no duplicate output over attempting to interpret terminal redraws as new transcript content. A future API-level output cursor can improve coverage without changing the Discord command contract.

## Error Handling

- If `/stream on` cannot fetch output, leave streaming disabled and send an error acknowledgement.
- If `/stream off` cannot refresh output, disable streaming and report that the cursor refresh failed.
- If an output poll fails, preserve the previous cursor and stream setting.
- If sending a streamed output chunk fails, do not advance the cursor past the unsent suffix.
- Existing safe error logging remains in effect; prompt contents and bot tokens are never logged.

## Testing

Add focused tests covering:

- Exact `/stream on` and `/stream off` parsing.
- Per-pane stream-setting persistence and reload.
- Enabling from the current output without historical replay.
- Disabling output publication while still advancing the cursor.
- Suffix-only publication when streaming is enabled.
- Suppression of unchanged and rolling/redraw snapshots.
- API failures preserving state and command-specific acknowledgements.
- Discord send failures not advancing the cursor.
- No duplicate output messages across repeated polls or restarts.

Run `cd discord && go test ./... -race` and `go vet ./...`.
