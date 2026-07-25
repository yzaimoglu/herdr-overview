package main

import "testing"

func TestParseAgentList(t *testing.T) {
	raw := `{"result":{"agents":[{"name":"builder","agent":"opencode","agent_status":"running","workspace_id":"w1","pane_id":"w1:p2","terminal_id":"term-1","agent_session":{"value":"sess-1"},"cwd":"/tmp/project","focused":true}]}}`
	agents, err := parseAgentList(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Status != "working" || agents[0].SessionID != "sess-1" {
		t.Fatalf("unexpected parsed agent: %+v", agents)
	}
}

func TestCalculateStats(t *testing.T) {
	result := calculateStats([]Agent{{Status: "working"}, {Status: "blocked"}, {Status: "done"}, {Status: "idle"}})
	if result.Total != 4 || result.Working != 1 || result.Blocked != 1 || result.Ready != 2 {
		t.Fatalf("unexpected stats: %+v", result)
	}
}
