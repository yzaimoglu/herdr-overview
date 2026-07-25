package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	agents, err := s.listAgents()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"herdrAvailable": err == nil,
		"agentCount":     len(agents),
	})
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	agents, err := s.listAgents()
	result := overview{
		Source: "live", HerdrAvailable: err == nil, FetchedAt: nowUTC(), Activities: s.currentActivities(), Usage: s.fetchUsage(),
	}
	if err != nil {
		if !s.allowDemo {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		result.Source = "demo"
		result.Notice = "Herdr is unavailable; showing demo data."
		result.Agents = demoAgents()
	} else {
		result.Agents = agents
	}
	result.Stats = calculateStats(result.Agents)
	writeJSON(w, http.StatusOK, result)
}

func nowUTC() (resultTime time.Time) {
	return time.Now().UTC()
}

func (s *server) handleAgent(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "agents" {
		writeError(w, http.StatusNotFound, fmt.Errorf("agent route not found"))
		return
	}
	target, err := url.PathUnescape(parts[2])
	if err != nil || strings.TrimSpace(target) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid agent target"))
		return
	}
	switch parts[3] {
	case "output":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("output requires GET"))
			return
		}
		s.handleOutput(w, r, target)
	case "prompt":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("prompt requires POST"))
			return
		}
		s.handlePrompt(w, r, target)
	case "interrupt":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("interrupt requires POST"))
			return
		}
		s.handleInterrupt(w, r, target)
	case "close":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("close requires POST"))
			return
		}
		s.handleClose(w, r, target)
	default:
		writeError(w, http.StatusNotFound, fmt.Errorf("agent action %q not found", parts[3]))
	}
}

func (s *server) handleOutput(w http.ResponseWriter, r *http.Request, target string) {
	lines := 45
	if value := r.URL.Query().Get("lines"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("lines must be a positive integer"))
			return
		}
		if parsed > 200 {
			parsed = 200
		}
		lines = parsed
	}
	output, err := s.run("agent", "read", target, "--source", "recent-unwrapped", "--lines", strconv.Itoa(lines))
	if err != nil {
		if s.allowDemo {
			writeJSON(w, http.StatusOK, map[string]string{"text": demoOutput(target)})
			return
		}
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": normalizeReadOutput(output)})
}

func (s *server) handlePrompt(w http.ResponseWriter, r *http.Request, target string) {
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Prompt) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("prompt is required"))
		return
	}
	agent, err := s.resolveAgent(target)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if _, err := s.run("agent", "prompt", target, strings.TrimSpace(body.Prompt)); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	s.record("prompt", agent)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent": agent})
}

func (s *server) handleInterrupt(w http.ResponseWriter, r *http.Request, target string) {
	agent, err := s.resolveAgent(target)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if _, err := s.run("agent", "send-keys", target, "ctrl+c"); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	s.record("interrupted", agent)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleClose(w http.ResponseWriter, r *http.Request, target string) {
	agent, err := s.resolveAgent(target)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if _, err := s.run("pane", "close", target); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	s.record("closed", agent)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) resolveAgent(target string) (Agent, error) {
	agents, err := s.listAgents()
	if err != nil {
		return Agent{}, err
	}
	for _, agent := range agents {
		if agent.PaneID == target || agent.Name == target {
			return agent, nil
		}
	}
	return Agent{}, fmt.Errorf("agent %q no longer exists", target)
}

type startAgentRequest struct {
	Name string   `json:"name"`
	Kind string   `json:"kind"`
	Pane string   `json:"pane"`
	Args []string `json:"args"`
}

func (s *server) handleStartAgent(w http.ResponseWriter, r *http.Request) {
	var body startAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body"))
		return
	}
	body.Name, body.Kind, body.Pane = strings.TrimSpace(body.Name), strings.TrimSpace(body.Kind), strings.TrimSpace(body.Pane)
	if body.Name == "" || body.Kind == "" || body.Pane == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("name, kind, and pane are required"))
		return
	}
	args := []string{"agent", "start", body.Name, "--kind", body.Kind, "--pane", body.Pane}
	if len(body.Args) > 0 {
		args = append(args, "--")
		args = append(args, body.Args...)
	}
	if _, err := s.run(args...); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	agent := Agent{Name: body.Name, Kind: body.Kind, Status: "working", PaneID: body.Pane}
	s.record("started", agent)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func normalizeReadOutput(raw string) string {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal([]byte(raw), &envelope) == nil && len(envelope.Result) > 0 {
		var result struct {
			Read struct {
				Text string `json:"text"`
			} `json:"read"`
		}
		if json.Unmarshal(envelope.Result, &result) == nil && result.Read.Text != "" {
			return strings.TrimSpace(result.Read.Text)
		}
	}
	return strings.TrimSpace(raw)
}

func demoOutput(target string) string {
	return fmt.Sprintf("$ herdr agent read %s\n\nSession is running in demo mode.\nNo live Herdr binary was found on PATH.\n\nConnect Herdr and refresh to see terminal output.", target)
}
