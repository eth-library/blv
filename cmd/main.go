package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/SvenKethz/blv/internal/db"
	"github.com/SvenKethz/blv/internal/web"
)

var (
	_, ApplicationName = SeparateFileFromPath(os.Args[0])
	configPath         = flag.String("c", "/etc/blv/conf.d/blv.yml", "use -c to provide a custom path to the config file")
	config             ApplicationConfig
	LogIt              *slog.Logger
	reset              = flag.Bool("init", false, "Neuaufbau der Datenbank erzwingen")
)

func main() {
	flag.Parse()

	if *reset {
		_ = os.Remove(*dbPath)
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("Fehler beim Öffnen der Datenbank: %v", err)
	}
	defer database.Close()

	if err := db.CreateTables(database); err != nil {
		log.Fatalf("Fehler beim Erstellen der Tabellen: %v", err)
	}

	r := web.NewRouter(database)
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Starte Webserver auf %s ...", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("Fehler beim Starten des Servers: %v", err)
	}
}

