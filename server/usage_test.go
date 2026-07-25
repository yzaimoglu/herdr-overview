package main

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchUsage(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "usage.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	api := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/claude":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tool":    "claude",
				"windows": []map[string]any{{"name": "five_hour", "utilization": 12, "resets_at": "2026-07-25T15:30:00Z"}},
			})
		case "/codex":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tool": "codex",
				"rate_limits_by_limit_id": map[string]any{
					"codex": map[string]any{"limitId": "codex", "primary": map[string]any{"usedPercent": 3, "windowDurationMins": 10080, "resetsAt": 1785586214}},
				},
			})
		case "/opencode":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tool":  "opencode",
				"stats": map[string]any{"sessions": 2, "messages": 4, "total_cost": 1.25, "input_tokens": 1000},
			})
		default:
			http.Error(w, `{"error":"provider unavailable"}`, http.StatusBadGateway)
		}
	})}
	go func() { _ = api.Serve(listener) }()
	t.Cleanup(func() { _ = api.Close() })

	service := &server{usageSocket: socket}
	result := service.fetchUsage()
	cached := service.fetchUsage()
	if len(cached.Providers) != len(result.Providers) || requests.Load() != 3 {
		t.Fatalf("usage response was not cached: requests=%d", requests.Load())
	}
	if len(result.Providers) != 3 {
		t.Fatalf("expected three providers, got %+v", result.Providers)
	}
	if !result.Providers[0].Available || len(result.Providers[0].Windows) != 1 || result.Providers[0].Windows[0].Utilization != 12 {
		t.Fatalf("unexpected Claude usage: %+v", result.Providers[0])
	}
	if !result.Providers[1].Available || len(result.Providers[1].Windows) != 1 {
		t.Fatalf("unexpected Codex usage: %+v", result.Providers[1])
	}
	if !result.Providers[2].Available || len(result.Providers[2].Metrics) != 4 {
		t.Fatalf("unexpected OpenCode usage: %+v", result.Providers[2])
	}
}

func TestFetchUsageRateLimitCooldown(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "usage.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	api := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/claude" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"too many requests"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tool": strings.TrimPrefix(r.URL.Path, "/")})
	})}
	go func() { _ = api.Serve(listener) }()
	t.Cleanup(func() { _ = api.Close() })

	service := &server{usageSocket: socket}
	first := service.fetchUsage()
	if first.Providers[0].Available || !first.Providers[0].RateLimited {
		t.Fatalf("expected rate-limited Claude usage: %+v", first.Providers[0])
	}
	service.usageCached = time.Now().Add(-usageCacheTTL)
	second := service.fetchUsage()
	if !strings.Contains(second.Providers[0].Error, "retry after") || requests.Load() != 5 {
		t.Fatalf("rate limit cooldown was not applied: requests=%d usage=%+v", requests.Load(), second.Providers[0])
	}
}
