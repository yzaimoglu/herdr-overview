package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStateStoreInitializesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discord-state.json")
	store, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Records(); len(got) != 0 {
		t.Fatalf("expected empty state, got %+v", got)
	}
}

func TestStateStoreRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discord-state.json")
	store, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := AgentRecord{
		ThreadID:      "thread-1",
		Status:        "working",
		OutputHash:    "abc",
		StreamEnabled: true,
		LastSync:      time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC),
	}
	if err := store.Set("w3:p5", want); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get("w3:p5")
	if !ok || got != want {
		t.Fatalf("unexpected state: ok=%v record=%+v", ok, got)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode is %o, want 600", info.Mode().Perm())
	}

	if err := os.WriteFile(path, []byte(`{"pane":{"threadId":"thread-1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if record, ok := legacy.Get("pane"); !ok || record.StreamEnabled {
		t.Fatalf("missing stream setting should default to false: ok=%v record=%+v", ok, record)
	}
}

func TestStateStoreAtomicallyReplacesState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "discord-state.json")
	store, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("pane", AgentRecord{ThreadID: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("pane", AgentRecord{ThreadID: "second"}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get("pane")
	if !ok || got.ThreadID != "second" {
		t.Fatalf("unexpected replacement: ok=%v record=%+v", ok, got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".discord-state.json.tmp-") {
			t.Fatalf("temporary state file was left behind: %s", entry.Name())
		}
	}
}

func TestStateStoreRecoversMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "discord-state.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Records(); len(got) != 0 {
		t.Fatalf("expected empty recovered state, got %+v", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var foundCorrupt bool
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "discord-state.json.corrupt-") {
			foundCorrupt = true
		}
	}
	if !foundCorrupt {
		t.Fatal("malformed state was not renamed")
	}
}

func TestStateStoreRecordsReturnsCopy(t *testing.T) {
	store, err := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("pane", AgentRecord{ThreadID: "thread"}); err != nil {
		t.Fatal(err)
	}

	got := store.Records()
	got["pane"] = AgentRecord{ThreadID: "mutated"}
	delete(got, "pane")
	if record, ok := store.Get("pane"); !ok || record.ThreadID != "thread" {
		t.Fatalf("store was mutated through copy: ok=%v record=%+v", ok, record)
	}
}

func TestStateStoreSupportsConcurrentAccess(t *testing.T) {
	store, err := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			paneID := "pane-" + string(rune('0'+worker))
			for i := 0; i < 10; i++ {
				if err := store.Set(paneID, AgentRecord{Status: "working"}); err != nil {
					t.Errorf("set state: %v", err)
				}
				store.Get(paneID)
				store.Records()
			}
		}(worker)
	}
	wg.Wait()
}
