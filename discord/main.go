package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type gateway interface {
	Open() error
	Close() error
	RegisterMessageHandler(func(context.Context, MessageEvent) error) func()
}

// Run opens the Discord Gateway, reconciles immediately, and then reconciles serially on a ticker.
func (b *Bot) Run(ctx context.Context) (err error) {
	if b.config.SyncInterval <= 0 {
		return fmt.Errorf("sync interval must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nil
	}
	gw, ok := b.discord.(gateway)
	if !ok {
		return os.ErrInvalid
	}
	removeHandler := gw.RegisterMessageHandler(b.HandleMessage)
	defer removeHandler()
	if err := gw.Open(); err != nil {
		return err
	}
	defer func() {
		closeErr := gw.Close()
		if err == nil && ctx.Err() == nil {
			err = closeErr
		}
	}()

	b.reconcile(ctx)
	ticker := time.NewTicker(b.config.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			b.reconcile(ctx)
		}
	}
}

func (b *Bot) reconcile(ctx context.Context) {
	if err := b.syncer.Reconcile(ctx); err != nil && ctx.Err() == nil {
		log.Printf("Discord reconciliation failed: %v", err)
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv); err != nil {
		log.Printf("Discord bot stopped: %v", err)
		return
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	config, err := LoadConfig(getenv)
	if err != nil {
		return err
	}
	api := NewHTTPAgentAPI(config.APIURL, &http.Client{})
	state, err := NewStateStore(config.StatePath)
	if err != nil {
		return err
	}
	discord, err := NewDiscordgoClient(config)
	if err != nil {
		return err
	}
	syncer := NewSyncer(api, discord, state, config)
	return NewBot(config, api, discord, state, syncer).Run(ctx)
}
