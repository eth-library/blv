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
	LutFolder          = flag.String("lf", "./luts", "IP-Host-Listen laden")
	Reset              = flag.Bool("reset", false, "Neuaufbau der Datenbank erzwingen")
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
	app.LogIt.Debug("BlocklistPath:  " + app.Config.BlocklistPath)
	app.LogIt.Debug("OutputPath:     " + app.Config.OutputPath)
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

		if helpers.FlagIsPassed("lf") {
			fmt.Println("werde die Lut-Einträge von %s laden", *LutFolder)
			app.LogIt.Info("werde die Lut-Einträge von %s laden", *LutFolder)
			helpers.Checknaddtrailingslash(LutFolder)
			if err := functions.LoadLuts(database, *LutFolder); err != nil {
				log.Fatalf("Fehler beim Laden der Luts von %s: %s", *LutFolder, err)
			}
		}
		if *Reset {
			app.LogIt.Info("Die DB wird nun zurückgesetzt und die Apache-Listen neu geladen - was kann etwas dauern.")
			fmt.Println("Die DB wird nun zurückgesetzt und die Apache-Listen neu geladen - was kann etwas dauern.")
			functions.ResetDB(database)
			app.LogIt.Info("Die DB wurde zurückgesetzt und die Apache-Listen neu geladen.")
			fmt.Println("Die DB wurde zurückgesetzt und die Apache-Listen neu geladen.")
		} else {
			r := webserver.NewRouter(database, app.Config.BasePath)
			addr := fmt.Sprintf(":%d", app.Config.WebPort)
			log.Printf("Starte Webserver auf %s ...", addr)
			if err := r.Run(addr); err != nil {
				log.Fatalf("Fehler beim Starten des Servers: %v", err)
			}
		}
	}
}
