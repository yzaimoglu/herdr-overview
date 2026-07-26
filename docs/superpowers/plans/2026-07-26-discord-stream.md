# Discord Output Streaming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add persistent per-agent `/stream on` and `/stream off` controls so Discord sends only new append-only output when enabled and never sends agent output when disabled.

**Architecture:** Reuse the existing `AgentRecord.Output` and `OutputHash` as the normalized output cursor. Add a persisted `StreamEnabled` flag per pane. The message handler owns exact stream commands and cursor initialization; the synchronizer continues polling state but only publishes suffixes for enabled panes. Rolling terminal redraws advance the cursor silently.

**Tech Stack:** Go 1.26, existing Discord adapter, atomic JSON state store, existing focused tests, Docker Compose deployment.

## Global Constraints

- `/stream on` and `/stream off` are exact trimmed commands handled before normal prompts.
- Streaming is disabled by default and persists per Herdr pane across bot restarts.
- `/stream on` snapshots current output before enabling so historical output is not replayed.
- `/stream off` suppresses output publication and advances the cursor silently.
- Prompt acknowledgements, lifecycle/status messages, interrupt acknowledgements, and close acknowledgements remain unchanged.
- Append-only output sends only the new suffix; unchanged and rolling/redraw snapshots send nothing.
- Output API failures preserve the previous cursor and stream setting, except `/stream off` still disables streaming and reports cursor-refresh failure.
- Discord send failures do not advance the cursor past unsent output.
- Never log prompt contents, output contents, or bot tokens.
- Preserve existing thread reconciliation, API behavior, state atomicity, and eight-worker output bound.

---

## Task 1: Add Stream Commands And Persistent Settings

**Files:**
- Modify: `discord/message_handler.go`
- Modify: `discord/model.go` only if a command type needs a documented declaration
- Modify: `discord/state.go`
- Test: `discord/message_handler_test.go`
- Test: `discord/state_test.go`

**Interfaces:**
- Consumes: `Bot.HandleMessage`, `parseMessage`, `StateStore`, `AgentAPI`, `MessageEvent`, and existing pane/thread lookup.
- Produces: exact `messageStreamOn`/`messageStreamOff` parsing and persisted `AgentRecord.StreamEnabled` behavior consumed by Task 2.

- [ ] **Step 1: Write failing command parsing tests**

Add table-driven cases to `discord/message_handler_test.go` proving exact trimmed `/stream on` and `/stream off` commands produce distinct command kinds, while `/stream`, `/stream on now`, and `/stream off now` remain normal prompts.

```go
func TestParseMessageStreamCommands(t *testing.T) {
    for _, test := range []struct {
        content string
        kind    string
    }{
        {content: " /stream on ", kind: messageStreamOn},
        {content: "\n/stream off\t", kind: messageStreamOff},
        {content: "/stream", kind: messagePrompt},
        {content: "/stream on now", kind: messagePrompt},
    } {
        got, _ := parseMessage(test.content)
        if got != test.kind {
            t.Fatalf("parseMessage(%q) = %q, want %q", test.content, got, test.kind)
        }
    }
}
```

- [ ] **Step 2: Run the parsing tests and verify they fail**

Run: `cd discord && go test ./... -run TestParseMessageStreamCommands`

Expected: FAIL because the stream command kinds and parser cases do not exist.

- [ ] **Step 3: Add persisted stream state tests**

Extend state round-trip coverage with `AgentRecord{StreamEnabled: true}` and assert the flag survives `NewStateStore(path)` reload. Add a default assertion that a record created without the JSON field loads with `StreamEnabled == false`.

- [ ] **Step 4: Implement command parsing and state field**

Add:

```go
const (
    messageStreamOn  = "stream_on"
    messageStreamOff = "stream_off"
)
```

Update `parseMessage` to match exact trimmed `/stream on` and `/stream off` before the prompt fallback. Add this field to `AgentRecord`:

```go
StreamEnabled bool `json:"streamEnabled,omitempty"`
```

Keep missing JSON fields backward-compatible as `false`.

- [ ] **Step 5: Implement stream command handling**

Add one helper in `message_handler.go` with this behavior:

```go
func (b *Bot) setStream(ctx context.Context, paneID string, enabled bool) error
```

For `/stream on`, fetch `AgentAPI.Output(ctx, paneID, 0)`, normalize it, update `Output` and `OutputHash`, set `StreamEnabled=true`, and persist atomically. If the fetch fails, do not enable streaming.

For `/stream off`, set `StreamEnabled=false` and persist even if refreshing output fails. If refresh succeeds, also update `Output` and `OutputHash`; if it fails, return the refresh error after the disabled state is persisted.

Handle command acknowledgements:

- Success on: `Output streaming enabled.`
- Success off: `Output streaming disabled.`
- Off with refresh failure: `Output streaming disabled, but the cursor could not be refreshed.`

Leave normal prompt, interrupt, close, and lifecycle acknowledgements unchanged. Remove the current prompt-specific `markOutputPending` call and its error path; explicit streaming now controls output publication.

- [ ] **Step 6: Run focused Task 1 tests**

Run: `cd discord && go test ./... -run 'TestParseMessageStreamCommands|TestStateStore'`

Expected: PASS.

- [ ] **Step 7: Commit Task 1**

```bash
git add discord/message_handler.go discord/message_handler_test.go discord/state.go discord/state_test.go
git commit -m "feat: add persistent Discord stream controls"
```

## Task 2: Make Synchronization Stream-Aware

**Files:**
- Modify: `discord/sync.go`
- Modify: `discord/output.go`
- Modify: `discord/state.go` to remove obsolete prompt-forcing state if Task 1 did not remove it
- Test: `discord/sync_test.go`
- Test: `discord/output_test.go`

**Interfaces:**
- Consumes: `AgentRecord.StreamEnabled`, `AgentAPI.Output`, `DiscordClient.SendMessage`, `normalizeOutput`, `outputUpdate`, and the existing pane lock.
- Produces: stream-aware `SyncAgentOutput` behavior used by reconciliation and prompt handling.

- [ ] **Step 1: Write failing stream synchronization tests**

Add focused tests using the existing fakes:

```go
func TestSyncAgentOutputDoesNotSendWhenStreamDisabled(t *testing.T) {
    store := testStore(t)
    if err := store.Set("pane", AgentRecord{
        ThreadID: "thread-1",
        Output: "old output",
        OutputHash: "previous-output-hash",
        StreamEnabled: false,
    }); err != nil { t.Fatal(err) }
    api := &fakeAgentAPI{outputs: map[string]string{"pane": "old output\nnew output"}}
    discord := &fakeDiscordClient{}
    if err := NewSyncer(api, discord, store, testConfig()).SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}); err != nil {
        t.Fatal(err)
    }
    if len(discord.messages) != 0 { t.Fatalf("sent output while disabled: %+v", discord.messages) }
    if got, _ := store.Get("pane"); got.Output != "old output\nnew output" { t.Fatalf("cursor was not advanced: %+v", got) }
}
```

Also test enabled append-only output sends exactly one fenced suffix, unchanged output sends nothing, a rolling replacement sends nothing while advancing the cursor, and a Discord send failure leaves the old cursor intact.

- [ ] **Step 2: Run the new synchronization tests and verify failure**

Run: `cd discord && go test ./... -run 'TestSyncAgentOutputDoesNotSendWhenStreamDisabled|TestSyncAgentOutput.*Stream'`

Expected: FAIL because synchronization currently publishes based on output changes without checking `StreamEnabled`.

- [ ] **Step 3: Remove obsolete prompt-forcing behavior**

Remove `OutputPending`, `OutputMessageHash`, and their related fields from `AgentRecord`. Remove `markOutputPending` and the `OutputPending`/`OutputMessageHash` preservation and force-snapshot branches. The stream switch plus the full normalized output cursor/hash must be the only output-publication state.

- [ ] **Step 4: Implement stream-aware output synchronization**

In `SyncAgentOutput`:

1. Read the record and fetch/normalize output as today.
2. If the normalized hash is unchanged, return without writing state.
3. If `StreamEnabled` is false, update `OutputHash`, `Output`, and `LastSync`, persist, and return without Discord calls.
4. If enabled, call `outputUpdate(record.Output, normalized)`.
5. Send only a non-empty append-only suffix, chunked with the existing 1900-character limit and fence handling.
6. If the output is a replacement/redraw, send nothing but persist the new cursor.
7. Persist the cursor only after all generated chunks send successfully.

Do not add a second emitted-message hash: the persisted normalized output hash and append-only cursor are sufficient once rolling replacements are suppressed. Do not change the eight-worker bound or pane locking.

- [ ] **Step 5: Run focused synchronization tests**

Run: `cd discord && go test ./... -run 'TestSyncAgentOutput|TestOutputUpdate|TestReconcile'`

Expected: PASS.

- [ ] **Step 6: Run the race suite and vet**

Run: `cd discord && go test ./... -race && go vet ./...`

Expected: PASS.

- [ ] **Step 7: Commit Task 2**

```bash
git add discord/sync.go discord/output.go discord/state.go discord/sync_test.go discord/output_test.go
git commit -m "feat: stream only new Discord output"
```

## Task 3: Document Commands And Verify Deployment

**Files:**
- Modify: `README.md`
- Test/verify: `discord/*_test.go`, `server/*_test.go`, Compose files, Docker image

**Interfaces:**
- Consumes: stream commands and persisted state from Tasks 1-2.
- Produces: operator documentation and a verified running adapter.

- [ ] **Step 1: Document stream controls**

Add a Discord setup subsection to `README.md` documenting:

- `/stream on` enables output messages for the current thread.
- `/stream off` disables output messages for the current thread.
- Streaming is off by default and persists per agent.
- Enabling starts from the current output and does not replay history.
- Lifecycle/status and prompt acknowledgements still appear while output streaming is off.

- [ ] **Step 2: Run all verification checks**

Run:

```bash
cd discord && go test ./... -race && go vet ./...
cd ../server && go test ./... && go vet ./...
cd .. && bun run build
docker compose --env-file .env -f docker-compose.yml -f docker-compose.discord.yml config --quiet
docker build -f Dockerfile.discord .
```

Expected: all commands pass; existing frontend deprecation hints may remain non-blocking.

- [ ] **Step 3: Verify runtime behavior**

With the local Compose deployment running:

1. Confirm `/stream off` sends only its acknowledgement and no output.
2. Confirm `/stream on` sends only its acknowledgement initially.
3. Produce appended agent output and confirm exactly the new suffix appears once.
4. Wait through at least two polling intervals and confirm no duplicate output appears.
5. Restart `discord-bot`, confirm the stream setting persists, and confirm the cursor does not replay old output.

- [ ] **Step 4: Commit documentation and verification**

```bash
git add README.md
git commit -m "docs: document Discord output streaming"
```
