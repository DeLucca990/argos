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
	_ "time/tzdata"

	"github.com/DeLucca990/argos-bot/internal/dotenv"
	"github.com/DeLucca990/argos-bot/internal/store"
	"github.com/DeLucca990/argos-bot/internal/telegram"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[argos] ")

	dotenv.LoadEnvVariables()

	switch command := strings.Join(os.Args[1:], " "); command {
	case "":
		runBot()
	case "migrate":
		migrate()
	default:
		log.Fatalf("unknown command %q: argos with no arguments runs the bot, argos migrate only migrates its database", command)
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
