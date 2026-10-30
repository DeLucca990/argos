package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/DeLucca990/argos-bot/internal/dotenv"
	"github.com/DeLucca990/argos-bot/internal/store"
	"github.com/DeLucca990/argos-bot/internal/telegram"
)

func main() {
	log.SetOutput(os.Stderr)

	dotenv.LoadEnvVariables()

	switch command := strings.Join(os.Args[1:], " "); command {
	case "":
		runBot()
	case "migrate":
		migrate()
	case "healthcheck":
		if err := telegram.Healthy(); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown command %q: argos runs the bot; argos migrate only migrates its database; "+
			"argos healthcheck says whether a running bot is answering", command)
	}
}

func runBot() {
	cfg, err := telegram.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	st := openStore(cfg.DataDir)
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bot, err := telegram.New(cfg, st)
	if err != nil {
		log.Fatal(err)
	}

	if cfg.BackupKeep > 0 {
		dir := filepath.Join(cfg.DataDir, "backups")
		log.Printf("backups to %s every %s, keeping the newest %d", dir, cfg.BackupEvery, cfg.BackupKeep)
		go backups(ctx, st, dir, cfg.BackupKeep, cfg.BackupEvery)
	}

	bot.Run(ctx)
	log.Println("bot stopped")
}

func migrate() {
	dir, err := telegram.DataDir()
	if err != nil {
		log.Fatal(err)
	}
	openStore(dir).Close()
}

func openStore(dir string) *store.Store {
	file := filepath.Join(dir, "argos.db")
	st, err := store.Open(file)
	if err != nil {
		log.Fatal(err)
	}

	state := fmt.Sprintf("at schema version %d, nothing to apply", st.Version())
	if applied := st.Applied(); len(applied) > 0 {
		state = fmt.Sprintf("applied %s, now at schema version %d", strings.Join(applied, ", "), st.Version())
	}
	log.Printf("database %s: %s", file, state)
	return st
}

func backups(ctx context.Context, st *store.Store, dir string, keep int, every time.Duration) {
	for {
		written, removed, next, err := st.ScheduledBackup(ctx, dir, keep, every, time.Now())
		wait := min(time.Until(next), time.Hour)
		switch {
		case err != nil:
			log.Printf("backup: %v", err)
			wait = min(every, time.Hour)
		case written != "":
			log.Printf("backup: wrote %s, keeping the newest %d, removed %d", written, keep, removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(wait, time.Second)):
		}
	}
}
