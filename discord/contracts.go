package main

import "context"

// AgentAPI defines the overview and control operations used by the adapter.
type AgentAPI interface {
	Overview(context.Context) (Overview, error)
	Output(context.Context, string, int) (string, error)
	Prompt(context.Context, string, string) error
	Interrupt(context.Context, string) error
	Close(context.Context, string) error
}

// DiscordClient defines the Discord operations used by the adapter.
type DiscordClient interface {
	CreateForumThread(context.Context, string, string, string) (Thread, error)
	FindThreadByPane(context.Context, string, string) (Thread, bool, error)
	SendMessage(context.Context, string, string) error
	ArchiveThread(context.Context, string) error
}
