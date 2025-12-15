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

	app "github.com/SvenKethz/blv/internal/configuration"
	"github.com/SvenKethz/blv/internal/db"
	"github.com/SvenKethz/blv/internal/helpers"
)

func ImportConf(database *sql.DB, r io.Reader, poolName string, status string) (int, error) {
	scanner := bufio.NewScanner(r)
	imported := 0

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
		if err := db.InsertPool(database, cidr, poolName, comment, status); err != nil {
			return imported, fmt.Errorf("Fehler beim Import von %s: %w", cidr, err)
		}
		imported++
	}

	if err := scanner.Err(); err != nil {
		return imported, err
	}
	return imported, nil
}

type PoolEntry struct {
	CIDR    string
	Comment string
}

func GetStatusCount(entries []db.Pool) (wCount int, bCount int) {
	for _, e := range entries {
		if e.Status == "w" {
			wCount++
		} else if e.Status == "b" {
			bCount++
		}
	}
	return wCount, bCount
}

func ExportConf(database *sql.DB, poolName string) (wExported int, bExported int, err error) {
	entries, _ := db.ListByPool(database, poolName)
	wCount, bCount := GetStatusCount(entries)
	wExported = 0
	bExported = 0

	if wCount > 0 {
		// Datei anlegen/überschreiben
		whitelistFile, err := os.Create(app.Config.OutputPath + "whitelists/" + poolName + ".conf")
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
		// Datei anlegen/überschreiben
		blocklistFile, err := os.Create(app.Config.OutputPath + "blocklists/" + poolName + ".conf")
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

func ResetDB(database *sql.DB) error {
	ExportDB(database)
	err := db.CleanDB(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Putzen der Datenbank: %v", err))
	}
	err = db.CreateTables(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Anlegen der Datenbank: %v", err))
	}
	err = LoadApacheLists(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Laden der ApacheBlocklisten %v", err))
	}
	return err
}

func ExportDB(database *sql.DB) {
	pools, err := db.ListPoolNames(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der Pools: %v", err))
	}
	for _, pool := range pools {
		outputFilePath := app.Config.OutputPath + pool + ".conf"
		wCount, bCount, err := ExportConf(database, pool)
		count := wCount + bCount
		if err != nil {
			app.LogIt.Error(fmt.Sprintf("Fehler beim Export des Pools %s: %v", pool, err))
		} else {
			app.LogIt.Info(fmt.Sprintf("%d", count) + " items from " + pool + " exported to " + outputFilePath)
		}
	}
}

func LoadApacheLists(database *sql.DB) error {
	app.LogIt.Debug("LoadApacheLists")

	entries, err := os.ReadDir(app.Config.BlocklistPath)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der ApacheBlocklisten: %v", err))
	}

	for _, conf := range entries {
		if filepath.Ext(conf.Name()) == ".conf" {
			app.LogIt.Debug("found " + conf.Name() + " in " + app.Config.BlocklistPath)
			file, err := os.Open(app.Config.BlocklistPath + conf.Name())
			if err != nil {
				app.LogIt.Error(fmt.Sprintf("Fehler beim Öffnen von %s: %v", conf.Name(), err))
			} else {
				poolName := strings.TrimSuffix(conf.Name(), filepath.Ext(conf.Name()))
				ImportConf(database, file, poolName, "b")
			}

		}
	}
	entries, err = os.ReadDir(app.Config.WhitelistPath)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der ApacheWhitelisten: %v", err))
	}

	for _, conf := range entries {
		if filepath.Ext(conf.Name()) == ".conf" {
			app.LogIt.Debug("found " + conf.Name() + " in " + app.Config.WhitelistPath)
			file, err := os.Open(app.Config.WhitelistPath + conf.Name())
			if err != nil {
				app.LogIt.Error(fmt.Sprintf("Fehler beim Öffnen von %s: %v", conf.Name(), err))
			} else {
				poolName := strings.TrimSuffix(conf.Name(), filepath.Ext(conf.Name()))
				ImportConf(database, file, poolName, "w")
			}

		}
	}

	return err
}

func LoadLuts(database *sql.DB, folder string) error {
	app.LogIt.Debug("importing csv files from ", folder, " into db.lut")

	csvFiles, err := os.ReadDir(folder)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der csv files: %v", err))
	}

	for _, ipList := range csvFiles {
		if filepath.Ext(ipList.Name()) == ".txt" {
			app.LogIt.Debug("found " + ipList.Name() + " in " + folder)
			file, err := os.Open(folder + ipList.Name())
			if err != nil {
				app.LogIt.Error(fmt.Sprintf("Fehler beim Öffnen von %s: %v", ipList.Name(), err))
			} else {
				_, err = ImportLut(database, file)
			}
			defer file.Close()
		}
	}
	return err
}

func ImportLut(database *sql.DB, file io.Reader) (int, error) {
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
				app.LogIt.Info(fmt.Sprintf("%d Zeilen von %s geladen", lineNumber, file))
			}
			if lineNumber%10000 == 0 {
				fmt.Printf("%d Zeilen von %s geladen\n", lineNumber, file)
			}
		}
	}

	fmt.Printf("Verarbeitet: %d Zeilen\n", lineNumber)
	return lineNumber, nil
}
