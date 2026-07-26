package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type handlerAgentAPI struct {
	mu           sync.Mutex
	prompt       string
	interrupt    string
	close        string
	output       string
	promptErr    error
	interruptErr error
	closeErr     error
	outputErr    error
}

func (f *handlerAgentAPI) Overview(context.Context) (Overview, error) { return Overview{}, nil }

func (f *handlerAgentAPI) Output(context.Context, string, int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.output, f.outputErr
}

func (f *handlerAgentAPI) Prompt(_ context.Context, _, prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompt = prompt
	return f.promptErr
}

func (f *handlerAgentAPI) Interrupt(_ context.Context, paneID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupt = paneID
	return f.interruptErr
}

func (f *handlerAgentAPI) Close(_ context.Context, paneID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.close = paneID
	return f.closeErr
}

type handlerDiscordClient struct {
	mu       sync.Mutex
	messages []string
	err      error
}

type runAgentAPI struct {
	handlerAgentAPI
	overviewCalls atomic.Int32
	firstOverview chan struct{}
}

func (f *runAgentAPI) Overview(context.Context) (Overview, error) {
	if f.overviewCalls.Add(1) == 1 {
		close(f.firstOverview)
	}
	return Overview{}, nil
}

type runDiscordClient struct {
	handlerDiscordClient
	opened  bool
	closed  bool
	handler func(context.Context, MessageEvent) error
}

func (f *runDiscordClient) Open() error {
	f.opened = true
	return nil
}

func (f *runDiscordClient) Close() error {
	f.closed = true
	return nil
}

func (f *runDiscordClient) RegisterMessageHandler(handler func(context.Context, MessageEvent) error) func() {
	f.handler = handler
	return func() { f.handler = nil }
}

func (*handlerDiscordClient) CreateForumThread(context.Context, string, string, string) (Thread, error) {
	return Thread{}, nil
}

func (*handlerDiscordClient) FindThreadByPane(context.Context, string, string) (Thread, bool, error) {
	return Thread{}, false, nil
}

func (f *handlerDiscordClient) SendMessage(_ context.Context, _, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, content)
	return f.err
}

func (*handlerDiscordClient) ArchiveThread(context.Context, string) error { return nil }

func TestParseMessageRecognizesOnlyExactControlCommands(t *testing.T) {
	tests := []struct {
		content string
		kind    string
		body    string
	}{
		{" /interrupt ", "interrupt", ""},
		{"\t/close\n", "close", ""},
		{"/interrupt now", "prompt", "/interrupt now"},
		{"please /close", "prompt", "please /close"},
		{"continue the investigation", "prompt", "continue the investigation"},
		{" \t\n", "ignore", ""},
	}
	for _, test := range tests {
		t.Run(test.content, func(t *testing.T) {
			kind, body := parseMessage(test.content)
			if kind != test.kind || body != test.body {
				t.Fatalf("parseMessage() = (%q, %q), want (%q, %q)", kind, body, test.kind, test.body)
			}
		})
	}
}

func TestAuthorizedRequiresGuildForumUserAndNonBot(t *testing.T) {
	bot := &Bot{config: Config{
		GuildID:        "guild",
		ForumChannelID: "forum",
		AllowedUserIDs: map[string]struct{}{"111": {}},
	}}
	base := MessageEvent{GuildID: "guild", ChannelID: "thread", ParentID: "forum", AuthorID: "111"}
	for name, event := range map[string]MessageEvent{
		"valid":       base,
		"wrong guild": {GuildID: "other", ChannelID: "thread", ParentID: "forum", AuthorID: "111"},
		"wrong forum": {GuildID: "guild", ChannelID: "thread", ParentID: "other", AuthorID: "111"},
		"wrong user":  {GuildID: "guild", ChannelID: "thread", ParentID: "forum", AuthorID: "222"},
		"bot":         {GuildID: "guild", ChannelID: "thread", ParentID: "forum", AuthorID: "111", AuthorIsBot: true},
	} {
		want := name == "valid"
		if got := bot.authorized(event); got != want {
			t.Errorf("authorized(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestHandleMessageForwardsAuthorizedPromptAndSyncsOutput(t *testing.T) {
	api := &handlerAgentAPI{output: "agent response"}
	discord := &handlerDiscordClient{}
	store := handlerStore(t, AgentRecord{ThreadID: "thread-1"})
	config := handlerConfig()
	bot := NewBot(config, api, discord, store, NewSyncer(api, discord, store, config))

	err := bot.HandleMessage(context.Background(), MessageEvent{
		GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111", Content: "continue the investigation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if api.prompt != "continue the investigation" {
		t.Fatalf("prompt=%q", api.prompt)
	}
	if len(discord.messages) != 2 || discord.messages[0] != "Prompt sent." || !strings.Contains(discord.messages[1], "agent response") {
		t.Fatalf("unexpected acknowledgments: %#v", discord.messages)
	}
}

func TestHandleMessageRejectsUnauthorizedAndUnknownThreads(t *testing.T) {
	api := &handlerAgentAPI{}
	discord := &handlerDiscordClient{}
	store := handlerStore(t, AgentRecord{ThreadID: "thread-1"})
	config := handlerConfig()
	bot := NewBot(config, api, discord, store, NewSyncer(api, discord, store, config))

	if err := bot.HandleMessage(context.Background(), MessageEvent{GuildID: "other", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111", Content: "secret"}); err != nil {
		t.Fatal(err)
	}
	if api.prompt != "" || len(discord.messages) != 0 {
		t.Fatalf("unauthorized message reached handler: prompt=%q messages=%v", api.prompt, discord.messages)
	}

	if err := bot.HandleMessage(context.Background(), MessageEvent{GuildID: "guild", ChannelID: "unknown", ParentID: "forum", AuthorID: "111", Content: "hello"}); !errors.Is(err, errUnknownThread) {
		t.Fatalf("unknown thread error = %v", err)
	}
	if len(discord.messages) != 1 || !strings.Contains(discord.messages[0], "not linked") {
		t.Fatalf("unknown-thread acknowledgment missing: %v", discord.messages)
	}
}

func TestHandleMessageControlsAndAcknowledgesAgentErrors(t *testing.T) {
	apiErr := errors.New("agent unavailable")
	api := &handlerAgentAPI{promptErr: apiErr, interruptErr: apiErr, closeErr: apiErr}
	discord := &handlerDiscordClient{}
	store := handlerStore(t, AgentRecord{ThreadID: "thread-1"})
	config := handlerConfig()
	bot := NewBot(config, api, discord, store, NewSyncer(api, discord, store, config))
	event := MessageEvent{GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111"}

	for _, command := range []string{"/interrupt", "/close", "prompt"} {
		event.Content = command
		if err := bot.HandleMessage(context.Background(), event); !errors.Is(err, apiErr) {
			t.Fatalf("%s error = %v, want %v", command, err, apiErr)
		}
	}
	if len(discord.messages) != 3 {
		t.Fatalf("got %d error acknowledgments, want 3: %v", len(discord.messages), discord.messages)
	}
	for _, message := range discord.messages {
		if !strings.Contains(message, "couldn't") {
			t.Fatalf("error acknowledgment = %q", message)
		}
	}
}

func TestHandleMessageRunsInterruptAndCloseControls(t *testing.T) {
	api := &handlerAgentAPI{}
	discord := &handlerDiscordClient{}
	store := handlerStore(t, AgentRecord{ThreadID: "thread-1"})
	config := handlerConfig()
	bot := NewBot(config, api, discord, store, NewSyncer(api, discord, store, config))
	event := MessageEvent{GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111"}

	event.Content = " /interrupt "
	if err := bot.HandleMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	event.Content = "\n/close\t"
	if err := bot.HandleMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if api.interrupt != "pane-1" || api.close != "pane-1" {
		t.Fatalf("control routes used wrong pane: interrupt=%q close=%q", api.interrupt, api.close)
	}
	if got, want := strings.Join(discord.messages, "\n"), "Agent interrupted.\nClose requested."; got != want {
		t.Fatalf("control acknowledgments = %q, want %q", got, want)
	}
}

func TestHandleMessageRejectsBotMessages(t *testing.T) {
	api := &handlerAgentAPI{}
	discord := &handlerDiscordClient{}
	store := handlerStore(t, AgentRecord{ThreadID: "thread-1"})
	config := handlerConfig()
	bot := NewBot(config, api, discord, store, NewSyncer(api, discord, store, config))

	if err := bot.HandleMessage(context.Background(), MessageEvent{
		GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111", Content: "loop", AuthorIsBot: true,
	}); err != nil {
		t.Fatal(err)
	}
	if api.prompt != "" || len(discord.messages) != 0 {
		t.Fatalf("bot message was handled: prompt=%q messages=%v", api.prompt, discord.messages)
	}
}

func TestNewDiscordgoClientConfiguresRequiredGatewayIntents(t *testing.T) {
	client, err := NewDiscordgoClient(Config{Token: "not-logged"})
	if err != nil {
		t.Fatal(err)
	}
	if client.session.Identify.Intents != discordGatewayIntents {
		t.Fatalf("gateway intents = %d, want %d", client.session.Identify.Intents, discordGatewayIntents)
	}
}

func TestBotRunReconcilesImmediatelyAndClosesOnCancellation(t *testing.T) {
	api := &runAgentAPI{firstOverview: make(chan struct{})}
	discord := &runDiscordClient{}
	store := handlerStore(t, AgentRecord{})
	config := handlerConfig()
	config.SyncInterval = time.Hour
	bot := NewBot(config, api, discord, store, NewSyncer(api, discord, store, config))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-api.firstOverview
		cancel()
	}()

	if err := bot.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !discord.opened || !discord.closed {
		t.Fatalf("gateway lifecycle = opened %v, closed %v", discord.opened, discord.closed)
	}
	if api.overviewCalls.Load() != 1 {
		t.Fatalf("overview calls = %d, want immediate reconciliation only", api.overviewCalls.Load())
	}
}

func handlerConfig() Config {
	return Config{GuildID: "guild", ForumChannelID: "forum", AllowedUserIDs: map[string]struct{}{"111": {}}}
}

func handlerStore(t *testing.T, record AgentRecord) *StateStore {
	t.Helper()
	store, err := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("pane-1", record); err != nil {
		t.Fatal(err)
	}
	return store
}
