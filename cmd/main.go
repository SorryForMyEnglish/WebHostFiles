package main

import (
	"fmt"
	"log"
	"os"

	"github.com/example/filestoragebot/bot"
	"github.com/example/filestoragebot/config"
	"github.com/example/filestoragebot/db"
	"github.com/example/filestoragebot/logdb"
	"github.com/example/filestoragebot/server"
	"github.com/example/filestoragebot/version"
)

func main() {
	banner := fmt.Sprintf("\n\x1b[34m*******************************\n*  FileStorage Bot v%s  *\n*******************************\x1b[0m\n", version.Version)
	fmt.Print(banner)

	created := false
	if _, err := os.Stat("config.yml"); os.IsNotExist(err) {
		created = true
	}

	cfg, err := config.Ensure("config.yml")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if created {
		log.Println("Default configuration generated at config.yml. Please edit it and restart.")
		return
	}

	database, err := db.New(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}

	logs, err := logdb.New(cfg.LogsDatabasePath)
	if err != nil {
		log.Fatalf("logs database: %v", err)
	}

	b, err := bot.New(cfg, database, logs)
	if err != nil {
		log.Fatalf("bot: %v", err)
	}

	go func() {
		if err := server.Start(cfg, database, logs, func(id int64, msg string) {
			_ = b.Notify(id, msg)
		}); err != nil {
			log.Fatalf("server: %v", err)
		}
	}()

	b.Start()
}
