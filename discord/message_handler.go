package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	messagePrompt    = "prompt"
	messageInterrupt = "interrupt"
	messageClose     = "close"
	messageIgnore    = "ignore"
)

var errUnknownThread = errors.New("message thread is not linked to a Herdr pane")

// Bot handles authorized Discord messages and runs reconciliation alongside the gateway.
type Bot struct {
	config  Config
	api     AgentAPI
	discord DiscordClient
	state   *StateStore
	syncer  *Syncer
}

// NewBot creates a Discord bot from the existing adapter services.
func NewBot(config Config, api AgentAPI, discord DiscordClient, state *StateStore, syncer *Syncer) *Bot {
	return &Bot{config: config, api: api, discord: discord, state: state, syncer: syncer}
}

func parseMessage(content string) (kind, body string) {
	trimmed := strings.TrimSpace(content)
	switch trimmed {
	case "/interrupt":
		return messageInterrupt, ""
	case "/close":
		return messageClose, ""
	case "":
		return messageIgnore, ""
	default:
		return messagePrompt, content
	}
}

func (b *Bot) authorized(event MessageEvent) bool {
	if event.GuildID != b.config.GuildID || event.ChannelID == "" || event.ParentID != b.config.ForumChannelID || event.AuthorID == "" || event.AuthorIsBot {
		return false
	}
	_, ok := b.config.AllowedUserIDs[event.AuthorID]
	return ok
}

// HandleMessage handles one inbound Discord message. Unauthorized messages are ignored.
func (b *Bot) HandleMessage(ctx context.Context, event MessageEvent) error {
	if !b.authorized(event) {
		return nil
	}
	kind, body := parseMessage(event.Content)
	if kind == messageIgnore {
		return nil
	}
	paneID, ok := b.paneForThread(event.ChannelID)
	if !ok {
		if err := b.discord.SendMessage(ctx, event.ChannelID, "This thread is not linked to a Herdr agent."); err != nil {
			return errors.Join(errUnknownThread, fmt.Errorf("acknowledge unknown thread: %w", err))
		}
		return errUnknownThread
	}

	switch kind {
	case messagePrompt:
		if err := b.api.Prompt(ctx, paneID, body); err != nil {
			return b.agentError(ctx, event.ChannelID, "I couldn't send that prompt to the agent.", "send prompt", err)
		}
		if err := b.discord.SendMessage(ctx, event.ChannelID, "Prompt sent."); err != nil {
			return fmt.Errorf("acknowledge prompt: %w", err)
		}
		if err := b.syncer.SyncAgentOutput(ctx, Agent{PaneID: paneID}); err != nil {
			return b.agentError(ctx, event.ChannelID, "Prompt sent, but I couldn't fetch the latest agent output.", "sync prompt output", err)
		}
	case messageInterrupt:
		if err := b.api.Interrupt(ctx, paneID); err != nil {
			return b.agentError(ctx, event.ChannelID, "I couldn't interrupt the agent.", "interrupt agent", err)
		}
		if err := b.discord.SendMessage(ctx, event.ChannelID, "Agent interrupted."); err != nil {
			return fmt.Errorf("acknowledge interrupt: %w", err)
		}
	case messageClose:
		if err := b.api.Close(ctx, paneID); err != nil {
			return b.agentError(ctx, event.ChannelID, "I couldn't close the agent.", "close agent", err)
		}
		if err := b.discord.SendMessage(ctx, event.ChannelID, "Close requested."); err != nil {
			return fmt.Errorf("acknowledge close: %w", err)
		}
	}
	return nil
}

func (b *Bot) paneForThread(threadID string) (string, bool) {
	for paneID, record := range b.state.Records() {
		if record.ThreadID == threadID && !record.Closed {
			return paneID, true
		}
	}
	return "", false
}

func (b *Bot) agentError(ctx context.Context, threadID, acknowledgment, operation string, err error) error {
	ackErr := b.discord.SendMessage(ctx, threadID, acknowledgment)
	if ackErr != nil {
		return errors.Join(fmt.Errorf("%s: %w", operation, err), fmt.Errorf("send error acknowledgment: %w", ackErr))
	}
	return fmt.Errorf("%s: %w", operation, err)
}
