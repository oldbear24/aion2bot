// Command aion2bot posts AION 2 raid "looking for group" requests to every
// Discord server that has set up an LFG channel.
package main

import (
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"aion2bot/internal/bot"
	"aion2bot/internal/config"
	"aion2bot/internal/lfg"
)

// version is set by the release build (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	if err := config.LoadDotEnv(".env"); err != nil {
		log.Fatalf("read .env: %v", err)
	}
	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN is not set. Put it in .env or the environment.")
	}

	game, err := config.LoadGame(config.EnvOr("GAME_FILE", "game.json"))
	if err != nil {
		log.Fatal(err)
	}
	dbPath := config.EnvOr("DB_FILE", "data/aion2bot.db")
	store, err := lfg.Open(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	// Versions before the database kept everything in store.json.
	legacy := filepath.Join(filepath.Dir(dbPath), "store.json")
	if imported, err := store.ImportJSON(legacy); err != nil {
		log.Fatal(err)
	} else if imported {
		log.Printf("Imported servers and posts from %s into %s", legacy, dbPath)
	}

	b, err := bot.New(token, game, store, os.Getenv("DEV_GUILD_ID"))
	if err != nil {
		log.Fatal(err)
	}
	if err := b.Start(); err != nil {
		log.Fatal(err)
	}
	log.Printf("AION 2 LFG bot %s is running with %d dungeons and %d classes. Press Ctrl+C to stop.",
		version, len(game.Dungeons), len(game.Classes))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("Shutting down…")
	b.Stop()
}
