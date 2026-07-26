package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAgentAPI struct {
	mu                  sync.Mutex
	overview            Overview
	overviewErr         error
	outputs             map[string]string
	outputSequence      map[string][]string
	outputErr           error
	outputCalls         int
	current             int
	maxCurrent          int
	outputStarted       chan struct{}
	outputRelease       chan struct{}
	outputOnce          sync.Once
	outputSecondStarted chan struct{}
	outputSecondOnce    sync.Once
	overviewStarted     chan struct{}
	overviewRelease     chan struct{}
	overviewOnce        sync.Once
}

func (f *fakeAgentAPI) Overview(context.Context) (Overview, error) {
	if f.overviewStarted != nil {
		f.overviewOnce.Do(func() { close(f.overviewStarted) })
		<-f.overviewRelease
	}
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
	if f.outputCalls == 2 && f.outputSecondStarted != nil {
		f.outputSecondOnce.Do(func() { close(f.outputSecondStarted) })
	}
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
	if f.outputStarted != nil {
		f.outputOnce.Do(func() { close(f.outputStarted) })
		select {
		case <-f.outputRelease:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
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
	if sequence := f.outputSequence[paneID]; len(sequence) > 0 {
		output := sequence[0]
		f.outputSequence[paneID] = sequence[1:]
		return output, nil
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
	archived       []string
	archiveErr     error
	unarchived     []string
	findErr        error
	messageStarted chan struct{}
	messageOnce    sync.Once
	messageErr     error
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
	if f.messageStarted != nil {
		f.messageOnce.Do(func() { close(f.messageStarted) })
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, struct {
		threadID string
		content  string
	}{threadID, content})
	return f.messageErr
}

func (f *fakeDiscordClient) ArchiveThread(_ context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archived = append(f.archived, threadID)
	return f.archiveErr
}

func (f *fakeDiscordClient) UnarchiveThread(_ context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unarchived = append(f.unarchived, threadID)
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

func TestReconcilePreservesStreamEnabledAfterStateReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := AgentRecord{
		ThreadID:      "thread-1",
		Status:        "working",
		StreamEnabled: true,
		Output:        "cursor",
		OutputHash:    outputHash("cursor"),
	}
	if err := store.Set("pane", want); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAgentAPI{overview: Overview{Agents: []Agent{{PaneID: "pane", Status: "working"}}}, outputs: map[string]string{"pane": "cursor"}}
	discord := &fakeDiscordClient{found: map[string]Thread{"pane": {ID: "thread-1", ParentID: "forum"}}}
	syncer := NewSyncer(api, discord, reloaded, testConfig())

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if record, ok := reloaded.Get("pane"); !ok || !record.StreamEnabled {
		t.Fatalf("stream setting was not preserved: ok=%v record=%+v", ok, record)
	}
	persisted, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if record, ok := persisted.Get("pane"); !ok || !record.StreamEnabled {
		t.Fatalf("stream setting was not persisted after reconciliation: ok=%v record=%+v", ok, record)
	}
}

func TestReconcilePublishesStatusAndDeduplicatesOutput(t *testing.T) {
	api := &fakeAgentAPI{overview: Overview{Agents: []Agent{{PaneID: "pane", Name: "worker", Status: "working"}}}, outputs: map[string]string{"pane": "same output"}}
	discord := &fakeDiscordClient{}
	store := testStore(t)
	if err := store.Set("pane", AgentRecord{StreamEnabled: true}); err != nil {
		t.Fatal(err)
	}
	syncer := NewSyncer(api, discord, store, testConfig())

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

func TestReconcileDoesNotRepeatClosureNoticeWhenArchiveFails(t *testing.T) {
	api := &fakeAgentAPI{overview: Overview{Agents: []Agent{{PaneID: "gone", Name: "worker", Status: "idle"}}}}
	discord := &fakeDiscordClient{archiveErr: errors.New("archive unavailable")}
	store := testStore(t)
	syncer := NewSyncer(api, discord, store, testConfig())

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.overview = Overview{}
	if err := syncer.Reconcile(context.Background()); err == nil {
		t.Fatal("expected archive error")
	}
	if err := syncer.Reconcile(context.Background()); err == nil {
		t.Fatal("expected archive error")
	}

	closureNotices := 0
	for _, message := range discord.messages {
		if strings.Contains(message.content, "no longer available") {
			closureNotices++
		}
	}
	if closureNotices != 1 {
		t.Fatalf("sent %d closure notices, want 1: %+v", closureNotices, discord.messages)
	}
}

func TestReconcileRejectsFallbackOverviewBeforeDiscordChanges(t *testing.T) {
	api := &fakeAgentAPI{overview: Overview{Source: "demo", HerdrAvailable: boolPointer(false)}}
	discord := &fakeDiscordClient{}
	store := testStore(t)
	if err := store.Set("gone", AgentRecord{ThreadID: "thread-gone", Status: "working"}); err != nil {
		t.Fatal(err)
	}
	syncer := NewSyncer(api, discord, store, testConfig())

	if err := syncer.Reconcile(context.Background()); err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("expected fallback overview error, got %v", err)
	}
	if len(discord.created) != 0 || len(discord.messages) != 0 || len(discord.archived) != 0 {
		t.Fatalf("fallback overview changed Discord: created=%v messages=%v archived=%v", discord.created, discord.messages, discord.archived)
	}
	if record, ok := store.Get("gone"); !ok || record.Closed {
		t.Fatalf("fallback overview changed state: ok=%v record=%+v", ok, record)
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

func TestReconcileReusesArchivedAdapterThread(t *testing.T) {
	api := &fakeAgentAPI{overview: Overview{Agents: []Agent{{PaneID: "pane", Name: "worker", Status: "idle"}}}}
	discord := &fakeDiscordClient{found: map[string]Thread{"pane": {ID: "archived-thread", ParentID: "forum", Archived: true}}}
	syncer := NewSyncer(api, discord, testStore(t), testConfig())

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(discord.created) != 0 {
		t.Fatalf("created replacement for archived adapter thread: %v", discord.created)
	}
	if len(discord.unarchived) != 1 || discord.unarchived[0] != "archived-thread" {
		t.Fatalf("archived thread was not recovered: %v", discord.unarchived)
	}
	if record, ok := syncer.state.Get("pane"); !ok || record.ThreadID != "archived-thread" {
		t.Fatalf("archived thread mapping was not reused: ok=%v record=%+v", ok, record)
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

func TestSyncAgentOutputDoesNotSendWhenStreamDisabled(t *testing.T) {
	store := testStore(t)
	if err := store.Set("pane", AgentRecord{
		ThreadID:      "thread-1",
		Output:        "old output",
		OutputHash:    "previous-output-hash",
		StreamEnabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	api := &fakeAgentAPI{outputs: map[string]string{"pane": "old output\nnew output"}}
	discord := &fakeDiscordClient{}
	if err := NewSyncer(api, discord, store, testConfig()).SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}); err != nil {
		t.Fatal(err)
	}
	if len(discord.messages) != 0 {
		t.Fatalf("sent output while disabled: %+v", discord.messages)
	}
	if got, _ := store.Get("pane"); got.Output != "old output\nnew output" {
		t.Fatalf("cursor was not advanced: %+v", got)
	}
}

func TestSyncAgentOutputStreamsAppendOnlySuffix(t *testing.T) {
	previous := "line 1"
	store := testStore(t)
	if err := store.Set("pane", AgentRecord{
		ThreadID:      "thread-1",
		OutputHash:    outputHash(previous),
		Output:        previous,
		StreamEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	api := &fakeAgentAPI{outputs: map[string]string{"pane": previous + "\nline 2"}}
	discord := &fakeDiscordClient{}
	if err := NewSyncer(api, discord, store, testConfig()).SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}); err != nil {
		t.Fatal(err)
	}
	if len(discord.messages) != 1 || discord.messages[0].content != "```\nline 2\n```" {
		t.Fatalf("unexpected suffix message: %+v", discord.messages)
	}
}

func TestSyncAgentOutputDoesNotSendUnchangedStreamOutput(t *testing.T) {
	previous := "same output"
	want := AgentRecord{
		ThreadID:      "thread-1",
		OutputHash:    outputHash(previous),
		Output:        previous,
		StreamEnabled: true,
		LastSync:      time.Unix(1, 0),
	}
	store := testStore(t)
	if err := store.Set("pane", want); err != nil {
		t.Fatal(err)
	}
	discord := &fakeDiscordClient{}
	if err := NewSyncer(&fakeAgentAPI{outputs: map[string]string{"pane": previous}}, discord, store, testConfig()).SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}); err != nil {
		t.Fatal(err)
	}
	if len(discord.messages) != 0 {
		t.Fatalf("sent unchanged output: %+v", discord.messages)
	}
	if got, _ := store.Get("pane"); got != want {
		t.Fatalf("unchanged output rewrote state: got=%+v want=%+v", got, want)
	}
}

func TestSyncAgentOutputSuppressesRollingStreamReplacementAndAdvancesCursor(t *testing.T) {
	previous := "line 1\nline 2\nline 3"
	current := "line 2\nline 3\nline 4"
	store := testStore(t)
	if err := store.Set("pane", AgentRecord{
		ThreadID:      "thread-1",
		OutputHash:    outputHash(previous),
		Output:        previous,
		StreamEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	discord := &fakeDiscordClient{}
	if err := NewSyncer(&fakeAgentAPI{outputs: map[string]string{"pane": current}}, discord, store, testConfig()).SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}); err != nil {
		t.Fatal(err)
	}
	if len(discord.messages) != 0 {
		t.Fatalf("sent rolling replacement: %+v", discord.messages)
	}
	if got, _ := store.Get("pane"); got.Output != current || got.OutputHash != outputHash(current) {
		t.Fatalf("rolling replacement did not advance cursor: %+v", got)
	}
}

func TestSyncAgentOutputPreservesCursorWhenDiscordSendFails(t *testing.T) {
	previous := "line 1"
	want := AgentRecord{
		ThreadID:      "thread-1",
		OutputHash:    outputHash(previous),
		Output:        previous,
		StreamEnabled: true,
	}
	store := testStore(t)
	if err := store.Set("pane", want); err != nil {
		t.Fatal(err)
	}
	sendErr := errors.New("Discord unavailable")
	discord := &fakeDiscordClient{messageErr: sendErr}
	err := NewSyncer(&fakeAgentAPI{outputs: map[string]string{"pane": previous + "\nline 2"}}, discord, store, testConfig()).SyncAgentOutput(context.Background(), Agent{PaneID: "pane"})
	if !errors.Is(err, sendErr) {
		t.Fatalf("expected Discord send error, got %v", err)
	}
	if got, _ := store.Get("pane"); got != want {
		t.Fatalf("cursor advanced after Discord send failure: got=%+v want=%+v", got, want)
	}
}

func TestSyncAgentOutputPublishesOnlyNewOutputSuffix(t *testing.T) {
	previous := "line 1"
	current := "line 1\nline 2"
	store := testStore(t)
	if err := store.Set("pane", AgentRecord{
		ThreadID:      "thread-1",
		OutputHash:    fmt.Sprintf("%x", sha256.Sum256([]byte(normalizeOutput(previous)))),
		Output:        previous,
		StreamEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	api := &fakeAgentAPI{outputs: map[string]string{"pane": current}}
	discord := &fakeDiscordClient{}
	syncer := NewSyncer(api, discord, store, testConfig())

	if err := syncer.SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}); err != nil {
		t.Fatal(err)
	}
	if len(discord.messages) != 1 || discord.messages[0].content != "```\nline 2\n```" {
		t.Fatalf("unexpected suffix message: %+v", discord.messages)
	}
}

func TestSyncAgentOutputSuppressesRepeatedRollingUpdates(t *testing.T) {
	previous := "line 1\nline 2\nline 3"
	first := "line 2\nline 3\nline 4"
	second := "line 3\nline 4\nline 4"
	store := testStore(t)
	if err := store.Set("pane", AgentRecord{
		ThreadID:      "thread-1",
		OutputHash:    fmt.Sprintf("%x", sha256.Sum256([]byte(previous))),
		Output:        previous,
		StreamEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	api := &fakeAgentAPI{outputSequence: map[string][]string{"pane": {first, second}}}
	discord := &fakeDiscordClient{}
	syncer := NewSyncer(api, discord, store, testConfig())

	if err := syncer.SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}); err != nil {
		t.Fatal(err)
	}
	if err := syncer.SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}); err != nil {
		t.Fatal(err)
	}
	if len(discord.messages) != 0 {
		t.Fatalf("sent rolling updates: %+v", discord.messages)
	}
}

func TestSyncAgentOutputUsesSelfContainedFencesForLongOutput(t *testing.T) {
	store := testStore(t)
	if err := store.Set("pane", AgentRecord{ThreadID: "thread-1", StreamEnabled: true}); err != nil {
		t.Fatal(err)
	}
	api := &fakeAgentAPI{outputs: map[string]string{"pane": strings.Repeat("x", 4000)}}
	discord := &fakeDiscordClient{}
	syncer := NewSyncer(api, discord, store, testConfig())

	if err := syncer.SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}); err != nil {
		t.Fatal(err)
	}
	if len(discord.messages) < 2 {
		t.Fatalf("long output was not chunked: %d messages", len(discord.messages))
	}
	for i, message := range discord.messages {
		if len([]rune(message.content)) > maxDiscordPayload {
			t.Fatalf("message %d exceeds limit: %d", i, len([]rune(message.content)))
		}
		if !strings.HasPrefix(message.content, "```\n") || !strings.HasSuffix(message.content, "\n```") {
			t.Fatalf("message %d is not self-contained fenced Markdown: %q", i, message.content)
		}
	}
}

func TestReconcileRejectsThreadFromAnotherForum(t *testing.T) {
	api := &fakeAgentAPI{overview: Overview{Agents: []Agent{{PaneID: "pane", Name: "worker", Status: "idle"}}}}
	discord := &fakeDiscordClient{found: map[string]Thread{"pane": {ID: "wrong-thread", ParentID: "other-forum"}}}
	syncer := NewSyncer(api, discord, testStore(t), testConfig())

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(discord.created) != 1 || discord.created[0].ID == "wrong-thread" {
		t.Fatalf("wrong-parent thread was adopted: created=%+v", discord.created)
	}
}

func TestConcurrentOutputAndReconcileDoNotClobberState(t *testing.T) {
	api := &fakeAgentAPI{
		overview:        Overview{Agents: []Agent{{PaneID: "pane", Name: "worker", Status: "idle"}}},
		outputs:         map[string]string{"pane": "new output"},
		outputStarted:   make(chan struct{}),
		outputRelease:   make(chan struct{}),
		overviewStarted: make(chan struct{}),
		overviewRelease: make(chan struct{}),
	}
	discord := &fakeDiscordClient{
		found:          map[string]Thread{"pane": {ID: "thread-1", ParentID: "forum"}},
		messageStarted: make(chan struct{}),
	}
	store := testStore(t)
	if err := store.Set("pane", AgentRecord{ThreadID: "thread-1", Status: "working", Output: "old output"}); err != nil {
		t.Fatal(err)
	}
	syncer := NewSyncer(api, discord, store, testConfig())

	outputDone := make(chan error, 1)
	go func() { outputDone <- syncer.SyncAgentOutput(context.Background(), Agent{PaneID: "pane"}) }()
	<-api.outputStarted
	reconcileDone := make(chan error, 1)
	go func() { reconcileDone <- syncer.Reconcile(context.Background()) }()
	<-api.overviewStarted
	close(api.overviewRelease)
	select {
	case <-discord.messageStarted:
	case <-time.After(100 * time.Millisecond):
	}
	close(api.outputRelease)
	if err := <-outputDone; err != nil {
		t.Fatal(err)
	}
	if err := <-reconcileDone; err != nil {
		t.Fatal(err)
	}

	record, _ := store.Get("pane")
	if record.Status != "idle" || record.OutputHash == "" || record.Output != "new output" {
		t.Fatalf("stale state won: %+v", record)
	}
}

func TestStreamCommandWaitsForConcurrentReconciliation(t *testing.T) {
	api := &fakeAgentAPI{
		overview:            Overview{Agents: []Agent{{PaneID: "pane", Status: "working"}}},
		outputs:             map[string]string{"pane": "current output"},
		outputStarted:       make(chan struct{}),
		outputRelease:       make(chan struct{}),
		outputSecondStarted: make(chan struct{}),
	}
	discord := &fakeDiscordClient{found: map[string]Thread{"pane": {ID: "thread-1", ParentID: "forum"}}}
	store := testStore(t)
	if err := store.Set("pane", AgentRecord{ThreadID: "thread-1", Status: "working"}); err != nil {
		t.Fatal(err)
	}
	syncer := NewSyncer(api, discord, store, testConfig())
	bot := NewBot(handlerConfig(), api, discord, store, syncer)

	commandDone := make(chan error, 1)
	go func() {
		commandDone <- bot.HandleMessage(context.Background(), MessageEvent{
			GuildID: "guild", ChannelID: "thread-1", ParentID: "forum", AuthorID: "111", Content: "/stream on",
		})
	}()
	<-api.outputStarted

	reconcileDone := make(chan error, 1)
	go func() { reconcileDone <- syncer.Reconcile(context.Background()) }()
	select {
	case <-api.outputSecondStarted:
		t.Fatal("reconciliation started a concurrent output refresh for the pane")
	case <-time.After(100 * time.Millisecond):
	}
	close(api.outputRelease)
	if err := <-commandDone; err != nil {
		t.Fatal(err)
	}
	if err := <-reconcileDone; err != nil {
		t.Fatal(err)
	}
	if record, ok := store.Get("pane"); !ok || !record.StreamEnabled {
		t.Fatalf("stream command was clobbered: ok=%v record=%+v", ok, record)
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

func boolPointer(value bool) *bool {
	return &value
}
