package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPAgentAPIOverviewDecodesAgents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/overview" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"agents":[{"name":"builder","paneId":"w3:p5","status":"working"}]}`)
	}))
	defer server.Close()

	client := NewHTTPAgentAPI(server.URL, server.Client())
	got, err := client.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 1 || got.Agents[0].PaneID != "w3:p5" {
		t.Fatalf("unexpected overview: %+v", got)
	}
	if client.client.Timeout != 10*time.Second {
		t.Fatalf("unexpected client timeout: %s", client.client.Timeout)
	}
}

func TestHTTPAgentAPIOutputUsesEscapedPaneAndLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/agents/w3:p5/output" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
		}
		if r.URL.Query().Get("lines") != "7" {
			t.Errorf("unexpected lines query: %q", r.URL.Query().Get("lines"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"line 1\nline 2"}`)
	}))
	defer server.Close()

	client := NewHTTPAgentAPI(server.URL, server.Client())
	got, err := client.Output(context.Background(), "w3:p5", 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != "line 1\nline 2" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestHTTPAgentAPIEscapesPanePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/api/agents/work%2Fpane/output" {
			t.Errorf("unexpected escaped path: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"ok"}`)
	}))
	defer server.Close()

	client := NewHTTPAgentAPI(server.URL, server.Client())
	if _, err := client.Output(context.Background(), "work/pane", 1); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPAgentAPIPromptEscapesPaneAndSendsJSON(t *testing.T) {
	var gotPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/agents/w3:p5/prompt" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content type: %q", r.Header.Get("Content-Type"))
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode prompt: %v", err)
		}
		gotPrompt = body.Prompt
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{ "ok": true }`)
	}))
	defer server.Close()

	client := NewHTTPAgentAPI(server.URL, server.Client())
	if err := client.Prompt(context.Background(), "w3:p5", "check status"); err != nil {
		t.Fatal(err)
	}
	if gotPrompt != "check status" {
		t.Fatalf("unexpected prompt: %q", gotPrompt)
	}
}

func TestHTTPAgentAPIActionsUsePOST(t *testing.T) {
	for _, action := range []string{"interrupt", "close"} {
		t.Run(action, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/agents/w3:p5/"+action {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			client := NewHTTPAgentAPI(server.URL, server.Client())
			var err error
			if action == "interrupt" {
				err = client.Interrupt(context.Background(), "w3:p5")
			} else {
				err = client.Close(context.Background(), "w3:p5")
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHTTPAgentAPIRejectsNon2xxWithStatusAndAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"backend unavailable"}`)
	}))
	defer server.Close()

	client := NewHTTPAgentAPI(server.URL, server.Client())
	_, err := client.Overview(context.Background())
	if err == nil {
		t.Fatal("expected overview error")
	}
	if got := err.Error(); got != "502 Bad Gateway: backend unavailable" {
		t.Fatalf("unexpected error: %q", got)
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatalf("error leaked request URL: %q", err)
	}
}

func TestHTTPAgentAPIRejectsMalformedErrorWithoutResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "proxy private details")
	}))
	defer server.Close()

	client := NewHTTPAgentAPI(server.URL, server.Client())
	_, err := client.Overview(context.Background())
	if err == nil {
		t.Fatal("expected overview error")
	}
	if got := err.Error(); got != "502 Bad Gateway" {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestHTTPAgentAPIRejectsNon2xxForEveryOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"operation blocked"}`)
	}))
	defer server.Close()

	client := NewHTTPAgentAPI(server.URL, server.Client())
	cases := []struct {
		name string
		call func() error
	}{
		{name: "overview", call: func() error { _, err := client.Overview(context.Background()); return err }},
		{name: "output", call: func() error { _, err := client.Output(context.Background(), "pane", 1); return err }},
		{name: "prompt", call: func() error { return client.Prompt(context.Background(), "pane", "hello") }},
		{name: "interrupt", call: func() error { return client.Interrupt(context.Background(), "pane") }},
		{name: "close", call: func() error { return client.Close(context.Background(), "pane") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.call(); got == nil || got.Error() != "409 Conflict: operation blocked" {
				t.Fatalf("unexpected error: %v", got)
			}
		})
	}
}
