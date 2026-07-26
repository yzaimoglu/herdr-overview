package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
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

func (f *runDiscordClient) RegisterMessageHandler(_ context.Context, handler func(context.Context, MessageEvent) error) func() {
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

func (*handlerDiscordClient) ArchiveThread(context.Context, string) error   { return nil }
func (*handlerDiscordClient) UnarchiveThread(context.Context, string) error { return nil }

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

func TestParseMessageStreamCommands(t *testing.T) {
	for _, test := range []struct {
		content string
		kind    string
	}{
		{content: " /stream on ", kind: messageStreamOn},
		{content: "\n/stream off\t", kind: messageStreamOff},
		{content: "/stream", kind: messagePrompt},
		{content: "/stream on now", kind: messagePrompt},
		{content: "/stream off now", kind: messagePrompt},
	} {
		got, _ := parseMessage(test.content)
		if got != test.kind {
			t.Fatalf("parseMessage(%q) = %q, want %q", test.content, got, test.kind)
		}
	}
}

func TestHandleMessageEnablesStreamWithoutReplayingHistory(t *testing.T) {
	api := &handlerAgentAPI{output: "  current output\r\n"}
	discord := &handlerDiscordClient{}
	store := handlerStore(t, AgentRecord{ThreadID: "thread-1", Output: "historical output", OutputHash: outputHash("historical output")})
	bot := NewBot(handlerConfig(), api, discord, store, NewSyncer(api, discord, store, handlerConfig()))

	err := bot.HandleMessage(context.Background(), MessageEvent{
		GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111", Content: " /stream on ",
	})
	if err != nil {
		t.Fatal(err)
	}
	record, ok := store.Get("pane-1")
	if !ok || !record.StreamEnabled || record.Output != "current output" || record.OutputHash != outputHash("current output") {
		t.Fatalf("stream state = ok %v record %+v", ok, record)
	}
	if got := strings.Join(discord.messages, "\n"); got != "Output streaming enabled." {
		t.Fatalf("acknowledgment = %q", got)
	}
}

func TestHandleMessageDoesNotClaimDisabledWhenStatePersistenceFails(t *testing.T) {
	api := &handlerAgentAPI{output: "refreshed output"}
	discord := &handlerDiscordClient{}
	store := handlerStore(t, AgentRecord{ThreadID: "thread-1", StreamEnabled: true, Output: "old output", OutputHash: outputHash("old output")})
	store.path = t.TempDir()
	bot := NewBot(handlerConfig(), api, discord, store, NewSyncer(api, discord, store, handlerConfig()))

	err := bot.HandleMessage(context.Background(), MessageEvent{
		GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111", Content: "/stream off",
	})
	if err == nil || !strings.Contains(err.Error(), "persist stream state") {
		t.Fatalf("error = %v, want persistence failure", err)
	}
	if len(discord.messages) != 1 || discord.messages[0] != "I couldn't save output streaming state." {
		t.Fatalf("acknowledgment = %v", discord.messages)
	}
	if record, ok := store.Get("pane-1"); !ok || !record.StreamEnabled || record.Output != "old output" {
		t.Fatalf("state changed after persistence failure: ok=%v record=%+v", ok, record)
	}
}

func TestHandleMessageDisablesStreamAndRefreshesCursor(t *testing.T) {
	api := &handlerAgentAPI{output: "  refreshed output\n"}
	discord := &handlerDiscordClient{}
	store := handlerStore(t, AgentRecord{ThreadID: "thread-1", StreamEnabled: true, Output: "old output", OutputHash: outputHash("old output")})
	bot := NewBot(handlerConfig(), api, discord, store, NewSyncer(api, discord, store, handlerConfig()))

	err := bot.HandleMessage(context.Background(), MessageEvent{
		GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111", Content: "\n/stream off\t",
	})
	if err != nil {
		t.Fatal(err)
	}
	record, ok := store.Get("pane-1")
	if !ok || record.StreamEnabled || record.Output != "refreshed output" || record.OutputHash != outputHash("refreshed output") {
		t.Fatalf("stream state = ok %v record %+v", ok, record)
	}
	if got := strings.Join(discord.messages, "\n"); got != "Output streaming disabled." {
		t.Fatalf("acknowledgment = %q", got)
	}
}

func TestHandleMessageDisablesStreamWhenCursorRefreshFails(t *testing.T) {
	refreshErr := errors.New("output unavailable")
	api := &handlerAgentAPI{output: "new output", outputErr: refreshErr}
	discord := &handlerDiscordClient{}
	store := handlerStore(t, AgentRecord{ThreadID: "thread-1", StreamEnabled: true, Output: "old output", OutputHash: outputHash("old output")})
	bot := NewBot(handlerConfig(), api, discord, store, NewSyncer(api, discord, store, handlerConfig()))

	err := bot.HandleMessage(context.Background(), MessageEvent{
		GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111", Content: "/stream off",
	})
	if !errors.Is(err, refreshErr) {
		t.Fatalf("error = %v, want %v", err, refreshErr)
	}
	record, ok := store.Get("pane-1")
	if !ok || record.StreamEnabled || record.Output != "old output" || record.OutputHash != outputHash("old output") {
		t.Fatalf("stream state = ok %v record %+v", ok, record)
	}
	if got := strings.Join(discord.messages, "\n"); got != "Output streaming disabled, but the cursor could not be refreshed." {
		t.Fatalf("acknowledgment = %q", got)
	}
}

func TestHandleMessageDoesNotEnableStreamWhenCursorInitializationFails(t *testing.T) {
	refreshErr := errors.New("output unavailable")
	api := &handlerAgentAPI{outputErr: refreshErr}
	discord := &handlerDiscordClient{}
	store := handlerStore(t, AgentRecord{ThreadID: "thread-1"})
	bot := NewBot(handlerConfig(), api, discord, store, NewSyncer(api, discord, store, handlerConfig()))

	err := bot.HandleMessage(context.Background(), MessageEvent{
		GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111", Content: "/stream on",
	})
	if !errors.Is(err, refreshErr) {
		t.Fatalf("error = %v, want %v", err, refreshErr)
	}
	record, ok := store.Get("pane-1")
	if !ok || record.StreamEnabled || record.Output != "" || record.OutputHash != "" {
		t.Fatalf("stream state = ok %v record %+v", ok, record)
	}
	if got := strings.Join(discord.messages, "\n"); got != "I couldn't enable output streaming." {
		t.Fatalf("acknowledgment = %q", got)
	}
}

func outputHash(output string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(normalizeOutput(output))))
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

func TestHandlePromptDoesNotForceRollingResponseSnapshot(t *testing.T) {
	previous := "old terminal screen"
	current := "new terminal screen"
	api := &fakeAgentAPI{outputs: map[string]string{"pane-1": current}}
	discord := &fakeDiscordClient{}
	store := handlerStore(t, AgentRecord{
		ThreadID:   "thread-1",
		Status:     "working",
		OutputHash: fmt.Sprintf("%x", sha256.Sum256([]byte(previous))),
		Output:     previous,
	})
	config := handlerConfig()
	bot := NewBot(config, api, discord, store, NewSyncer(api, discord, store, config))

	if err := bot.HandleMessage(context.Background(), MessageEvent{
		GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111", Content: "continue",
	}); err != nil {
		t.Fatal(err)
	}
	if len(discord.messages) != 1 || discord.messages[0].content != "Prompt sent." {
		t.Fatalf("prompt acknowledgment = %+v", discord.messages)
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

func TestMessageHandlerUsesRunContext(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "message-context")
	client := testDiscordgoClient(t, func(request *http.Request) (*http.Response, error) {
		if got := request.Context().Value(contextKey{}); got != "message-context" {
			t.Errorf("message lookup lost context value: %v", got)
		}
		return discordResponse(`{"id":"thread-1","guild_id":"guild","parent_id":"forum","type":11}`), nil
	})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var got context.Context
	wantErr := errors.New("handler failed with private prompt")
	err := client.handleMessage(ctx, client.session, &discordgo.MessageCreate{Message: &discordgo.Message{
		GuildID: "guild", ChannelID: "thread-1", Content: "private prompt", Author: &discordgo.User{ID: "111"},
	}}, func(gotContext context.Context, _ MessageEvent) error {
		got = gotContext
		return wantErr
	})
	if !errors.Is(err, wantErr) || got != ctx {
		t.Fatalf("handleMessage() error/context = %v/%v, want %v/%v", err, got, wantErr, ctx)
	}
}

func TestFindThreadByPaneUsesExactAdapterNameAndForumParent(t *testing.T) {
	client := testDiscordgoClient(t, func(*http.Request) (*http.Response, error) {
		return discordResponse(`{"threads":[
			{"id":"unrelated","name":"unrelated [pane-1]","parent_id":"forum","type":11},
			{"id":"renamed","name":"Herdr agent [pane-1] renamed","parent_id":"forum","type":11},
			{"id":"wrong-parent","name":"Herdr agent [pane-1]","parent_id":"other-forum","type":11},
			{"id":"exact","name":"Herdr agent [pane-1]","parent_id":"forum","type":11}
		]}`), nil
	})

	thread, found, err := client.FindThreadByPane(context.Background(), "guild", "pane-1")
	if err != nil || !found || thread.ID != "exact" {
		t.Fatalf("FindThreadByPane() = %+v, %v, %v", thread, found, err)
	}
	if got := canonicalThreadName("worker [pane-1]"); got != "Herdr agent [pane-1]" {
		t.Fatalf("canonicalThreadName() = %q", got)
	}
}

func TestFindThreadByPaneSearchesArchivedThreads(t *testing.T) {
	client := testDiscordgoClient(t, func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v9/guilds/guild/threads/active":
			return discordResponse(`{"threads":[]}`), nil
		case "/api/v9/channels/forum/threads/archived/public":
			return discordResponse(`{"threads":[{"id":"archived","name":"Herdr agent [pane-1]","parent_id":"forum","type":11,"thread_metadata":{"archived":true}}]}`), nil
		default:
			return nil, fmt.Errorf("unexpected Discord path %s", request.URL.Path)
		}
	})

	thread, found, err := client.FindThreadByPane(context.Background(), "guild", "pane-1")
	if err != nil || !found || thread.ID != "archived" || !thread.Archived {
		t.Fatalf("FindThreadByPane() = %+v, %v, %v", thread, found, err)
	}
}

func TestFindThreadByPanePaginatesArchivedThreads(t *testing.T) {
	client := testDiscordgoClient(t, func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v9/guilds/guild/threads/active":
			return discordResponse(`{"threads":[]}`), nil
		case "/api/v9/channels/forum/threads/archived/public":
			if request.URL.Query().Get("before") == "" {
				return discordResponse(`{"has_more":true,"threads":[{"id":"older","name":"not this pane","parent_id":"forum","type":11,"thread_metadata":{"archived":true,"archive_timestamp":"2026-07-26T12:00:00Z"}}]}`), nil
			}
			return discordResponse(`{"threads":[{"id":"archived","name":"Herdr agent [pane-1]","parent_id":"forum","type":11,"thread_metadata":{"archived":true}}]}`), nil
		default:
			return nil, fmt.Errorf("unexpected Discord path %s", request.URL.Path)
		}
	})

	thread, found, err := client.FindThreadByPane(context.Background(), "guild", "pane-1")
	if err != nil || !found || thread.ID != "archived" {
		t.Fatalf("FindThreadByPane() = %+v, %v, %v", thread, found, err)
	}
}

func TestCreateForumThreadUsesCanonicalAdapterName(t *testing.T) {
	var requestBody string
	client := testDiscordgoClient(t, func(request *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		requestBody = string(data)
		return discordResponse(`{"id":"thread-1","name":"Herdr agent [pane-1]","parent_id":"forum","type":11}`), nil
	})

	if _, err := client.CreateForumThread(context.Background(), "guild", "forum", "worker [pane-1]"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requestBody, `"name":"Herdr agent [pane-1]"`) {
		t.Fatalf("request used non-canonical thread name: %s", requestBody)
	}
}

func TestDiscordGeneratedMessagesDisableMentions(t *testing.T) {
	var requests []string
	client := testDiscordgoClient(t, func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		requests = append(requests, string(body))
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/threads") {
			return discordResponse(`{"id":"thread-1","name":"Herdr agent [pane-1]","parent_id":"forum","type":11}`), nil
		}
		return discordResponse(`{"id":"message-1"}`), nil
	})

	if _, err := client.CreateForumThread(context.Background(), "guild", "forum", "worker [pane-1]"); err != nil {
		t.Fatal(err)
	}
	if err := client.SendMessage(context.Background(), "thread-1", "@everyone"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d Discord message requests, want 2", len(requests))
	}
	for _, body := range requests {
		var payload struct {
			AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions"`
			Message         struct {
				AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("decode Discord payload %q: %v", body, err)
		}
		mentions := payload.AllowedMentions
		if mentions == nil {
			mentions = payload.Message.AllowedMentions
		}
		if mentions == nil || len(mentions.Parse) != 0 || len(mentions.Roles) != 0 || len(mentions.Users) != 0 || mentions.RepliedUser {
			t.Fatalf("unsafe allowed mentions in payload: %s", body)
		}
	}
}

func TestDiscordRESTRequestsUsePassedContext(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request-context")
	client := testDiscordgoClient(t, func(request *http.Request) (*http.Response, error) {
		if got := request.Context().Value(contextKey{}); got != "request-context" {
			return nil, fmt.Errorf("missing request context value: %v", got)
		}
		switch request.URL.Path {
		case "/api/v9/guilds/guild/threads/active":
			return discordResponse(`{"threads":[]}`), nil
		case "/api/v9/channels/forum/threads/archived/public":
			return discordResponse(`{"threads":[]}`), nil
		case "/api/v9/channels/forum/threads":
			return discordResponse(`{"id":"thread-1","parent_id":"forum","type":11}`), nil
		case "/api/v9/channels/thread-1/messages":
			return discordResponse(`{"id":"message-1"}`), nil
		case "/api/v9/channels/thread-1":
			return discordResponse(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected Discord path %s", request.URL.Path)
		}
	})

	if _, err := client.CreateForumThread(ctx, "guild", "forum", "worker [pane-1]"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.FindThreadByPane(ctx, "guild", "pane-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.SendMessage(ctx, "thread-1", "message"); err != nil {
		t.Fatal(err)
	}
	if err := client.ArchiveThread(ctx, "thread-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.UnarchiveThread(ctx, "thread-1"); err != nil {
		t.Fatal(err)
	}
}

func TestMessageHandlerFiltersBeforeDiscordLookup(t *testing.T) {
	lookups := 0
	client := testDiscordgoClient(t, func(*http.Request) (*http.Response, error) {
		lookups++
		return discordResponse(`{"id":"thread-1","parent_id":"forum","type":11}`), nil
	})
	for _, event := range []*discordgo.MessageCreate{
		{Message: &discordgo.Message{GuildID: "other", ChannelID: "thread-1", Author: &discordgo.User{ID: "111"}}},
		{Message: &discordgo.Message{GuildID: "guild", ChannelID: "thread-1", Author: nil}},
		{Message: &discordgo.Message{GuildID: "guild", ChannelID: "thread-1", Author: &discordgo.User{ID: "111", Bot: true}}},
		{Message: &discordgo.Message{GuildID: "guild", ChannelID: "thread-1", Author: &discordgo.User{ID: "222"}}},
		{Message: &discordgo.Message{GuildID: "guild", ChannelID: "", Author: &discordgo.User{ID: "111"}}},
	} {
		if err := client.handleMessage(context.Background(), client.session, event, func(context.Context, MessageEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if lookups != 0 {
		t.Fatalf("Discord lookups = %d, want 0", lookups)
	}

	lookups = 0
	client = testDiscordgoClient(t, func(*http.Request) (*http.Response, error) {
		lookups++
		return discordResponse(`{"id":"channel","parent_id":"other-forum","type":11}`), nil
	})
	handlerCalls := 0
	if err := client.handleMessage(context.Background(), client.session, &discordgo.MessageCreate{Message: &discordgo.Message{
		GuildID: "guild", ChannelID: "thread-1", Author: &discordgo.User{ID: "111"},
	}}, func(context.Context, MessageEvent) error {
		handlerCalls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if lookups != 1 || handlerCalls != 0 {
		t.Fatalf("wrong-channel handling = lookups %d, handler calls %d", lookups, handlerCalls)
	}
}

func TestMessageHandlerErrorLogDoesNotIncludeRawError(t *testing.T) {
	privatePrompt := "private prompt must not be logged"
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)

	logMessageHandlerError(errors.New(privatePrompt))
	if strings.Contains(output.String(), privatePrompt) || !strings.Contains(output.String(), "handler error") {
		t.Fatalf("unsafe handler log: %q", output.String())
	}
}

func testDiscordgoClient(t *testing.T, transport func(*http.Request) (*http.Response, error)) *DiscordgoClient {
	t.Helper()
	client, err := NewDiscordgoClient(Config{GuildID: "guild", ForumChannelID: "forum", Token: "test-token", AllowedUserIDs: map[string]struct{}{"111": {}}})
	if err != nil {
		t.Fatal(err)
	}
	client.session.Client = &http.Client{Transport: roundTripFunc(transport)}
	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func discordResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
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
