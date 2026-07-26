package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
)

const discordGatewayIntents = discordgo.IntentGuilds | discordgo.IntentGuildMessages | discordgo.IntentMessageContent

// DiscordgoClient adapts discordgo REST and Gateway operations to the bot contracts.
type DiscordgoClient struct {
	session *discordgo.Session
	config  Config
}

// NewDiscordgoClient creates a Discord session without logging the bot token.
func NewDiscordgoClient(config Config) (*DiscordgoClient, error) {
	session, err := discordgo.New("Bot " + config.Token)
	if err != nil {
		return nil, fmt.Errorf("create Discord session: %w", err)
	}
	session.Identify.Intents = discordGatewayIntents
	return &DiscordgoClient{session: session, config: config}, nil
}

func (c *DiscordgoClient) CreateForumThread(ctx context.Context, guildID, forumID, name string) (Thread, error) {
	if err := contextError(ctx); err != nil {
		return Thread{}, err
	}
	channel, err := c.session.ForumThreadStartComplex(forumID, &discordgo.ThreadStart{
		Name: canonicalThreadName(name),
		Type: discordgo.ChannelTypeGuildPublicThread,
	}, &discordgo.MessageSend{Content: "Thread created."})
	if err != nil {
		return Thread{}, fmt.Errorf("create forum thread in guild %s: %w", guildID, err)
	}
	if err := contextError(ctx); err != nil {
		return Thread{}, err
	}
	return threadFromChannel(channel), nil
}

func (c *DiscordgoClient) FindThreadByPane(ctx context.Context, guildID, paneID string) (Thread, bool, error) {
	if err := contextError(ctx); err != nil {
		return Thread{}, false, err
	}
	threads, err := c.session.GuildThreadsActive(guildID)
	if err != nil {
		return Thread{}, false, fmt.Errorf("list active Discord threads: %w", err)
	}
	if threads == nil {
		return Thread{}, false, nil
	}
	for _, channel := range threads.Threads {
		if channel == nil || channel.ParentID != c.config.ForumChannelID || channel.Name != adapterThreadName(paneID) {
			continue
		}
		return threadFromChannel(channel), true, nil
	}
	return Thread{}, false, contextError(ctx)
}

func (c *DiscordgoClient) SendMessage(ctx context.Context, threadID, content string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if _, err := c.session.ChannelMessageSend(threadID, content); err != nil {
		return fmt.Errorf("send Discord message: %w", err)
	}
	return contextError(ctx)
}

func (c *DiscordgoClient) ArchiveThread(ctx context.Context, threadID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	archived := true
	if _, err := c.session.ChannelEdit(threadID, &discordgo.ChannelEdit{Archived: &archived}); err != nil {
		return fmt.Errorf("archive Discord thread: %w", err)
	}
	return contextError(ctx)
}

// RegisterMessageHandler registers the single inbound message handler and returns its removal function.
func (c *DiscordgoClient) RegisterMessageHandler(ctx context.Context, handler func(context.Context, MessageEvent) error) func() {
	return c.session.AddHandler(func(session *discordgo.Session, event *discordgo.MessageCreate) {
		if err := c.handleMessage(ctx, session, event, handler); err != nil {
			logMessageHandlerError(err)
		}
	})
}

func (c *DiscordgoClient) handleMessage(ctx context.Context, session *discordgo.Session, event *discordgo.MessageCreate, handler func(context.Context, MessageEvent) error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if event == nil || event.Message == nil || event.Author == nil || event.GuildID == "" || event.GuildID != c.config.GuildID || event.ChannelID == "" || event.Author.ID == "" || event.Author.Bot {
		return nil
	}
	channel, err := session.Channel(event.ChannelID)
	if err != nil {
		return fmt.Errorf("lookup Discord channel: %w", err)
	}
	if channel == nil || channel.ParentID != c.config.ForumChannelID || !channel.IsThread() {
		return nil
	}
	return handler(ctx, MessageEvent{
		GuildID:     event.GuildID,
		ChannelID:   event.ChannelID,
		ParentID:    channel.ParentID,
		AuthorID:    event.Author.ID,
		Content:     event.Content,
		AuthorIsBot: event.Author.Bot,
	})
}

func logMessageHandlerError(error) {
	log.Printf("Discord message handling failed: handler error")
}

// Open connects the Discord Gateway.
func (c *DiscordgoClient) Open() error {
	if err := c.session.Open(); err != nil {
		return fmt.Errorf("open Discord Gateway: %w", err)
	}
	log.Printf("Discord Gateway connected")
	return nil
}

// Close disconnects the Discord Gateway.
func (c *DiscordgoClient) Close() error {
	if err := c.session.Close(); err != nil {
		return fmt.Errorf("close Discord Gateway: %w", err)
	}
	return nil
}

func threadFromChannel(channel *discordgo.Channel) Thread {
	thread := Thread{}
	if channel == nil {
		return thread
	}
	thread.ID = channel.ID
	thread.ParentID = channel.ParentID
	thread.Name = channel.Name
	thread.Archived = channel.ThreadMetadata != nil && channel.ThreadMetadata.Archived
	return thread
}

func adapterThreadName(paneID string) string {
	return "Herdr agent [" + paneID + "]"
}

func canonicalThreadName(name string) string {
	name = strings.TrimSpace(name)
	if end := strings.LastIndex(name, "]"); end == len(name)-1 {
		if start := strings.LastIndex(name[:end], " ["); start >= 0 && start+2 < end {
			return adapterThreadName(name[start+2 : end])
		}
	}
	return name
}

func contextError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
