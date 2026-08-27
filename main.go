package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	app "github.com/SvenKethz/fairdb/internal/configuration"
	"github.com/SvenKethz/fairdb/internal/db"
	"github.com/SvenKethz/fairdb/internal/functions"
	"github.com/SvenKethz/fairdb/internal/helpers"
	"github.com/SvenKethz/fairdb/internal/webserver"
)

var (
	_, ApplicationName = helpers.SeparateFileFromPath(os.Args[0])
	ConfigPath         = flag.String("c", "/etc/fairdb/conf.d/fairdb.yml", "use -c to provide a custom path to the config file")
	DBinit             = flag.Bool("init", false, "Neuaufbau der Datenbank erzwingen")
)

func main() {
	flag.Parse()

	fmt.Println("starting ", ApplicationName)
	app.Initialize(ApplicationName, *ConfigPath)
	fmt.Println("LogLevel is set to " + app.Config.Logcfg.LogLevel)
	fmt.Println("will log to", app.Config.Logcfg.LogFolder)

	app.LogIt.Info(ApplicationName + " starting")
	app.LogIt.Debug("folgende Werte wurden gesetzt")
	app.LogIt.Debug("DbPath:         " + app.Config.DbPath)
	app.LogIt.Debug("WhitelistPath:  " + app.Config.WhitelistPath)
	app.LogIt.Debug("BlocklistPath:  " + app.Config.BlocklistPath)
	app.LogIt.Debug("BackupPath:     " + app.Config.BackupPath)
	app.LogIt.Debug("WebfilesPath:   " + app.Config.WebfilesPath)
	app.LogIt.Debug("BasePath:       " + app.Config.BasePath)
	app.LogIt.Debug(fmt.Sprintf("WebPort:        %v", app.Config.WebPort))
	app.LogIt.Debug(fmt.Sprintf("TrustedProxies: %v", app.Config.TrustedProxies))
	app.LogIt.Debug("LogLevel:       " + app.Config.Logcfg.LogLevel)
	app.LogIt.Debug("LogFolder:      " + app.Config.Logcfg.LogFolder)

	if *DBinit {
		app.LogIt.Info("Die DB wird initialisiert.")
		if helpers.FileExists(app.Config.DbPath) {
			os.Remove(app.Config.DbPath)
		}
		database, err := db.Open(app.Config.DbPath)
		if err != nil {
			log.Fatalf("Fehler beim Öffnen der Datenbank: %v", err)
		}
		defer database.Close()
		if err := functions.InitDB(database); err != nil {
			log.Fatalf("Fehler beim Initialisieren der Datenbank: %v", err)
		}
		app.LogIt.Info("Die DB wurde initialisiert - nun kann das System gestartet werden.")
		fmt.Println("Die DB wurde initialisiert - nun kann das System gestartet werden.")
	} else {
		database, err := db.Open(app.Config.DbPath)
		if err != nil {
			log.Fatalf("Fehler beim Öffnen der Datenbank: %v", err)
		}
		defer database.Close()

		// Schema idempotent auf dem neuesten Stand halten (z. B. group_name/
		// groups aus einer älteren DB nachziehen), ohne die Daten anzutasten.
		// CreateTables nutzt CREATE TABLE IF NOT EXISTS bzw. prüft fehlende
		// Spalten vor einem ALTER TABLE, ist also auch bei jedem normalen
		// Start gefahrlos aufrufbar.
		if err := db.CreateTables(database); err != nil {
			log.Fatalf("Fehler bei der Schema-Migration: %v", err)
		}

		app.LogIt.Info("Gleiche DB-Status mit den vorhandenen Apache-Konfigurationsdateien ab ...")
		if err := functions.SyncDBWithApacheState(database, app.Config.WhitelistPath, app.Config.BlocklistPath); err != nil {
			log.Fatalf("Fehler beim Abgleich mit der Apache-Konfiguration: %v", err)
		}

		r := webserver.NewRouter(database, app.Config.BasePath)
		addr := fmt.Sprintf(":%d", app.Config.WebPort)
		log.Printf("Starte Webserver auf %s ...", addr)
		if err := r.Run(addr); err != nil {
			log.Fatalf("Fehler beim Starten des Servers: %v", err)
		}
	}
}
