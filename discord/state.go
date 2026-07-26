package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AgentRecord is the persisted Discord mapping and synchronization state for a pane.
type AgentRecord struct {
	ThreadID          string    `json:"threadId"`
	Status            string    `json:"status"`
	OutputHash        string    `json:"outputHash"`
	Output            string    `json:"output,omitempty"`
	OutputMessageHash string    `json:"outputMessageHash,omitempty"`
	LastSync          time.Time `json:"lastSync"`
	Closed            bool      `json:"closed"`
	ClosureSent       bool      `json:"closureSent"`
}

// StateStore keeps pane state in memory and persists it as an atomic JSON file.
type StateStore struct {
	path    string
	mu      sync.RWMutex
	records map[string]AgentRecord
}

// NewStateStore loads state from path, treating a missing file as empty state.
func NewStateStore(path string) (*StateStore, error) {
	store := &StateStore{path: path, records: make(map[string]AgentRecord)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(data, &store.records); err != nil {
		corruptPath := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		if renameErr := os.Rename(path, corruptPath); renameErr != nil {
			return nil, fmt.Errorf("quarantine corrupt state: %w", renameErr)
		}
		store.records = make(map[string]AgentRecord)
		return store, nil
	}
	if store.records == nil {
		store.records = make(map[string]AgentRecord)
	}
	return store, nil
}

// Get returns the record for paneID, if it exists.
func (s *StateStore) Get(paneID string) (AgentRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.records[paneID]
	return record, ok
}

// Set updates and durably persists the record for paneID.
func (s *StateStore) Set(paneID string, record AgentRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous, existed := s.records[paneID]
	s.records[paneID] = record
	if err := s.saveLocked(); err != nil {
		if existed {
			s.records[paneID] = previous
		} else {
			delete(s.records, paneID)
		}
		return err
	}
	return nil
}

// Records returns a copy of all persisted records.
func (s *StateStore) Records() map[string]AgentRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make(map[string]AgentRecord, len(s.records))
	for paneID, record := range s.records {
		records[paneID] = record
	}
	return records
}

func (s *StateStore) saveLocked() error {
	data, err := json.Marshal(s.records)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), "."+filepath.Base(s.path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary state permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	removeTemp = false
	return nil
}
