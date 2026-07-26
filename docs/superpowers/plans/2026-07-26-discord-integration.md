# Discord Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a separate Go Discord adapter that mirrors Herdr agents into one Discord forum channel, forwards authorized thread messages as prompts, publishes lifecycle/output updates, and archives threads when agents close.

**Architecture:** The new `discord` service connects to Discord's Gateway and calls the existing `overview-api` over Docker's private network. A durable atomic JSON state file maps Herdr pane IDs to Discord thread IDs. A bounded polling synchronizer reconciles agents and output; a small Discord client interface keeps synchronization and message handling testable without live Discord credentials.

**Tech Stack:** Go 1.26, `discordgo`, Go standard library HTTP/JSON/file/crypto packages, Docker Compose, existing Herdr Overview Go API.

## Global Constraints

- Keep Discord Gateway and synchronization isolated from the existing `overview-api` process.
- Use `DISCORD_BOT_TOKEN`, `DISCORD_GUILD_ID`, `DISCORD_FORUM_CHANNEL_ID`, and `DISCORD_ALLOWED_USER_IDS` as runtime configuration; never commit secrets.
- Use one configured Discord forum channel and one thread per Herdr `paneId`.
- Normal authorized thread messages are prompts; `/interrupt` and `/close` are control messages.
- Discord mirrors and controls agents but does not start agents in this version.
- Do not archive agents after a failed overview/API request.
- Do not repost unchanged output.
- Keep state in an atomic JSON file on a dedicated Docker volume.
- Preserve the existing web UI and API behavior.
- Keep exported Go types and functions small, documented where non-obvious, and covered by focused tests.

---

## File Map

- Create `discord/go.mod`: standalone module and `discordgo` dependency.
- Create `discord/config.go`: environment parsing and validation.
- Create `discord/model.go`: Discord adapter domain types and Herdr API DTOs.
- Create `discord/contracts.go`: interfaces shared by sync and message handling.
- Create `discord/api_client.go`: HTTP client for existing overview API routes.
- Create `discord/state.go`: atomic persistent pane/thread state.
- Create `discord/output.go`: output diffing, formatting, chunking, and message text.
- Create `discord/sync.go`: agent/thread reconciliation and status/output publication.
- Create `discord/message_handler.go`: authorization, prompt forwarding, and control parsing.
- Create `discord/discordgo_client.go`: concrete `discordgo` REST/Gateway adapter.
- Create `discord/main.go`: process wiring, Gateway lifecycle, and sync ticker.
- Create focused `discord/*_test.go` files alongside each unit.
- Create `Dockerfile.discord`: minimal multi-stage Discord bot image.
- Modify `docker-compose.yml`: add `discord-bot` and persistent state volume.
- Modify `.gitignore` and `.dockerignore`: exclude local Discord build/state artifacts.
- Modify `README.md`: Discord application setup, permissions, environment, and smoke test.

## Task 1: Scaffold Config And Contracts

**Files:**
- Create: `discord/go.mod`
- Create: `discord/config.go`
- Create: `discord/model.go`
- Create: `discord/contracts.go`
- Test: `discord/config_test.go`

**Interfaces:**
- Produces `Config`, `LoadConfig(func(string) string) (Config, error)`, `Agent`, `Overview`, `Thread`, `MessageEvent`, `AgentAPI`, and `DiscordClient` contracts used by later tasks.

- [ ] **Step 1: Create the standalone module and dependency declaration**

Create `discord/go.mod` with module path `github.com/yzaimoglu/herdr-overview/discord`, Go version `1.26`, and `github.com/bwmarrin/discordgo v0.28.1`.

Run: `cd discord && go mod tidy`
Expected: `go.mod` and `go.sum` are created without errors.

- [ ] **Step 2: Write failing configuration tests**

Add tests covering required variables, defaults, comma-separated user IDs, invalid duration, and missing required values.

```go
func TestLoadConfigDefaultsAndUsers(t *testing.T) {
    values := map[string]string{
        "DISCORD_BOT_TOKEN":        "token",
        "DISCORD_GUILD_ID":         "guild",
        "DISCORD_FORUM_CHANNEL_ID": "forum",
        "DISCORD_ALLOWED_USER_IDS": "111, 222",
    }
    config, err := LoadConfig(func(key string) string { return values[key] })
    if err != nil {
        t.Fatal(err)
    }
    if config.APIURL != "http://overview-api:8787" || config.SyncInterval != 10*time.Second {
        t.Fatalf("unexpected defaults: %+v", config)
    }
    if _, ok := config.AllowedUserIDs["222"]; !ok {
        t.Fatalf("allowed user was not parsed: %+v", config.AllowedUserIDs)
    }
}

func TestLoadConfigRequiresControlSettings(t *testing.T) {
    _, err := LoadConfig(func(string) string { return "" })
    if err == nil {
        t.Fatal("expected missing configuration error")
    }
}
```

Run: `cd discord && go test ./...`
Expected: FAIL because `Config` and `LoadConfig` do not exist yet.

- [ ] **Step 3: Implement configuration parsing**

Define:

```go
type Config struct {
    Token           string
    GuildID         string
    ForumChannelID  string
    AllowedUserIDs  map[string]struct{}
    APIURL          string
    StatePath       string
    SyncInterval    time.Duration
}

func LoadConfig(getenv func(string) string) (Config, error)
```

Require non-empty token, guild ID, forum channel ID, and at least one allowed user ID. Default `HERDR_OVERVIEW_API_URL` to `http://overview-api:8787`, `DISCORD_STATE_PATH` to `/data/discord-state.json`, and `DISCORD_SYNC_INTERVAL` to `10s`. Reject non-positive intervals and return errors that name the missing/invalid variable without including the token value.

- [ ] **Step 4: Define domain models and interfaces**

Use local DTOs instead of refactoring the existing `server` package, which is currently `package main`:

```go
type Agent struct {
    Name, Kind, Status, WorkspaceID, PaneID, TerminalID, SessionID, CWD string
    Focused bool
}

type Overview struct { Agents []Agent }

type Thread struct {
    ID, ParentID, Name string
    Archived bool
}

type MessageEvent struct {
    GuildID, ChannelID, ParentID, AuthorID, Content string
    AuthorIsBot bool
}

type AgentAPI interface {
    Overview(context.Context) (Overview, error)
    Output(context.Context, string, int) (string, error)
    Prompt(context.Context, string, string) error
    Interrupt(context.Context, string) error
    Close(context.Context, string) error
}

type DiscordClient interface {
    CreateForumThread(context.Context, string, string, string) (Thread, error)
    FindThreadByPane(context.Context, string, string) (Thread, bool, error)
    SendMessage(context.Context, string, string) error
    ArchiveThread(context.Context, string) error
}
```

Add JSON tags to API DTOs matching the existing camelCase API response. Keep Discord IDs as strings.

- [ ] **Step 5: Run focused tests and commit**

Run: `cd discord && go test ./...`
Expected: PASS.

Commit:

```bash
git add discord/go.mod discord/go.sum discord/config.go discord/model.go discord/contracts.go discord/config_test.go
git commit -m "feat: scaffold Discord adapter contracts"
```

## Task 2: Implement API Client And Durable State

**Files:**
- Create: `discord/api_client.go`
- Create: `discord/state.go`
- Test: `discord/api_client_test.go`
- Test: `discord/state_test.go`

**Interfaces:**
- Consumes `AgentAPI` and DTOs from Task 1.
- Produces `HTTPAgentAPI`, `StateStore`, `NewStateStore(string) (*StateStore, error)`, and `AgentRecord` for Tasks 3-5.

- [ ] **Step 1: Write failing HTTP client tests**

Use `httptest.Server` to verify URL escaping, HTTP methods, JSON payloads, successful decoding, and non-2xx errors for `Overview`, `Output`, `Prompt`, `Interrupt`, and `Close`.

```go
func TestHTTPAgentAPIPromptEscapesPaneAndSendsJSON(t *testing.T) {
    var gotPath, gotPrompt string
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        var body struct{ Prompt string `json:"prompt"` }
        _ = json.NewDecoder(r.Body).Decode(&body)
        gotPrompt = body.Prompt
        w.Header().Set("Content-Type", "application/json")
        _, _ = io.WriteString(w, `{ "ok": true }`)
    }))
    defer server.Close()

    client := NewHTTPAgentAPI(server.URL, server.Client())
    if err := client.Prompt(context.Background(), "w3:p5", "check status"); err != nil {
        t.Fatal(err)
    }
    if gotPath != "/api/agents/w3%3Ap5/prompt" || gotPrompt != "check status" {
        t.Fatalf("unexpected request: path=%q prompt=%q", gotPath, gotPrompt)
    }
}
```

Run: `cd discord && go test ./...`
Expected: FAIL because the HTTP client is not implemented.

- [ ] **Step 2: Implement the API client**

Define `HTTPAgentAPI` with a base URL and injected `*http.Client`. Use `url.PathEscape` for pane IDs, a 10-second request timeout in the client, JSON `Content-Type` for prompts, and errors containing only HTTP status plus the API error string. Decode `/api/overview` into `Overview`, `/output` from `{ "text": "..." }`, and require a successful 2xx response for actions.

- [ ] **Step 3: Write failing state-store tests**

Cover first-run initialization, save/reload, atomic replacement, concurrent access, malformed-file recovery, and copying state out of the store so callers cannot mutate internal maps.

```go
func TestStateStoreRoundTrips(t *testing.T) {
    path := filepath.Join(t.TempDir(), "discord-state.json")
    store, err := NewStateStore(path)
    if err != nil {
        t.Fatal(err)
    }
    want := AgentRecord{ThreadID: "thread-1", Status: "working", OutputHash: "abc"}
    if err := store.Set("w3:p5", want); err != nil {
        t.Fatal(err)
    }
    reloaded, err := NewStateStore(path)
    if err != nil {
        t.Fatal(err)
    }
    got, ok := reloaded.Get("w3:p5")
    if !ok || got.ThreadID != want.ThreadID || got.OutputHash != want.OutputHash {
        t.Fatalf("unexpected state: ok=%v record=%+v", ok, got)
    }
}
```

Run: `cd discord && go test ./...`
Expected: FAIL because `StateStore` is not implemented.

- [ ] **Step 4: Implement atomic JSON state**

Define:

```go
type AgentRecord struct {
    ThreadID    string    `json:"threadId"`
    Status      string    `json:"status"`
    OutputHash  string    `json:"outputHash"`
    LastSync    time.Time `json:"lastSync"`
    Closed      bool      `json:"closed"`
}

type StateStore struct {
    path    string
    mu      sync.RWMutex
    records map[string]AgentRecord
}

func NewStateStore(path string) (*StateStore, error)
func (s *StateStore) Get(paneID string) (AgentRecord, bool)
func (s *StateStore) Set(paneID string, record AgentRecord) error
func (s *StateStore) Records() map[string]AgentRecord
```

Load an absent file as empty state. For invalid JSON, rename the file to `state.json.corrupt-<UTC timestamp>` before starting empty. Save by writing a same-directory temporary file with mode `0600`, syncing/closing it, and renaming it over the target. Guard all map access with a mutex.

- [ ] **Step 5: Run focused tests and commit**

Run: `cd discord && go test ./...`
Expected: PASS.

Commit:

```bash
git add discord/api_client.go discord/api_client_test.go discord/state.go discord/state_test.go
git commit -m "feat: add Discord API client and durable state"
```

## Task 3: Build Thread Synchronization And Output Formatting

**Files:**
- Create: `discord/output.go`
- Create: `discord/sync.go`
- Test: `discord/output_test.go`
- Test: `discord/sync_test.go`

**Interfaces:**
- Consumes `AgentAPI`, `DiscordClient`, `StateStore`, `Config`, and models from Tasks 1-2.
- Produces `Syncer`, `NewSyncer(AgentAPI, DiscordClient, *StateStore, Config) *Syncer`, `Reconcile(context.Context) error`, and `SyncAgentOutput(context.Context, Agent) error` for Tasks 4-5.

- [ ] **Step 1: Write failing output helper tests**

Test changed-output detection, ring-buffer replacement, Markdown fencing, and chunking below 1900 characters without splitting UTF-8 incorrectly.

```go
func TestOutputUpdateUsesOnlyNewSuffixWhenPossible(t *testing.T) {
    update, changed := outputUpdate("line 1\nline 2", "line 1\nline 2\nline 3")
    if !changed || update != "line 3" {
        t.Fatalf("unexpected update: changed=%v update=%q", changed, update)
    }
}
```

Run: `cd discord && go test ./...`
Expected: FAIL because output helpers are not implemented.

- [ ] **Step 2: Implement output helpers**

Define:

```go
func outputUpdate(previous, current string) (string, bool)
func chunkMessage(text string, limit int) []string
func formatOutput(text string) string
func threadName(agent Agent) string
func lifecycleMessage(agent Agent, previousStatus string) string
```

Use a 1900-character payload limit, preserve line boundaries when possible, and fall back to rune-safe splitting for a single oversized line. `outputUpdate` returns the suffix when `current` has `previous` as a prefix; when Herdr's rolling output has replaced the prefix, return the current output as a changed snapshot. Hash the normalized current output with SHA-256 for state deduplication. Keep thread names deterministic and under Discord's 100-character limit.

- [ ] **Step 3: Write failing synchronization tests with fakes**

Create fake API and Discord clients. Test new-thread creation, idempotent resync, status updates, output deduplication, missing-agent archival, API failure preservation, deleted-thread recovery, and bounded output calls for a list of agents.

```go
func TestReconcileCreatesOneThreadAndDoesNotDuplicate(t *testing.T) {
    api := &fakeAgentAPI{overview: Overview{Agents: []Agent{{PaneID: "w3:p5", Name: "builder", Status: "working"}}}}
    discord := &fakeDiscordClient{}
    store, err := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
    if err != nil { t.Fatal(err) }
    syncer := NewSyncer(api, discord, store, testConfig())

    if err := syncer.Reconcile(context.Background()); err != nil { t.Fatal(err) }
    if err := syncer.Reconcile(context.Background()); err != nil { t.Fatal(err) }
    if len(discord.created) != 1 { t.Fatalf("created %d threads", len(discord.created)) }
}
```

Run: `cd discord && go test ./...`
Expected: FAIL because `Syncer` is not implemented.

- [ ] **Step 4: Implement bounded reconciliation**

Define:

```go
type Syncer struct {
    api     AgentAPI
    discord DiscordClient
    state   *StateStore
    config  Config
    now     func() time.Time
}

func NewSyncer(api AgentAPI, discord DiscordClient, state *StateStore, config Config) *Syncer
func (s *Syncer) Reconcile(ctx context.Context) error
func (s *Syncer) SyncAgentOutput(ctx context.Context, agent Agent) error
```

Only archive records after `Overview` succeeds. For each live pane, find/load/create its thread, post initial status for new threads, post lifecycle messages only when status changes, and call output synchronization only for working agents or agents whose status changed. Use a semaphore of eight concurrent output requests and a `WaitGroup`; do not create an unbounded goroutine per agent. Never hold the state mutex while making Discord or API calls. Persist each successful mapping/status/output update.

When a mapped thread is absent, call `FindThreadByPane` before creating a replacement. When a live list omits a previously open record, post the closure message, archive the thread, and mark the record closed instead of deleting it.

- [ ] **Step 5: Run focused tests and commit**

Run: `cd discord && go test ./... -race`
Expected: PASS.

Commit:

```bash
git add discord/output.go discord/output_test.go discord/sync.go discord/sync_test.go
git commit -m "feat: synchronize Herdr agents to Discord threads"
```

## Task 4: Add Discord Message Handling And Gateway Adapter

**Files:**
- Create: `discord/message_handler.go`
- Create: `discord/discordgo_client.go`
- Create: `discord/main.go`
- Test: `discord/message_handler_test.go`

**Interfaces:**
- Consumes `Syncer`, `AgentAPI`, `DiscordClient`, `StateStore`, and `Config` from Tasks 1-3.
- Produces `Bot.Run(context.Context) error` and `Bot.HandleMessage(context.Context, MessageEvent) error` for deployment wiring.

- [ ] **Step 1: Write failing message-handler tests**

Test exact `/interrupt` and `/close` parsing, normal prompt forwarding, authorization, bot-message rejection, wrong guild/channel rejection, unknown-thread rejection, and error acknowledgments.

```go
func TestHandleMessageForwardsAuthorizedPrompt(t *testing.T) {
    api := &fakeAgentAPI{}
    discord := &fakeDiscordClient{}
    store := testStoreWithRecord(t, "w3:p5", AgentRecord{ThreadID: "thread-1"})
    bot := NewBot(testConfig(), api, discord, store, NewSyncer(api, discord, store, testConfig()))

    err := bot.HandleMessage(context.Background(), MessageEvent{
        GuildID: "guild", ChannelID: "thread-1", ParentID: "forum",
        AuthorID: "111", Content: "continue the investigation",
    })
    if err != nil { t.Fatal(err) }
    if api.lastPrompt != "continue the investigation" { t.Fatalf("prompt=%q", api.lastPrompt) }
}
```

Run: `cd discord && go test ./...`
Expected: FAIL because `Bot` is not implemented.

- [ ] **Step 2: Implement pure message parsing and authorization**

Define `parseMessage(content string) (kind, body string)` where `kind` is `prompt`, `interrupt`, `close`, or `ignore`. Match control commands only after trimming whitespace and require the exact command string. Define `authorized(event MessageEvent) bool` requiring configured guild, a thread whose parent is the configured forum, an allowed author ID, and `AuthorIsBot == false`.

- [ ] **Step 3: Implement Bot message handling**

Define:

```go
type Bot struct {
    config Config
    api AgentAPI
    discord DiscordClient
    state *StateStore
    syncer *Syncer
}

func NewBot(Config, AgentAPI, DiscordClient, *StateStore, *Syncer) *Bot
func (b *Bot) HandleMessage(context.Context, MessageEvent) error
func (b *Bot) Run(context.Context) error
```

For prompts, call `AgentAPI.Prompt`, acknowledge success in the thread, then call `SyncAgentOutput` so a response can be posted without waiting for the next ticker. For `/interrupt` and `/close`, call the matching API route and post a result message; `/close` is archived by reconciliation after the API confirms the pane is gone. Keep Discord failures visible in logs and return them to the Gateway handler without crashing the process.

- [ ] **Step 4: Implement the `discordgo` adapter**

Create a concrete client that maps `DiscordClient` operations to Discord REST calls. Configure Gateway intents for guilds, guild messages, message content, and message reactions only if required by the library. Register one `MessageCreate` handler that converts the Discord event to `MessageEvent` and calls `Bot.HandleMessage`. Ignore DMs and events outside the configured guild/forum before API calls.

- [ ] **Step 5: Wire the long-running process**

In `main.go`, load config, create the HTTP API client, state store, `discordgo` client, syncer, and bot. Open the Gateway, run an immediate reconciliation, then run a ticker at `Config.SyncInterval`. Prevent overlapping reconciliations by running one loop at a time. On context cancellation, close the Discord session and return cleanly. Log connection, reconciliation, and message errors without logging the bot token or prompt contents.

- [ ] **Step 6: Run focused tests and commit**

Run: `cd discord && go test ./... -race`
Expected: PASS.

Commit:

```bash
git add discord/message_handler.go discord/message_handler_test.go discord/discordgo_client.go discord/main.go
git commit -m "feat: add Discord Gateway controls"
```

## Task 5: Add Container Deployment And Documentation

**Files:**
- Create: `Dockerfile.discord`
- Modify: `docker-compose.yml`
- Modify: `.gitignore`
- Modify: `.dockerignore`
- Modify: `README.md`

**Interfaces:**
- Consumes the `discord` module executable and runtime configuration from Tasks 1-4.
- Produces a reproducible `discord-bot` service with durable state.

- [ ] **Step 1: Add the Discord multi-stage image**

Create `Dockerfile.discord`:

```dockerfile
FROM golang:1.26 AS build
WORKDIR /src/discord
COPY discord/go.mod discord/go.sum ./
RUN go mod download
COPY discord/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/herdr-discord-bot .

FROM debian:bookworm-slim
COPY --from=build /out/herdr-discord-bot /usr/local/bin/herdr-discord-bot
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/herdr-discord-bot"]
```

- [ ] **Step 2: Add the Compose service and volume**

Add a `discord-bot` service with `build: { context: ., dockerfile: Dockerfile.discord }`, `depends_on: [overview-api]`, `unless-stopped`, `discord-state:/data`, and the configuration variables. Use `${DISCORD_BOT_TOKEN:?DISCORD_BOT_TOKEN is required}` so the container refuses to start without a token. Add a top-level named volume `discord-state`.

- [ ] **Step 3: Extend ignore rules**

Ignore `/discord/herdr-discord-bot`, `/data/`, and local state files such as `discord-state.json` while keeping `discord/go.mod`, `discord/go.sum`, source, tests, and the Dockerfile tracked.

- [ ] **Step 4: Document setup and smoke test**

Add README sections covering Discord Developer Portal setup, enabling the Message Content privileged intent, bot permissions, forum channel creation, environment variables, allowed user IDs, state volume, and this smoke test:

1. Start Compose with the required Discord variables.
2. Confirm the bot connects without logging the token.
3. Confirm every current Herdr agent has one forum thread.
4. Send a normal message as an allowed user and verify the API receives a prompt.
5. Send `/interrupt` and verify the thread reports the action.
6. Close an agent through the web UI and verify the matching thread archives after reconciliation.

- [ ] **Step 5: Validate the deployment files**

Run: `docker compose config --quiet`
Expected: exit 0 with no Compose validation errors.

Run: `docker build -f Dockerfile.discord .`
Expected: image builds without requiring Discord credentials.

- [ ] **Step 6: Commit deployment changes**

```bash
git add Dockerfile.discord docker-compose.yml .gitignore .dockerignore README.md
git commit -m "feat: deploy Discord adapter"
```

## Task 6: Full Verification And Handoff

**Files:**
- Test: all `discord/*_test.go`
- Verify: `server/*_test.go`, frontend build, Compose configuration

- [x] **Step 1: Run the Discord test suite with race detection**

Run: `cd discord && go test ./... -race`
Expected: PASS.

- [x] **Step 2: Run the existing API suite**

Run: `cd server && go test ./... && go vet ./...`
Expected: PASS.

- [x] **Step 3: Run the frontend build**

Run: `bun run build`
Expected: Astro check/build completes with 0 errors. Existing dependency deprecation hints may remain non-blocking.

- [x] **Step 4: Validate Compose and route health**

Run: `docker compose config --quiet`
Expected: exit 0.

Run: `curl -fsS http://127.0.0.1/api/health`
Expected: JSON response with `"ok":true`.

Verification note: the branch API returned the expected health JSON on an isolated local port. The required host-port check returned 404 from an unrelated Caddy Compose project already occupying port 80; the Discord integration Compose project was not running.

- [x] **Step 5: Review security and repository state**

Run: `git diff --check`, inspect `git status --short`, and scan tracked files for token/private-key patterns. Confirm no `.env` file, bot token, state JSON, or build artifact is staged.

- [x] **Step 6: Commit verification results**

```bash
git add discord docs/superpowers/plans/2026-07-26-discord-integration.md
git commit -m "test: verify Discord integration"
```

## Plan Self-Review

- Spec coverage: architecture, configuration, lifecycle, inbound controls, outbound output, persistence, failure behavior, testing, deployment, and deferred scope are covered by Tasks 1-6.
- Scalability: output polling is bounded to eight concurrent API requests, reconciliations do not overlap, unchanged output is fingerprinted, and state access is mutex-protected.
- Maintainability: Discord REST/Gateway code is isolated behind `DiscordClient`; Herdr calls are isolated behind `AgentAPI`; sync logic is testable with fakes.
- Security: the token is runtime-only, user IDs are allowlisted, guild/forum boundaries are checked, bot loops are ignored, and state is outside the image/repository.
- Placeholder scan: no unresolved placeholders or design choices remain in this plan.
