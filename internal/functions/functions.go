package functions

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	app "github.com/SvenKethz/blv/internal/configuration"
	"github.com/SvenKethz/blv/internal/db"
)

func ImportConf(database *sql.DB, r io.Reader, poolName string, status string) (int, error) {
	scanner := bufio.NewScanner(r)
	imported := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "Require") {
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

		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		cidr := parts[3]
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

func ExportConf(database *sql.DB, poolName string) (int, error) {
	// Einträge aus der DB lesen
	w_rows, err := database.Query(`
        SELECT cidr, comment
        FROM pools
        WHERE name = ?
   	    AND status = "w"
        ORDER BY cidr`, poolName)
	if err != nil {
		return 0, fmt.Errorf("konnte Pool-Einträge nicht lesen: %w", err)
	}
	defer w_rows.Close()

	b_rows, err := database.Query(`
        SELECT cidr, comment
        FROM pools
        WHERE name = ?
   	    AND status = "b"
        ORDER BY cidr`, poolName)
	if err != nil {
		return 0, fmt.Errorf("konnte Pool-Einträge nicht lesen: %w", err)
	}
	defer b_rows.Close()

	// Datei anlegen/überschreiben
	wf, err := os.Create(app.Config.OutputPath + "whitelists/" + poolName + ".conf")
	if err != nil {
		return 0, fmt.Errorf("konnte Datei nicht erstellen: %w", err)
	}
	defer wf.Close()
	bf, err := os.Create(app.Config.OutputPath + "blocklists/" + poolName + ".conf")
	if err != nil {
		return 0, fmt.Errorf("konnte Datei nicht erstellen: %w", err)
	}
	defer bf.Close()

	w := bufio.NewWriter(wf)
	defer w.Flush()

	// Header schreiben
	fmt.Fprintln(w, "#----------------------------------------")
	fmt.Fprintln(w, "# WHITELIST"+poolName)
	fmt.Fprintln(w, "#----------------------------------------")

	exported := 0
	for w_rows.Next() {
		var e PoolEntry
		if err := w_rows.Scan(&e.CIDR, &e.Comment); err != nil {
			return exported, fmt.Errorf("Fehler beim Lesen der DB-Daten: %w", err)
		}

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
		exported++
	}

	if err := w_rows.Err(); err != nil {
		return exported, fmt.Errorf("Fehler beim Durchlaufen der DB-Ergebnisse: %w", err)
	}
	w = bufio.NewWriter(bf)
	defer w.Flush()

	// Header schreiben
	fmt.Fprintln(w, "#----------------------------------------")
	fmt.Fprintln(w, "# BLOCKLIST"+poolName)
	fmt.Fprintln(w, "#----------------------------------------")

	for b_rows.Next() {
		var e PoolEntry
		if err := b_rows.Scan(&e.CIDR, &e.Comment); err != nil {
			return exported, fmt.Errorf("Fehler beim Lesen der DB-Daten: %w", err)
		}

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
		exported++
	}

	if err := b_rows.Err(); err != nil {
		return exported, fmt.Errorf("Fehler beim Durchlaufen der DB-Ergebnisse: %w", err)
	}

	return exported, nil
}

func ResetDB(database *sql.DB) error {
	ExportDB(database)
	err := db.CleanDB(database)
	if err != nil {
		app.LogIt.Error("Fehler beim Putzen der Datenbank: %v", err)
	}
	err = db.CreateTables(database)
	if err != nil {
		app.LogIt.Error("Fehler beim Anlegen der Datenbank: %v", err)
	}
	err = LoadApacheLists(database)
	if err != nil {
		app.LogIt.Error("Fehler beim Laden der ApacheBlocklisten %v", err)
	}
	return err
}

func ExportDB(database *sql.DB) {
	pools, err := db.ListPoolNames(database)
	if err != nil {
		app.LogIt.Error("Fehler beim Lesen der Pools: %v", err)
	}
	for _, pool := range pools {
		outputFilePath := app.Config.OutputPath + pool + ".conf"
		count, err := ExportConf(database, pool)
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
