package main

import (
	"fmt"
	"strings"
)

const maxDiscordPayload = 1900

const fencedOutputOverhead = len("```\n") + len("\n```")

func normalizeOutput(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

func outputUpdate(previous, current string) (string, bool) {
	previous = normalizeOutput(previous)
	current = normalizeOutput(current)
	if previous == current {
		return "", false
	}
	if strings.HasPrefix(current, previous) {
		return strings.TrimLeft(strings.TrimPrefix(current, previous), "\r\n"), true
	}
	if update, ok := rollingUpdate(previous, current); ok {
		return update, true
	}
	return current, true
}

func rollingUpdate(previous, current string) (string, bool) {
	previousLines := strings.Split(previous, "\n")
	currentLines := strings.Split(current, "\n")
	for count := min(len(previousLines), len(currentLines)); count > 0; count-- {
		start := len(previousLines) - count
		match := true
		for i := 0; i < count; i++ {
			if previousLines[start+i] != currentLines[i] {
				match = false
				break
			}
		}
		if match {
			overlap := strings.Join(currentLines[:count], "\n")
			return strings.TrimLeft(strings.TrimPrefix(current, overlap), "\r\n"), true
		}
	}
	return "", false
}

func chunkMessage(text string, limit int) []string {
	if text == "" || limit <= 0 {
		return nil
	}

	chunks := make([]string, 0, (len([]rune(text))+limit-1)/limit)
	current := ""
	flush := func() {
		if current != "" {
			chunks = append(chunks, current)
			current = ""
		}
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		if len([]rune(line)) <= limit {
			if len([]rune(current))+len([]rune(line)) > limit {
				flush()
			}
			current += line
			continue
		}

		flush()
		runes := []rune(line)
		for len(runes) > limit {
			chunks = append(chunks, string(runes[:limit]))
			runes = runes[limit:]
		}
		if len(runes) > 0 {
			current = string(runes)
		}
	}
	flush()
	return chunks
}

func formatOutput(text string) string {
	text = normalizeOutput(text)
	if text == "" {
		return ""
	}
	return fencedOutput(text)
}

func fencedOutput(text string) string {
	return "```\n" + text + "\n```"
}

func threadName(agent Agent) string {
	name := strings.TrimSpace(agent.Name)
	if name == "" {
		name = "agent"
	}
	paneID := strings.TrimSpace(agent.PaneID)
	suffix := " [" + paneID + "]"
	if len([]rune(suffix)) >= 100 {
		return string([]rune(suffix)[:100])
	}
	nameLimit := 100 - len([]rune(suffix))
	return string([]rune(name)[:min(len([]rune(name)), nameLimit)]) + suffix
}

func lifecycleMessage(agent Agent, previousStatus string) string {
	name := strings.TrimSpace(agent.Name)
	if name == "" {
		name = agent.PaneID
	}
	if previousStatus == "" {
		return fmt.Sprintf("Agent **%s** is now **%s**.", name, agent.Status)
	}
	return fmt.Sprintf("Agent **%s** changed status from **%s** to **%s**.", name, previousStatus, agent.Status)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
