package main

import "strings"

// Agent describes a Herdr agent returned by the overview API.
type Agent struct {
	Name        string `json:"name"`
	Kind        string `json:"agent"`
	Status      string `json:"status"`
	WorkspaceID string `json:"workspaceId"`
	PaneID      string `json:"paneId"`
	TerminalID  string `json:"terminalId"`
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	Focused     bool   `json:"focused"`
}

// Overview contains the current Herdr agents.
type Overview struct {
	Source         string  `json:"source"`
	HerdrAvailable *bool   `json:"herdrAvailable"`
	Agents         []Agent `json:"agents"`
}

func (o Overview) isFallback() bool {
	return strings.EqualFold(strings.TrimSpace(o.Source), "demo") || o.HerdrAvailable != nil && !*o.HerdrAvailable
}

// Thread identifies a Discord forum thread associated with an agent.
type Thread struct {
	ID       string
	ParentID string
	Name     string
	Archived bool
}

// MessageEvent is an inbound Discord message relevant to the adapter.
type MessageEvent struct {
	GuildID     string
	ChannelID   string
	ParentID    string
	AuthorID    string
	Content     string
	AuthorIsBot bool
}
