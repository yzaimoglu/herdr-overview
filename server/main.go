package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const commandTimeout = 30 * time.Second
const usageCacheTTL = 5 * time.Minute
const usageRateLimitCooldown = 15 * time.Minute

type server struct {
	bin          string
	allowDemo    bool
	usageSocket  string
	mu           sync.RWMutex
	activities   []activity
	usageMu      sync.Mutex
	usageCache   usageSnapshot
	usageCached  time.Time
	usageLimited map[string]time.Time
}

func main() {
	s := &server{
		bin:         envOr("HERDR_BIN_PATH", "herdr"),
		allowDemo:   os.Getenv("HERDR_DEMO") != "false",
		usageSocket: envOr("AI_SUB_SERVER_SOCKET", "/run/user/1000/ai-sub-server.sock"),
		activities:  demoActivities(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("/api/agents/", s.handleAgent)
	mux.HandleFunc("POST /api/agents", s.handleStartAgent)

	address := envOr("HERDR_OVERVIEW_ADDR", ":8787")
	log.Printf("herdr overview API listening on %s", address)
	if err := http.ListenAndServe(address, withLogging(mux)); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func (s *server) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.bin, args...)
	if workingDir := strings.TrimSpace(os.Getenv("HERDR_WORKING_DIR")); workingDir != "" {
		cmd.Dir = filepath.Clean(workingDir)
	}
	output, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", fmt.Errorf("herdr command failed: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return "", fmt.Errorf("herdr command failed: %w", err)
}

func (s *server) record(action string, agent Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activities = append([]activity{{
		Action:    action,
		AgentName: agent.Name,
		AgentKind: agent.Kind,
		PaneID:    agent.PaneID,
		At:        time.Now().UTC(),
	}}, s.activities...)
	if len(s.activities) > 30 {
		s.activities = s.activities[:30]
	}
}

func (s *server) currentActivities() []activity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]activity(nil), s.activities...)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started).Round(time.Millisecond))
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
