package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputUpdateUsesOnlyNewSuffixWhenPossible(t *testing.T) {
	update, changed := outputUpdate("line 1\nline 2", "line 1\nline 2\nline 3")
	if !changed || update != "line 3" {
		t.Fatalf("unexpected update: changed=%v update=%q", changed, update)
	}
}

func TestOutputUpdateDetectsUnchangedAndReplacedOutput(t *testing.T) {
	if update, changed := outputUpdate("same\n", "same\r\n"); changed || update != "" {
		t.Fatalf("expected normalized output to be unchanged: changed=%v update=%q", changed, update)
	}
	if update, changed := outputUpdate("old output", "new output"); !changed || update != "" {
		t.Fatalf("expected rolling snapshot to be suppressed: changed=%v update=%q", changed, update)
	}
}

func TestOutputUpdateSuppressesRollingWindow(t *testing.T) {
	update, changed := outputUpdate("line 1\nline 2\nline 3", "line 2\nline 3\nline 4")
	if !changed || update != "line 4" {
		t.Fatalf("unexpected rolling update: changed=%v update=%q", changed, update)
	}
}

func TestFormatOutputUsesMarkdownFence(t *testing.T) {
	if got, want := formatOutput("line 1\nline 2"), "```\nline 1\nline 2\n```"; got != want {
		t.Fatalf("formatOutput() = %q, want %q", got, want)
	}
}

func TestChunkMessagePreservesLinesAndUTF8(t *testing.T) {
	text := strings.Repeat("é", 1900) + "\nnext line"
	chunks := chunkMessage(text, 1900)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	for i, chunk := range chunks {
		if len([]rune(chunk)) > 1900 {
			t.Fatalf("chunk %d has %d runes", i, len([]rune(chunk)))
		}
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is not valid UTF-8", i)
		}
	}
	if strings.Join(chunks, "") != text {
		t.Fatalf("chunk contents changed: %#v", chunks)
	}
}

func TestChunkMessageSplitsOversizedLineOnRuneBoundary(t *testing.T) {
	text := strings.Repeat("界", 1901)
	chunks := chunkMessage(text, 1900)
	if len(chunks) != 2 || len([]rune(chunks[0])) != 1900 || len([]rune(chunks[1])) != 1 {
		t.Fatalf("unexpected chunks: lengths=%d,%d", len([]rune(chunks[0])), len([]rune(chunks[1])))
	}
	if !utf8.ValidString(chunks[0] + chunks[1]) {
		t.Fatal("chunks do not preserve UTF-8")
	}
}

func TestThreadNameIsDeterministicAndBounded(t *testing.T) {
	agent := Agent{Name: strings.Repeat("builder", 20), PaneID: "w3:p5"}
	got := threadName(agent)
	if len([]rune(got)) > 100 || !strings.Contains(got, agent.PaneID) {
		t.Fatalf("invalid thread name %q", got)
	}
	if got != threadName(agent) {
		t.Fatal("thread name is not deterministic")
	}
}

func TestLifecycleMessageIncludesStatusTransition(t *testing.T) {
	message := lifecycleMessage(Agent{Name: "builder", Status: "idle"}, "working")
	if !strings.Contains(message, "working") || !strings.Contains(message, "idle") {
		t.Fatalf("lifecycle message omitted transition: %q", message)
	}
}
