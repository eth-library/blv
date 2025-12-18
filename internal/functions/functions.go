package functions

import (
	"bufio"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	app "github.com/SvenKethz/blv/internal/configuration"
	"github.com/SvenKethz/blv/internal/db"
	"github.com/SvenKethz/blv/internal/helpers"
)

func ImportConf(database *sql.DB, r io.Reader, poolName string, status string) error {
	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "Require") && !helpers.StartsWithIP(line) {
			continue
		}

		// Kommentar abtrennen
		var comment string
		if idx := strings.Index(line, "#"); idx != -1 {
			comment = strings.TrimSpace(line[idx+1:])
			if len(comment) > 60 {
				comment = comment[:60]
			}
			line = strings.TrimSpace(line[:idx])
		}

		var cidr string
		for part := range strings.FieldsSeq(line) {
			if helpers.StartsWithIP(part) {
				cidr = part
			}
		}
		err := db.InsertPoollistEntry(database, cidr, poolName, comment, status)
		if err != nil {
			return fmt.Errorf("Fehler beim Import von %s: %w", cidr, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func GetStatusCount(entries []db.PoolEntry) (wCount int, bCount int) {
	for _, e := range entries {
		switch e.Status {
		case "w":
			wCount++
		case "b":
			bCount++
		}
	}
	return wCount, bCount
}

func ExportConf(database *sql.DB, poolName, outputPath string) (wExported int, bExported int, err error) {
	entries, _ := db.ListByPool(database, poolName)
	wCount, bCount := GetStatusCount(entries)
	wExported = 0
	bExported = 0

	if wCount > 0 {
		whitelistFile, err := os.Create(outputPath + "whitelists/" + poolName + ".conf")
		if err != nil {
			return 0, 0, fmt.Errorf("konnte Datei nicht erstellen: %w", err)
		}
		defer whitelistFile.Close()

		w := bufio.NewWriter(whitelistFile)
		defer w.Flush()

		// Header schreiben
		fmt.Fprintln(w, "#----------------------------------------")
		fmt.Fprintln(w, "# WHITELIST "+poolName)
		fmt.Fprintln(w, "#----------------------------------------")

		for _, e := range entries {
			if e.Status == "w" {
				// Kommentar ggf. beschneiden (symmetrisch zu Import)
				comment := strings.TrimSpace(e.Comment)
				if len(comment) > 0 {
					if len(comment) > 60 {
						comment = comment[:60]
					}
					fmt.Fprintf(w, "Require ip %s # %s\n", e.CIDR, comment)
				} else {
					fmt.Fprintf(w, "Require ip %s\n", e.CIDR)
				}
				wExported++
			}
		}
	}
	if bCount > 0 {
		blocklistFile, err := os.Create(outputPath + "blocklists/" + poolName + ".conf")
		if err != nil {
			return 0, 0, fmt.Errorf("konnte Datei nicht erstellen: %w", err)
		}
		defer blocklistFile.Close()

		w := bufio.NewWriter(blocklistFile)
		defer w.Flush()

		// Header schreiben
		fmt.Fprintln(w, "#----------------------------------------")
		fmt.Fprintln(w, "# BLOCKLIST "+poolName)
		fmt.Fprintln(w, "#----------------------------------------")

		for _, e := range entries {
			if e.Status == "b" {
				// Kommentar ggf. beschneiden (symmetrisch zu Import)
				comment := strings.TrimSpace(e.Comment)
				if len(comment) > 0 {
					if len(comment) > 60 {
						comment = comment[:60]
					}
					fmt.Fprintf(w, "Require not ip %s # %s\n", e.CIDR, comment)
				} else {
					fmt.Fprintf(w, "Require not ip %s\n", e.CIDR)
				}
				bExported++
			}
		}
	}

	return wExported, bExported, nil
}

func InitDB(database *sql.DB) error {
	err := db.CreateTables(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Anlegen der Datenbank: %v", err))
	}
	return err
}

func ExportDB2Conf(database *sql.DB) error {
	today := time.Now().Format("2006-01-02")

	// aktuelle WhiteListen sichern:
	err := helpers.BackupFiles(app.Config.ListPath+"whitelists/", ".conf", app.Config.ListPath+"whitelists/"+today)
	if err != nil {
		app.LogIt.Error("Keine Dateien Exportiert, weil kein Backup erstellt werden konnte")
		return err
	}
	// aktuelle BlockListen sichern:
	err = helpers.BackupFiles(app.Config.ListPath+"blocklists/", ".conf", app.Config.ListPath+"blocklists/"+today)
	if err != nil {
		app.LogIt.Error("Keine Dateien Exportiert, weil kein Backup erstellt werden konnte")
		return err
	}
	err = ExportDB(database, app.Config.ListPath)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Export der Datenbank: %v", err))
		return err
	}
	fmt.Println("Konfigurationen aus der DB in die listen geschrieben.")
	app.LogIt.Info("Konfigurationen aus der DB in die listen geschrieben.")
	fmt.Println("der Webserver muss neu geladen werden (systemctl reload apache2)")
	return nil
}

func ResetDB(database *sql.DB) error {
	err := ExportDB(database, app.Config.BackupPath)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Putzen der Datenbank: %v", err))
		return err
	}
	err = db.CleanDB(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Putzen der Datenbank: %v", err))
		return err
	}
	err = db.CreateTables(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Anlegen der Datenbank: %v", err))
		return err
	}
	err = LoadApacheLists(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Laden der ApacheBlocklisten %v", err))
	}
	return err
}

func ExportDB(database *sql.DB, outputPath string) error {
	pools, err := db.ListPoolNames(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der Pools: %v", err))
		return err
	}
	for _, pool := range pools {
		wCount, bCount, err := ExportConf(database, pool, outputPath)
		count := wCount + bCount
		if err != nil {
			app.LogIt.Error(fmt.Sprintf("Fehler beim Export des Pools %s: %v", pool, err))
			return err
		} else {
			app.LogIt.Info(fmt.Sprintf("%d", count) + " items from " + pool + " exported to " + outputPath)
		}
	}
	return nil
}

func LoadApacheLists(database *sql.DB) error {
	app.LogIt.Debug("LoadApacheLists")

	entries, err := os.ReadDir(app.Config.ListPath + "blocklists/")
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der ApacheBlocklisten: %v", err))
	}
	err = LoadConfigs(database, entries, app.Config.ListPath+"blocklists/")
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der ApacheBlocklisten: %v", err))
	}

	entries, err = os.ReadDir(app.Config.ListPath + "whitelists/")
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der ApacheWhitelisten: %v", err))
	}
	err = LoadConfigs(database, entries, app.Config.ListPath+"whitelists/")
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der ApacheWhitelisten: %v", err))
	}
	return err
}

func LoadConfigs(database *sql.DB, entries []os.DirEntry, filesPath string) error {
	for _, conf := range entries {
		if filepath.Ext(conf.Name()) == ".conf" {
			app.LogIt.Debug("found " + conf.Name() + " in " + filesPath)
			file, err := os.Open(filesPath + conf.Name())
			if err != nil {
				app.LogIt.Error(fmt.Sprintf("Fehler beim Öffnen von %s: %v", conf.Name(), err))
				return err
			} else {
				app.LogIt.Info("lade " + conf.Name())
				fmt.Println("lade", conf.Name())
				poolName := strings.TrimSuffix(conf.Name(), filepath.Ext(conf.Name()))
				ImportConf(database, file, poolName, "w")
			}
		}
	}
	return nil
}

func LoadLuts(database *sql.DB, folder string) error {
	app.LogIt.Info("importiere csv files von ", folder, " in die db.lut")

	csvFiles, err := os.ReadDir(folder)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der csv files: %v", err))
	} else {
		app.LogIt.Info(fmt.Sprintf("%d Dateien in %s gefunden", len(csvFiles), folder))
		fmt.Printf("%d Dateien in %s gefunden\n", len(csvFiles), folder)
	}

	for _, ipList := range csvFiles {
		if filepath.Ext(ipList.Name()) == ".txt" {
			app.LogIt.Info("bearbeite " + ipList.Name() + " in " + folder)
			file, err := os.Open(folder + ipList.Name())
			if err != nil {
				app.LogIt.Error(fmt.Sprintf("Fehler beim Öffnen von %s: %v", ipList.Name(), err))
				fmt.Printf("Fehler beim Öffnen von %s: %v\n", ipList.Name(), err)
			} else {
				_, err = ImportLut(database, file, ipList.Name())
				if err != nil {
					app.LogIt.Error("Fehler beim importieren von %s: %v", ipList.Name(), err)
				}
			}
			defer file.Close()
		} else {
			app.LogIt.Info(ipList.Name() + "ist keine .txt - Datei")
		}
	}
	return err
}

func ImportLut(database *sql.DB, file io.Reader, fileName string) (int, error) {
	reader := csv.NewReader(file)
	// Optional: Konfiguration
	reader.Comma = ','          // Trennzeichen
	reader.Comment = '#'        // Kommentarzeichen
	reader.FieldsPerRecord = -1 // Variable Anzahl Felder erlauben
	reader.TrimLeadingSpace = true

	lineNumber := 0

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break // Ende der Datei erreicht
		}
		if err != nil {
			app.LogIt.Error(fmt.Sprintf("Fehler in Zeile %d: %v", lineNumber, err))
			continue // Überspringe fehlerhafte Zeilen
		}

		lineNumber++

		// Verarbeite die Zeile
		if err := db.InsertLutItem(database, record[0], record[1]); err != nil {
			return lineNumber, fmt.Errorf("Fehler beim Import von Zeile %d: %s,%s :: %w", lineNumber, record[0], record[1], err)
		} else {
			if lineNumber%1000 == 0 {
				app.LogIt.Info(fmt.Sprintf("%d Zeilen von %s geladen", lineNumber, fileName))
			}
			if lineNumber%10000 == 0 {
				fmt.Printf("%d Zeilen von %s geladen\n", lineNumber, fileName)
			}
		}
	}

	fmt.Printf("Verarbeitet: %d Zeilen\n", lineNumber)
	return lineNumber, nil
}
