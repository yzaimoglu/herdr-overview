package main

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

type Overview struct {
	Agents []Agent `json:"agents"`
}

type Thread struct {
	ID       string
	ParentID string
	Name     string
	Archived bool
}

type MessageEvent struct {
	GuildID     string
	ChannelID   string
	ParentID    string
	AuthorID    string
	Content     string
	AuthorIsBot bool
}
