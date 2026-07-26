package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const agentAPITimeout = 10 * time.Second

// HTTPAgentAPI calls the existing Herdr overview HTTP API.
type HTTPAgentAPI struct {
	baseURL string
	client  *http.Client
}

// NewHTTPAgentAPI creates an API client using the supplied transport settings.
func NewHTTPAgentAPI(baseURL string, client *http.Client) *HTTPAgentAPI {
	if client == nil {
		client = &http.Client{}
	}
	configured := *client
	configured.Timeout = agentAPITimeout
	return &HTTPAgentAPI{baseURL: strings.TrimRight(baseURL, "/"), client: &configured}
}

func (a *HTTPAgentAPI) Overview(ctx context.Context) (Overview, error) {
	var overview Overview
	if err := a.do(ctx, http.MethodGet, "/api/overview", nil, &overview); err != nil {
		return Overview{}, err
	}
	return overview, nil
}

func (a *HTTPAgentAPI) Output(ctx context.Context, paneID string, lines int) (string, error) {
	path := a.agentPath(paneID, "output")
	if lines > 0 {
		path += "?" + url.Values{"lines": {fmt.Sprint(lines)}}.Encode()
	}
	var response struct {
		Text string `json:"text"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return "", err
	}
	return response.Text, nil
}

func (a *HTTPAgentAPI) Prompt(ctx context.Context, paneID, prompt string) error {
	return a.do(ctx, http.MethodPost, a.agentPath(paneID, "prompt"), struct {
		Prompt string `json:"prompt"`
	}{Prompt: prompt}, nil)
}

func (a *HTTPAgentAPI) Interrupt(ctx context.Context, paneID string) error {
	return a.do(ctx, http.MethodPost, a.agentPath(paneID, "interrupt"), nil, nil)
}

func (a *HTTPAgentAPI) Close(ctx context.Context, paneID string) error {
	return a.do(ctx, http.MethodPost, a.agentPath(paneID, "close"), nil, nil)
}

func (a *HTTPAgentAPI) agentPath(paneID, action string) string {
	return "/api/agents/" + url.PathEscape(paneID) + "/" + action
}

func (a *HTTPAgentAPI) do(ctx context.Context, method, path string, payload, result any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode API request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create API request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return responseError(resp)
	}
	if result == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decode API response: %w", err)
	}
	return nil
}

func responseError(resp *http.Response) error {
	var response struct {
		Error string `json:"error"`
	}
	data, err := io.ReadAll(resp.Body)
	if err == nil {
		_ = json.Unmarshal(data, &response)
	}
	message := strings.TrimSpace(response.Error)
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("%s: %s", resp.Status, message)
}
