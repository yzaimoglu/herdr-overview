package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfigDefaultsAndUsers(t *testing.T) {
	values := map[string]string{
		"DISCORD_BOT_TOKEN":        "token",
		"DISCORD_GUILD_ID":         "guild",
		"DISCORD_FORUM_CHANNEL_ID": "forum",
		"DISCORD_ALLOWED_USER_IDS": "111, 222",
	}
	config, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.APIURL != "http://overview-api:8787" || config.SyncInterval != 10*time.Second {
		t.Fatalf("unexpected defaults: %+v", config)
	}
	if config.StatePath != "/data/discord-state.json" {
		t.Fatalf("unexpected state path: %q", config.StatePath)
	}
	if _, ok := config.AllowedUserIDs["222"]; !ok {
		t.Fatalf("allowed user was not parsed: %+v", config.AllowedUserIDs)
	}
}

func TestLoadConfigRequiresControlSettings(t *testing.T) {
	_, err := LoadConfig(func(string) string { return "" })
	if err == nil {
		t.Fatal("expected missing configuration error")
	}
}

func TestLoadConfigRejectsMissingAllowedUsers(t *testing.T) {
	values := map[string]string{
		"DISCORD_BOT_TOKEN":        "token",
		"DISCORD_GUILD_ID":         "guild",
		"DISCORD_FORUM_CHANNEL_ID": "forum",
	}
	_, err := LoadConfig(func(key string) string { return values[key] })
	if err == nil || !strings.Contains(err.Error(), "DISCORD_ALLOWED_USER_IDS") {
		t.Fatalf("expected missing allowed users error, got %v", err)
	}
}

func TestLoadConfigRejectsInvalidInterval(t *testing.T) {
	values := map[string]string{
		"DISCORD_BOT_TOKEN":        "secret-token",
		"DISCORD_GUILD_ID":         "guild",
		"DISCORD_FORUM_CHANNEL_ID": "forum",
		"DISCORD_ALLOWED_USER_IDS": "111",
		"DISCORD_SYNC_INTERVAL":    "not-a-duration",
	}
	_, err := LoadConfig(func(key string) string { return values[key] })
	if err == nil || !strings.Contains(err.Error(), "DISCORD_SYNC_INTERVAL") {
		t.Fatalf("expected invalid interval error, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaked token: %v", err)
	}
}

func TestLoadConfigRejectsNonPositiveInterval(t *testing.T) {
	values := map[string]string{
		"DISCORD_BOT_TOKEN":        "token",
		"DISCORD_GUILD_ID":         "guild",
		"DISCORD_FORUM_CHANNEL_ID": "forum",
		"DISCORD_ALLOWED_USER_IDS": "111",
		"DISCORD_SYNC_INTERVAL":    "0s",
	}
	_, err := LoadConfig(func(key string) string { return values[key] })
	if err == nil || !strings.Contains(err.Error(), "DISCORD_SYNC_INTERVAL") {
		t.Fatalf("expected non-positive interval error, got %v", err)
	}
}

func TestLoadConfigNamesMissingRequiredVariable(t *testing.T) {
	base := map[string]string{
		"DISCORD_BOT_TOKEN":        "token",
		"DISCORD_GUILD_ID":         "guild",
		"DISCORD_FORUM_CHANNEL_ID": "forum",
		"DISCORD_ALLOWED_USER_IDS": "111",
	}
	for _, key := range []string{"DISCORD_BOT_TOKEN", "DISCORD_GUILD_ID", "DISCORD_FORUM_CHANNEL_ID"} {
		t.Run(key, func(t *testing.T) {
			values := mapsWithout(base, key)
			_, err := LoadConfig(func(name string) string { return values[name] })
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected missing %s error, got %v", key, err)
			}
		})
	}
}

func mapsWithout(values map[string]string, key string) map[string]string {
	result := make(map[string]string, len(values)-1)
	for name, value := range values {
		if name != key {
			result[name] = value
		}
	}
	return result
}
