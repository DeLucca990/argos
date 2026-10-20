package telegram

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
)

const (
	HeartbeatFile     = "/tmp/argos-heartbeat"
	heartbeatInterval = 30 * time.Second
	heartbeatMaxAge   = 2 * time.Minute
)

func (b *Bot) heartbeat(ctx context.Context) {
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	failing := false
	for {
		if err := b.alive(ctx); err != nil {
			if !failing {
				log.Printf("health: %v", err)
			}
			failing = true
		} else {
			if failing {
				log.Print("health: back to normal")
			}
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b *Bot) alive(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := b.tg.GetMe(ctx); err != nil {
		return fmt.Errorf("telegram does not answer: %w", err)
	}
	if err := b.store.Ping(ctx); err != nil {
		return fmt.Errorf("the database does not answer: %w", err)
	}
	return os.WriteFile(HeartbeatFile, []byte(time.Now().Format(time.RFC3339)), 0o600)
}

func Healthy() error {
	info, err := os.Stat(HeartbeatFile)
	if err != nil {
		return fmt.Errorf("no heartbeat yet: %w", err)
	}
	if age := time.Since(info.ModTime()); age > heartbeatMaxAge {
		return fmt.Errorf("the last heartbeat is %s old", age.Round(time.Second))
	}
	return nil
}
