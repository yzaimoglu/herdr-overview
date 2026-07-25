package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const usageRequestTimeout = 4 * time.Second

var usageProviders = []string{"claude", "codex", "opencode"}

type usageWindow struct {
	Name        string    `json:"name"`
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

type providerUsage struct {
	Tool        string        `json:"tool"`
	Available   bool          `json:"available"`
	Windows     []usageWindow `json:"windows,omitempty"`
	Metrics     []usageMetric `json:"metrics,omitempty"`
	Error       string        `json:"error,omitempty"`
	RateLimited bool          `json:"-"`
}

type usageMetric struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type usageSnapshot struct {
	FetchedAt time.Time       `json:"fetchedAt"`
	Providers []providerUsage `json:"providers"`
}

func (s *server) fetchUsage() usageSnapshot {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if !s.usageCached.IsZero() && time.Since(s.usageCached) < usageCacheTTL {
		return s.usageCache
	}

	result := usageSnapshot{FetchedAt: nowUTC(), Providers: make([]providerUsage, len(usageProviders))}
	if s.usageLimited == nil {
		s.usageLimited = make(map[string]time.Time)
	}
	client := &http.Client{
		Timeout: usageRequestTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", s.usageSocket)
			},
		},
	}

	var wait sync.WaitGroup
	wait.Add(len(usageProviders))
	for index, provider := range usageProviders {
		go func() {
			defer wait.Done()
			if until, limited := s.usageLimited[provider]; limited && time.Now().Before(until) {
				result.Providers[index] = providerRateLimit(provider, until)
				return
			}
			result.Providers[index] = fetchProviderUsage(client, provider)
		}()
	}
	wait.Wait()
	for index, provider := range usageProviders {
		if result.Providers[index].RateLimited {
			s.usageLimited[provider] = time.Now().Add(usageRateLimitCooldown)
		}
	}
	s.usageCache = result
	s.usageCached = time.Now()
	return result
}

func fetchProviderUsage(client *http.Client, provider string) providerUsage {
	result := providerUsage{Tool: provider}
	request, err := http.NewRequest(http.MethodGet, "http://ai-sub-server/"+provider, nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	response, err := client.Do(request)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		result.RateLimited = response.StatusCode == http.StatusTooManyRequests
		var body struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(response.Body).Decode(&body) == nil && strings.TrimSpace(body.Error) != "" {
			result.Error = body.Error
		} else {
			result.Error = fmt.Sprintf("usage endpoint returned HTTP %d", response.StatusCode)
		}
		return result
	}

	var payload json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		result.Error = fmt.Sprintf("parse usage response: %v", err)
		return result
	}
	var envelope struct {
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		result.Error = fmt.Sprintf("parse usage response: %v", err)
		return result
	}
	if strings.TrimSpace(envelope.Tool) != "" {
		result.Tool = envelope.Tool
	}
	result.Available = true
	switch provider {
	case "claude":
		var data struct {
			Windows []usageWindow `json:"windows"`
		}
		if err := json.Unmarshal(payload, &data); err != nil {
			result.Error = fmt.Sprintf("parse Claude usage: %v", err)
			result.Available = false
			return result
		}
		result.Windows = data.Windows
	case "codex":
		result.Windows = parseCodexWindows(payload)
	case "opencode":
		result.Metrics = parseOpenCodeMetrics(payload)
	}
	return result
}

func providerRateLimit(provider string, until time.Time) providerUsage {
	return providerUsage{
		Tool:  provider,
		Error: fmt.Sprintf("rate limited; retry after %s", until.UTC().Format(time.RFC3339)),
	}
}

func parseCodexWindows(payload []byte) []usageWindow {
	type limit struct {
		LimitID            string  `json:"limitId"`
		LimitName          string  `json:"limitName"`
		UsedPercent        float64 `json:"usedPercent"`
		WindowDurationMins int     `json:"windowDurationMins"`
		ResetsAt           int64   `json:"resetsAt"`
	}
	type bucket struct {
		LimitID   string `json:"limitId"`
		LimitName string `json:"limitName"`
		Primary   *limit `json:"primary"`
		Secondary *limit `json:"secondary"`
	}
	data := struct {
		RateLimits struct {
			Primary   limit  `json:"primary"`
			Secondary *limit `json:"secondary"`
		} `json:"rate_limits"`
		ByLimitID map[string]bucket `json:"rate_limits_by_limit_id"`
	}{}
	if json.Unmarshal(payload, &data) != nil {
		return nil
	}
	limits := make([]limit, 0, len(data.ByLimitID)+2)
	seen := map[string]bool{}
	appendLimit := func(value limit) {
		if value.LimitID == "" || seen[value.LimitID] {
			return
		}
		seen[value.LimitID] = true
		limits = append(limits, value)
	}
	appendLimit(data.RateLimits.Primary)
	if data.RateLimits.Secondary != nil {
		appendLimit(*data.RateLimits.Secondary)
	}
	keys := make([]string, 0, len(data.ByLimitID))
	for key := range data.ByLimitID {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		bucket := data.ByLimitID[key]
		if bucket.Primary != nil {
			value := *bucket.Primary
			value.LimitID, value.LimitName = bucket.LimitID, bucket.LimitName
			appendLimit(value)
		}
		if bucket.Secondary != nil {
			value := *bucket.Secondary
			value.LimitID, value.LimitName = bucket.LimitID, bucket.LimitName
			appendLimit(value)
		}
	}
	windows := make([]usageWindow, 0, len(limits))
	for _, value := range limits {
		name := value.LimitName
		if name == "" {
			name = codexWindowName(value.WindowDurationMins)
		}
		windows = append(windows, usageWindow{
			Name: name, Utilization: value.UsedPercent,
			ResetsAt: time.Unix(value.ResetsAt, 0).UTC(),
		})
	}
	return windows
}

func codexWindowName(minutes int) string {
	if minutes >= 10000 {
		return "7-day"
	}
	if minutes >= 240 {
		return "5-hour"
	}
	return strconv.Itoa(minutes) + "-minute"
}

func parseOpenCodeMetrics(payload []byte) []usageMetric {
	data := struct {
		Stats struct {
			Sessions         int     `json:"sessions"`
			Messages         int     `json:"messages"`
			TotalCost        float64 `json:"total_cost"`
			InputTokens      int64   `json:"input_tokens"`
			OutputTokens     int64   `json:"output_tokens"`
			ReasoningTokens  int64   `json:"reasoning_tokens"`
			CacheReadTokens  int64   `json:"cache_read_tokens"`
			CacheWriteTokens int64   `json:"cache_write_tokens"`
		} `json:"stats"`
	}{}
	if json.Unmarshal(payload, &data) != nil {
		return nil
	}
	tokens := data.Stats.InputTokens + data.Stats.OutputTokens + data.Stats.ReasoningTokens + data.Stats.CacheReadTokens + data.Stats.CacheWriteTokens
	return []usageMetric{
		{Label: "Sessions", Value: strconv.Itoa(data.Stats.Sessions)},
		{Label: "Messages", Value: strconv.Itoa(data.Stats.Messages)},
		{Label: "Spend", Value: fmt.Sprintf("$%.2f", data.Stats.TotalCost)},
		{Label: "Tokens", Value: formatUsageNumber(tokens)},
	}
}

func formatUsageNumber(value int64) string {
	if value >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	}
	if value >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(value)/1_000)
	}
	return strconv.FormatInt(value, 10)
}
