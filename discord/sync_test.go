package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAgentAPI struct {
	mu          sync.Mutex
	overview    Overview
	overviewErr error
	outputs     map[string]string
	outputErr   error
	outputCalls int
	current     int
	maxCurrent  int
}

func (f *fakeAgentAPI) Overview(context.Context) (Overview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.overviewErr != nil {
		return Overview{}, f.overviewErr
	}
	return f.overview, nil
}

func (f *fakeAgentAPI) Output(ctx context.Context, paneID string, _ int) (string, error) {
	f.mu.Lock()
	f.outputCalls++
	f.current++
	if f.current > f.maxCurrent {
		f.maxCurrent = f.current
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.current--
		f.mu.Unlock()
	}()
	select {
	case <-time.After(time.Millisecond):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.outputErr != nil {
		return "", f.outputErr
	}
	return f.outputs[paneID], nil
}

func (f *fakeAgentAPI) Prompt(context.Context, string, string) error { return nil }
func (f *fakeAgentAPI) Interrupt(context.Context, string) error      { return nil }
func (f *fakeAgentAPI) Close(context.Context, string) error          { return nil }

type fakeDiscordClient struct {
	mu       sync.Mutex
	created  []Thread
	found    map[string]Thread
	findCall int
	messages []struct {
		threadID string
		content  string
	}
	archived []string
	findErr  error
}

func (f *fakeDiscordClient) CreateForumThread(_ context.Context, _, _, name string) (Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	thread := Thread{ID: fmt.Sprintf("thread-%d", len(f.created)+1), ParentID: "forum", Name: name}
	f.created = append(f.created, thread)
	if f.found == nil {
		f.found = make(map[string]Thread)
	}
	if start, end := strings.LastIndex(name, " ["), strings.LastIndex(name, "]"); start >= 0 && end > start {
		f.found[name[start+2:end]] = thread
	}
	return thread, nil
}

func (f *fakeDiscordClient) FindThreadByPane(_ context.Context, _, paneID string) (Thread, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findCall++
	if f.findErr != nil {
		return Thread{}, false, f.findErr
	}
	thread, ok := f.found[paneID]
	return thread, ok, nil
}

func (f *fakeDiscordClient) SendMessage(_ context.Context, threadID, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, struct {
		threadID string
		content  string
	}{threadID, content})
	return nil
}

func (f *fakeDiscordClient) ArchiveThread(_ context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archived = append(f.archived, threadID)
	return nil
}

func testConfig() Config {
	return Config{GuildID: "guild", ForumChannelID: "forum"}
}

func testStore(t *testing.T) *StateStore {
	t.Helper()
	store, err := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestReconcileCreatesOneThreadAndDoesNotDuplicate(t *testing.T) {
	api := &fakeAgentAPI{overview: Overview{Agents: []Agent{{PaneID: "w3:p5", Name: "builder", Status: "working"}}}, outputs: map[string]string{"w3:p5": "output"}}
	discord := &fakeDiscordClient{}
	syncer := NewSyncer(api, discord, testStore(t), testConfig())

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(discord.created) != 1 {
		t.Fatalf("created %d threads", len(discord.created))
	}
}

func TestReconcilePublishesStatusAndDeduplicatesOutput(t *testing.T) {
	api := &fakeAgentAPI{overview: Overview{Agents: []Agent{{PaneID: "pane", Name: "worker", Status: "working"}}}, outputs: map[string]string{"pane": "same output"}}
	discord := &fakeDiscordClient{}
	syncer := NewSyncer(api, discord, testStore(t), testConfig())

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.overview.Agents[0].Status = "idle"
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(discord.messages) != 3 {
		t.Fatalf("got %d sent messages, want initial status, output, and lifecycle status", len(discord.messages))
	}
	if !strings.Contains(discord.messages[2].content, "idle") {
		t.Fatalf("status update missing: %q", discord.messages[2].content)
	}
}

func TestReconcileArchivesOnlyAfterSuccessfulOverview(t *testing.T) {
	api := &fakeAgentAPI{overview: Overview{Agents: []Agent{{PaneID: "gone", Name: "worker", Status: "idle"}}}}
	discord := &fakeDiscordClient{}
	store := testStore(t)
	syncer := NewSyncer(api, discord, store, testConfig())
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.overviewErr = errors.New("overview unavailable")
	api.overview = Overview{}
	if err := syncer.Reconcile(context.Background()); !errors.Is(err, api.overviewErr) {
		t.Fatalf("expected overview error, got %v", err)
	}
	if len(discord.archived) != 0 {
		t.Fatalf("archived during failed overview: %v", discord.archived)
	}
	record, ok := store.Get("gone")
	if !ok || record.Closed {
		t.Fatalf("state changed during failed overview: ok=%v record=%+v", ok, record)
	}

	api.overviewErr = nil
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(discord.archived) != 1 || !storeRecordClosed(store, "gone") {
		t.Fatalf("missing agent was not archived: archived=%v record=%+v", discord.archived, store.Records()["gone"])
	}
}

func TestReconcileRecoversDeletedThread(t *testing.T) {
	api := &fakeAgentAPI{overview: Overview{Agents: []Agent{{PaneID: "pane", Name: "worker", Status: "idle"}}}}
	discord := &fakeDiscordClient{found: map[string]Thread{"pane": {ID: "deleted-thread", ParentID: "forum"}}}
	store := testStore(t)
	if err := store.Set("pane", AgentRecord{ThreadID: "deleted-thread", Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	syncer := NewSyncer(api, discord, store, testConfig())
	delete(discord.found, "pane")

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(discord.created) != 1 {
		t.Fatalf("created %d replacement threads", len(discord.created))
	}
	record, _ := store.Get("pane")
	if record.ThreadID == "deleted-thread" {
		t.Fatalf("mapping was not replaced: %+v", record)
	}
	var recreated bool
	for _, message := range discord.messages {
		if strings.Contains(message.content, "recreated") {
			recreated = true
		}
	}
	if !recreated {
		t.Fatalf("replacement lifecycle message missing: %+v", discord.messages)
	}
}

func TestSyncAgentOutputPreservesStateOnAPIFailure(t *testing.T) {
	apiErr := errors.New("output unavailable")
	api := &fakeAgentAPI{outputErr: apiErr}
	discord := &fakeDiscordClient{}
	store := testStore(t)
	want := AgentRecord{ThreadID: "thread-1", Status: "working", OutputHash: "previous", LastSync: time.Unix(1, 0)}
	if err := store.Set("pane", want); err != nil {
		t.Fatal(err)
	}
	syncer := NewSyncer(api, discord, store, testConfig())
	if err := syncer.SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}); !errors.Is(err, apiErr) {
		t.Fatalf("expected output error, got %v", err)
	}
	if got, _ := store.Get("pane"); got != want {
		t.Fatalf("state changed after output failure: got=%+v want=%+v", got, want)
	}
}

func TestReconcileBoundsConcurrentOutputRequests(t *testing.T) {
	agents := make([]Agent, 32)
	outputs := make(map[string]string, len(agents))
	for i := range agents {
		paneID := fmt.Sprintf("pane-%d", i)
		agents[i] = Agent{PaneID: paneID, Name: paneID, Status: "working"}
		outputs[paneID] = "output"
	}
	api := &fakeAgentAPI{overview: Overview{Agents: agents}, outputs: outputs}
	syncer := NewSyncer(api, &fakeDiscordClient{}, testStore(t), testConfig())
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.maxCurrent > 8 {
		t.Fatalf("observed %d concurrent output calls", api.maxCurrent)
	}
}

func storeRecordClosed(store *StateStore, paneID string) bool {
	record, ok := store.Get(paneID)
	return ok && record.Closed
}
