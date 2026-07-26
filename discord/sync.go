package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

// Syncer reconciles Herdr panes with their Discord forum threads.
type Syncer struct {
	api         AgentAPI
	discord     DiscordClient
	state       *StateStore
	config      Config
	now         func() time.Time
	reconcileMu sync.Mutex
	locksMu     sync.Mutex
	paneLocks   map[string]*sync.Mutex
}

// NewSyncer creates a synchronizer using the supplied API, Discord client, and state store.
func NewSyncer(api AgentAPI, discord DiscordClient, state *StateStore, config Config) *Syncer {
	return &Syncer{api: api, discord: discord, state: state, config: config, now: time.Now, paneLocks: make(map[string]*sync.Mutex)}
}

// Reconcile mirrors one successful Herdr overview into Discord.
func (s *Syncer) Reconcile(ctx context.Context) error {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	overview, err := s.api.Overview(ctx)
	if err != nil {
		return err
	}
	if overview.isFallback() {
		return fmt.Errorf("overview API returned fallback data")
	}

	records := s.state.Records()
	live := make(map[string]struct{}, len(overview.Agents))
	outputAgents := make([]Agent, 0, len(overview.Agents))
	var firstErr error
	for _, agent := range overview.Agents {
		if agent.PaneID == "" {
			continue
		}
		live[agent.PaneID] = struct{}{}
		previous, exists := records[agent.PaneID]
		statusChanged := !exists || previous.Status != agent.Status || previous.Closed
		if err := s.syncAgentThread(ctx, agent, previous, exists); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("sync pane %s: %w", agent.PaneID, err)
			}
			continue
		}
		if agent.Status == "working" || statusChanged {
			outputAgents = append(outputAgents, agent)
		}
	}

	for paneID, record := range records {
		if _, ok := live[paneID]; ok || record.Closed || record.ThreadID == "" {
			continue
		}
		if err := s.archivePane(ctx, paneID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("archive pane %s: %w", paneID, err)
			}
		}
	}

	if err := s.syncOutputs(ctx, outputAgents); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *Syncer) syncAgentThread(ctx context.Context, agent Agent, previous AgentRecord, exists bool) error {
	unlock := s.lockPane(agent.PaneID)
	defer unlock()
	previous, exists = s.state.Get(agent.PaneID)

	thread, found, err := s.discord.FindThreadByPane(ctx, s.config.GuildID, agent.PaneID)
	if err != nil {
		return err
	}
	found = found && thread.ID != "" && thread.ParentID == s.config.ForumChannelID
	if found && thread.Archived {
		if err := s.discord.UnarchiveThread(ctx, thread.ID); err != nil {
			return err
		}
		thread.Archived = false
	}

	recreated := exists && previous.ThreadID != "" && !found
	if !found {
		var err error
		thread, err = s.discord.CreateForumThread(ctx, s.config.GuildID, s.config.ForumChannelID, threadName(agent))
		if err != nil {
			return err
		}
		if thread.ID == "" {
			return fmt.Errorf("Discord returned an empty thread ID")
		}
		if err := s.discord.SendMessage(ctx, thread.ID, initialMessage(agent)); err != nil {
			return err
		}
		if recreated {
			if err := s.discord.SendMessage(ctx, thread.ID, fmt.Sprintf("Discord thread was recreated for pane `%s`.", agent.PaneID)); err != nil {
				return err
			}
		}
	}
	if thread.ID == "" {
		return fmt.Errorf("Discord returned an empty thread ID")
	}

	if exists && previous.Status != "" && previous.Status != agent.Status {
		if err := s.discord.SendMessage(ctx, thread.ID, lifecycleMessage(agent, previous.Status)); err != nil {
			return err
		}
	}
	record := AgentRecord{
		ThreadID: thread.ID,
		Status:   agent.Status,
		Closed:   false,
		LastSync: s.now(),
	}
	if exists {
		record.OutputHash = previous.OutputHash
		record.Output = previous.Output
		record.OutputMessageHash = previous.OutputMessageHash
	}
	return s.state.Set(agent.PaneID, record)
}

func (s *Syncer) archivePane(ctx context.Context, paneID string) error {
	unlock := s.lockPane(paneID)
	defer unlock()
	record, ok := s.state.Get(paneID)
	if !ok || record.Closed || record.ThreadID == "" {
		return nil
	}
	if !record.ClosureSent {
		if err := s.discord.SendMessage(ctx, record.ThreadID, fmt.Sprintf("Agent for pane `%s` is no longer available; closing this thread.", paneID)); err != nil {
			return err
		}
		record.ClosureSent = true
		if err := s.state.Set(paneID, record); err != nil {
			return err
		}
	}
	if err := s.discord.ArchiveThread(ctx, record.ThreadID); err != nil {
		return err
	}
	record.Closed = true
	record.LastSync = s.now()
	return s.state.Set(paneID, record)
}

func initialMessage(agent Agent) string {
	return fmt.Sprintf("Agent: **%s**\nProvider: **%s**\nPane: `%s`\nWorkspace: `%s`\nStatus: **%s**\n\nNormal messages are prompts. Use `/interrupt` or `/close` for controls.", agent.Name, agent.Kind, agent.PaneID, agent.WorkspaceID, agent.Status)
}

func (s *Syncer) syncOutputs(ctx context.Context, agents []Agent) error {
	if len(agents) == 0 {
		return nil
	}

	jobs := make(chan Agent)
	errors := make(chan error, len(agents))
	workers := min(8, len(agents))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for agent := range jobs {
				if err := s.SyncAgentOutput(ctx, agent); err != nil {
					errors <- fmt.Errorf("sync output for pane %s: %w", agent.PaneID, err)
				}
			}
		}()
	}
	for _, agent := range agents {
		jobs <- agent
	}
	close(jobs)
	wg.Wait()
	close(errors)
	for err := range errors {
		return err
	}
	return nil
}

// SyncAgentOutput polls and publishes only output whose normalized hash changed.
func (s *Syncer) SyncAgentOutput(ctx context.Context, agent Agent) error {
	unlock := s.lockPane(agent.PaneID)
	defer unlock()

	record, ok := s.state.Get(agent.PaneID)
	if !ok || record.ThreadID == "" {
		return fmt.Errorf("no Discord thread mapping for pane %s", agent.PaneID)
	}
	output, err := s.api.Output(ctx, agent.PaneID, 0)
	if err != nil {
		return err
	}
	normalized := normalizeOutput(output)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(normalized)))
	if hash == record.OutputHash {
		return nil
	}
	if update, changed := outputUpdate(record.Output, normalized); changed && update != "" {
		messageHash := fmt.Sprintf("%x", sha256.Sum256([]byte(update)))
		if messageHash != record.OutputMessageHash {
			for _, chunk := range chunkMessage(update, maxDiscordPayload-fencedOutputOverhead) {
				if err := s.discord.SendMessage(ctx, record.ThreadID, fencedOutput(chunk)); err != nil {
					return err
				}
			}
			record.OutputMessageHash = messageHash
		}
	}
	record.OutputHash = hash
	record.Output = normalized
	record.LastSync = s.now()
	return s.state.Set(agent.PaneID, record)
}

func (s *Syncer) lockPane(paneID string) func() {
	s.locksMu.Lock()
	if s.paneLocks == nil {
		s.paneLocks = make(map[string]*sync.Mutex)
	}
	lock := s.paneLocks[paneID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.paneLocks[paneID] = lock
	}
	s.locksMu.Unlock()
	lock.Lock()
	return lock.Unlock
}
