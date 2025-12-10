package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	app "github.com/SvenKethz/blv/internal/configuration"
	"github.com/SvenKethz/blv/internal/db"
	"github.com/SvenKethz/blv/internal/functions"
	"github.com/SvenKethz/blv/internal/helpers"
	"github.com/SvenKethz/blv/internal/webserver"
)

var (
	_, ApplicationName = helpers.SeparateFileFromPath(os.Args[0])
	ConfigPath         = flag.String("c", "/etc/blv/conf.d/blv.yml", "use -c to provide a custom path to the config file")
	DBinit             = flag.Bool("init", false, "Neuaufbau der Datenbank erzwingen")
	Reset              = flag.Bool("reset", false, "Neuaufbau der Datenbank erzwingen")
)

func main() {
	flag.Parse()

	fmt.Println("starting ", ApplicationName)
	app.Initialize(ApplicationName, *ConfigPath)
	// now setup logging
	fmt.Println("LogLevel is set to " + app.Config.Logcfg.LogLevel)
	fmt.Println("will log to", app.Config.Logcfg.LogFolder)

	app.LogIt.Info(ApplicationName + " starting")
	database, err := db.Open(app.Config.DbPath)
	if err != nil {
		log.Fatalf("Fehler beim Öffnen der Datenbank: %v", err)
	}
	defer database.Close()

	if *Reset {
		app.LogIt.Debug("Reset am Laufen")
		functions.ExportDB(database)
		if err := db.CleanDB(database); err != nil {
			log.Fatalf("Fehler beim Putzen der Datenbank: %v", err)
		}
		if err := db.CreateTables(database); err != nil {
			log.Fatalf("Fehler beim Erstellen der Tabellen: %v", err)
		}
		if err := db.LoadApacheLists(database); err != nil {
			log.Fatalf("Fehler beim Laden der aktuellen Apache-Blocklisten: %v", err)
		}
	}

	r := webserver.NewRouter(database)
	addr := fmt.Sprintf(":%d", app.Config.WebPort)
	log.Printf("Starte Webserver auf %s ...", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("Fehler beim Starten des Servers: %v", err)
	}
}
