package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/SvenKethz/blv/internal/db"
	"github.com/SvenKethz/blv/internal/utils"
	"github.com/SvenKethz/blv/internal/webserver"
)

var (
	_, ApplicationName = utils.SeparateFileFromPath(os.Args[0])
	configPath         = flag.String("c", "/etc/blv/conf.d/blv.yml", "use -c to provide a custom path to the config file")
	config             utils.ApplicationConfig
	LogIt              *slog.Logger
	reset              = flag.Bool("init", false, "Neuaufbau der Datenbank erzwingen")
)

func main() {
	flag.Parse()

	fmt.Println("starting ", ApplicationName)
	config.Initialize(configPath)
	// now setup logging
	LogIt = utils.SetupLogging(config.Logcfg, ApplicationName)
	fmt.Println("LogLevel is set to " + config.Logcfg.LogLevel)
	fmt.Println("will log to", config.Logcfg.LogFolder)

	if *reset {
		_ = os.Remove(config.DbPath)
	}

	database, err := db.Open(config.DbPath)
	if err != nil {
		log.Fatalf("Fehler beim Öffnen der Datenbank: %v", err)
	}
	defer database.Close()

	if err := db.CreateTables(database); err != nil {
		log.Fatalf("Fehler beim Erstellen der Tabellen: %v", err)
	}

	r := webserver.NewRouter(database)
	addr := fmt.Sprintf(":%d", config.WebPort)
	log.Printf("Starte Webserver auf %s ...", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("Fehler beim Starten des Servers: %v", err)
	}
}
