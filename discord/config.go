package main

import (
	"fmt"
	"strings"
	"time"
)

// Config contains runtime settings for the Discord adapter.
type Config struct {
	Token          string
	GuildID        string
	ForumChannelID string
	AllowedUserIDs map[string]struct{}
	APIURL         string
	StatePath      string
	SyncInterval   time.Duration
}

// LoadConfig reads and validates adapter settings from getenv.
func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, fmt.Errorf("missing DISCORD_BOT_TOKEN")
	}

	config := Config{
		Token:          strings.TrimSpace(getenv("DISCORD_BOT_TOKEN")),
		GuildID:        strings.TrimSpace(getenv("DISCORD_GUILD_ID")),
		ForumChannelID: strings.TrimSpace(getenv("DISCORD_FORUM_CHANNEL_ID")),
		APIURL:         strings.TrimSpace(getenv("HERDR_OVERVIEW_API_URL")),
		StatePath:      strings.TrimSpace(getenv("DISCORD_STATE_PATH")),
	}
	if config.Token == "" {
		return Config{}, fmt.Errorf("missing DISCORD_BOT_TOKEN")
	}
	if config.GuildID == "" {
		return Config{}, fmt.Errorf("missing DISCORD_GUILD_ID")
	}
	if config.ForumChannelID == "" {
		return Config{}, fmt.Errorf("missing DISCORD_FORUM_CHANNEL_ID")
	}

	config.AllowedUserIDs = make(map[string]struct{})
	for _, userID := range strings.Split(getenv("DISCORD_ALLOWED_USER_IDS"), ",") {
		if userID = strings.TrimSpace(userID); userID != "" {
			config.AllowedUserIDs[userID] = struct{}{}
		}
	}
	if len(config.AllowedUserIDs) == 0 {
		return Config{}, fmt.Errorf("missing DISCORD_ALLOWED_USER_IDS")
	}

	if config.APIURL == "" {
		config.APIURL = "http://overview-api:8787"
	}
	if config.StatePath == "" {
		config.StatePath = "/data/discord-state.json"
	}

	interval := strings.TrimSpace(getenv("DISCORD_SYNC_INTERVAL"))
	if interval == "" {
		interval = "10s"
	}
	var err error
	config.SyncInterval, err = time.ParseDuration(interval)
	if err != nil || config.SyncInterval <= 0 {
		if err != nil {
			return Config{}, fmt.Errorf("invalid DISCORD_SYNC_INTERVAL: %w", err)
		}
		return Config{}, fmt.Errorf("invalid DISCORD_SYNC_INTERVAL: must be positive")
	}

	return config, nil
}
