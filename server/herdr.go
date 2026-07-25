package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

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

type activity struct {
	Action    string    `json:"action"`
	AgentName string    `json:"agentName"`
	AgentKind string    `json:"agentKind"`
	PaneID    string    `json:"paneId"`
	At        time.Time `json:"at"`
}

type overview struct {
	Source         string     `json:"source"`
	HerdrAvailable bool       `json:"herdrAvailable"`
	Notice         string     `json:"notice,omitempty"`
	FetchedAt      time.Time  `json:"fetchedAt"`
	Agents         []Agent    `json:"agents"`
	Activities     []activity `json:"activities"`
	Stats          stats      `json:"stats"`
	Usage          usageSnapshot `json:"usage"`
}

type stats struct {
	Total   int `json:"total"`
	Working int `json:"working"`
	Blocked int `json:"blocked"`
	Ready   int `json:"ready"`
}

type cliAgent struct {
	Name    string `json:"name"`
	Kind    string `json:"agent"`
	Session struct {
		Value string `json:"value"`
	} `json:"agent_session"`
	Status      string `json:"agent_status"`
	WorkspaceID string `json:"workspace_id"`
	PaneID      string `json:"pane_id"`
	TerminalID  string `json:"terminal_id"`
	CWD         string `json:"cwd"`
	Focused     bool   `json:"focused"`
}

func (s *server) listAgents() ([]Agent, error) {
	raw, err := s.run("agent", "list")
	if err != nil {
		return nil, err
	}
	return parseAgentList(raw)
}

func parseAgentList(raw string) ([]Agent, error) {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	payload := []byte(raw)
	if err := json.Unmarshal(payload, &envelope); err == nil && len(envelope.Result) > 0 {
		payload = envelope.Result
	}
	var result struct {
		Agents []cliAgent `json:"agents"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("parse agent list: %w", err)
	}
	if result.Agents == nil {
		return nil, fmt.Errorf("parse agent list: response has no agents")
	}
	agents := make([]Agent, 0, len(result.Agents))
	for _, agent := range result.Agents {
		agents = append(agents, Agent{
			Name: agent.Name, Kind: agent.Kind, Status: normalizeStatus(agent.Status),
			WorkspaceID: agent.WorkspaceID, PaneID: agent.PaneID, TerminalID: agent.TerminalID,
			SessionID: agent.Session.Value, CWD: agent.CWD, Focused: agent.Focused,
		})
	}
	return agents, nil
}

func normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "working", "running":
		return "working"
	case "blocked":
		return "blocked"
	case "idle":
		return "idle"
	case "done", "completed":
		return "done"
	default:
		return "unknown"
	}
}

func calculateStats(agents []Agent) stats {
	result := stats{Total: len(agents)}
	for _, agent := range agents {
		switch agent.Status {
		case "working":
			result.Working++
		case "blocked":
			result.Blocked++
		case "idle", "done":
			result.Ready++
		}
	}
	return result
}

func demoAgents() []Agent {
	return []Agent{
		{Name: "release-notes", Kind: "opencode", Status: "working", WorkspaceID: "herdr", PaneID: "w1:p2", TerminalID: "term-release", SessionID: "session-8f3c1a", CWD: "~/src/herdr", Focused: true},
		{Name: "api-review", Kind: "claude", Status: "blocked", WorkspaceID: "herdr", PaneID: "w1:p4", TerminalID: "term-review", SessionID: "session-3c18b0", CWD: "~/src/herdr", Focused: false},
		{Name: "ui-polish", Kind: "codex", Status: "idle", WorkspaceID: "herdr-overview", PaneID: "w1:p6", TerminalID: "term-ui", SessionID: "session-91d0aa", CWD: "~/src/herdr-overview", Focused: false},
	}
}

func demoActivities() []activity {
	now := time.Now().UTC()
	return []activity{
		{Action: "prompt", AgentName: "release-notes", AgentKind: "opencode", PaneID: "w1:p2", At: now.Add(-4 * time.Minute)},
		{Action: "started", AgentName: "ui-polish", AgentKind: "codex", PaneID: "w1:p6", At: now.Add(-19 * time.Minute)},
		{Action: "response", AgentName: "api-review", AgentKind: "claude", PaneID: "w1:p4", At: now.Add(-31 * time.Minute)},
	}
}
