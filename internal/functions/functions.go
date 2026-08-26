package functions

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	app "github.com/SvenKethz/fairdb/internal/configuration"
	"github.com/SvenKethz/fairdb/internal/db"
	"github.com/SvenKethz/fairdb/internal/helpers"
)

// ImportConf liest eine Apache-Konfigurationsdatei bzw. IP-Liste (z. B. ein
// Upload im Admin-Bereich) und legt die Einträge als neuen bzw. ergänzten
// Pool an. Für Fremd-Listen gedacht (z. B. eine heruntergeladene Blockliste) -
// fairDB liest niemals seine eigenen Exporte zurück, die DB ist alleinige
// Source of Truth.
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

// writeGroupConfFile schreibt alle Einträge mit passendem Status in eine
// Datei, gegliedert in einen Abschnitt pro Pool (Header wie von ExportGroupConf
// vorgegeben). entries muss nach Pool-Name sortiert sein (siehe db.ListByGroup).
func writeGroupConfFile(path, headerLabel, requireKeyword string, entries []db.PoolEntry, status string) (int, error) {
	file, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	w := bufio.NewWriter(file)
	defer w.Flush()

	count := 0
	currentPool := ""
	sectionOpen := false
	for _, e := range entries {
		if e.Status != status {
			continue
		}
		if !sectionOpen || e.Name != currentPool {
			fmt.Fprintln(w, "#----")
			fmt.Fprintln(w, "# "+headerLabel+" "+e.Name)
			fmt.Fprintln(w, "#--------------------------------------------------------------------------------")
			currentPool = e.Name
			sectionOpen = true
		}
		comment := strings.TrimSpace(e.Comment)
		if comment != "" {
			fmt.Fprintf(w, "%s  %s # %s\n", requireKeyword, e.CIDR, comment)
		} else {
			fmt.Fprintf(w, "%s  %s\n", requireKeyword, e.CIDR)
		}
		count++
	}
	return count, nil
}

// ExportGroupConf schreibt die Einträge einer Gruppe nach
// whitelistPath/<groupName>.conf bzw. blocklistPath/<groupName>.conf,
// gegliedert nach Pool. Gibt es in einer Kategorie (w/b) keine Einträge mehr,
// wird eine zuvor exportierte Datei entfernt statt veraltet stehen zu bleiben
// (z. B. wenn eine Gruppe vollständig von blocked auf whitelisted wechselt).
func ExportGroupConf(database *sql.DB, groupName, whitelistPath, blocklistPath string) (wExported int, bExported int, err error) {
	entries, err := db.ListByGroup(database, groupName)
	if err != nil {
		return 0, 0, err
	}
	wCount, bCount := GetStatusCount(entries)

	whitelistFile := whitelistPath + groupName + ".conf"
	if wCount > 0 {
		n, err := writeGroupConfFile(whitelistFile, "WHITELIST", "Require ip", entries, "w")
		if err != nil {
			return 0, 0, fmt.Errorf("konnte Whitelist-Datei nicht erstellen: %w", err)
		}
		wExported = n
	} else if err := removeIfExists(whitelistFile); err != nil {
		return 0, 0, fmt.Errorf("konnte veraltete Whitelist-Datei nicht entfernen: %w", err)
	}

	blocklistFile := blocklistPath + groupName + ".conf"
	if bCount > 0 {
		n, err := writeGroupConfFile(blocklistFile, "BLOCKLIST", "Require not ip", entries, "b")
		if err != nil {
			return 0, 0, fmt.Errorf("konnte Blocklist-Datei nicht erstellen: %w", err)
		}
		bExported = n
	} else if err := removeIfExists(blocklistFile); err != nil {
		return 0, 0, fmt.Errorf("konnte veraltete Blocklist-Datei nicht entfernen: %w", err)
	}
	return wExported, bExported, nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ExportAllGroups exportiert alle bekannten Gruppen (siehe ExportGroupConf).
func ExportAllGroups(database *sql.DB, whitelistPath, blocklistPath string) error {
	groups, err := db.ListGroups(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Lesen der Gruppen: %v", err))
		return err
	}
	for _, group := range groups {
		wCount, bCount, err := ExportGroupConf(database, group.Name, whitelistPath, blocklistPath)
		if err != nil {
			app.LogIt.Error(fmt.Sprintf("Fehler beim Export der Gruppe %s: %v", group.Name, err))
			return err
		}
		app.LogIt.Info(fmt.Sprintf("%d items from group %s exported", wCount+bCount, group.Name))
	}
	return nil
}

// ReloadApache führt den konfigurierten Reload-Befehl aus (Default: sudo
// systemctl reload apache2) und protokolliert Erfolg bzw. Fehler. Der
// Service-User benötigt dafür ein entsprechendes NOPASSWD-sudoers-Recht,
// siehe README.
func ReloadApache() error {
	cmdArgs := app.Config.ApacheReloadCommand
	if len(cmdArgs) == 0 {
		err := fmt.Errorf("kein ApacheReloadCommand konfiguriert")
		app.LogIt.Error(err.Error())
		return err
	}
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Apache-Reload fehlgeschlagen (%s): %v - %s", strings.Join(cmdArgs, " "), err, strings.TrimSpace(string(output))))
		return fmt.Errorf("Apache-Reload fehlgeschlagen: %w", err)
	}
	app.LogIt.Info("Apache erfolgreich neu geladen (" + strings.Join(cmdArgs, " ") + ")")
	return nil
}

// ActivateGroup exportiert eine Gruppe in die Live-Pfade (app.Config.WhitelistPath/
// BlocklistPath) und lädt Apache neu. exportErr und reloadErr werden getrennt
// zurückgegeben, da bereits geschriebene Dateien bei einem fehlgeschlagenen
// Reload gültig bleiben (der Admin/API-Aufrufer kann den Reload manuell
// nachholen).
func ActivateGroup(database *sql.DB, groupName string) (wExported int, bExported int, exportErr error, reloadErr error) {
	wExported, bExported, exportErr = ExportGroupConf(database, groupName, app.Config.WhitelistPath, app.Config.BlocklistPath)
	if exportErr != nil {
		return wExported, bExported, exportErr, nil
	}
	reloadErr = ReloadApache()
	return wExported, bExported, nil, reloadErr
}

// WhitelistGroup whitelisted alle Pools einer Gruppe (siehe db.WhitelistPool,
// inkl. Konfliktprüfung gegen anderswo geblockte Einträge), setzt den
// Gruppenstatus und aktiviert die Gruppe sofort (Export + Apache-Reload).
// Werden Konflikte gefunden, bleibt die Gruppe unverändert.
func WhitelistGroup(database *sql.DB, groupName string) (conflicts []db.PoolEntry, wExported int, bExported int, err error, reloadErr error) {
	poolNames, err := db.ListPoolNamesInGroup(database, groupName)
	if err != nil {
		return nil, 0, 0, err, nil
	}
	for _, poolName := range poolNames {
		found, err := db.WhitelistPool(database, poolName)
		if err != nil {
			return nil, 0, 0, err, nil
		}
		conflicts = append(conflicts, found...)
	}
	if conflicts != nil {
		return conflicts, 0, 0, nil, nil
	}
	if err := db.SetGroupStatus(database, groupName, "w"); err != nil {
		return nil, 0, 0, err, nil
	}
	wExported, bExported, err, reloadErr = ActivateGroup(database, groupName)
	return nil, wExported, bExported, err, reloadErr
}

// BlockGroup blockt alle Pools einer Gruppe, setzt den Gruppenstatus und
// aktiviert die Gruppe sofort (Export + Apache-Reload).
func BlockGroup(database *sql.DB, groupName string) (wExported int, bExported int, err error, reloadErr error) {
	poolNames, err := db.ListPoolNamesInGroup(database, groupName)
	if err != nil {
		return 0, 0, err, nil
	}
	for _, poolName := range poolNames {
		if err := db.BlockPool(database, poolName); err != nil {
			return 0, 0, err, nil
		}
	}
	if err := db.SetGroupStatus(database, groupName, "b"); err != nil {
		return 0, 0, err, nil
	}
	return ActivateGroup(database, groupName)
}

// DeactivateGroup setzt den Status aller Pools einer Gruppe zurück auf
// inaktiv (weder whitelisted noch blocked) und aktiviert die Gruppe sofort
// (Export + Apache-Reload) - die Gruppe verschwindet dadurch sowohl aus der
// Whitelist- als auch der Blocklist-Datei.
func DeactivateGroup(database *sql.DB, groupName string) (wExported int, bExported int, err error, reloadErr error) {
	poolNames, err := db.ListPoolNamesInGroup(database, groupName)
	if err != nil {
		return 0, 0, err, nil
	}
	for _, poolName := range poolNames {
		if err := db.DeactivatePool(database, poolName); err != nil {
			return 0, 0, err, nil
		}
	}
	if err := db.SetGroupStatus(database, groupName, ""); err != nil {
		return 0, 0, err, nil
	}
	return ActivateGroup(database, groupName)
}

func InitDB(database *sql.DB) error {
	err := db.CreateTables(database)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Anlegen der Datenbank: %v", err))
	}
	return err
}

// ExportDB2Conf sichert den aktuellen Inhalt von WhitelistPath/BlocklistPath
// je als datiertes .tgz-Archiv nach BackupPath, exportiert danach den
// kompletten aktuellen DB-Stand direkt in die Live-Verzeichnisse
// (WhitelistPath/BlocklistPath) und lädt Apache neu.
func ExportDB2Conf(database *sql.DB) error {
	today := time.Now().Format("2006-01-02")

	if err := helpers.TarGzDir(app.Config.WhitelistPath, app.Config.BackupPath+"whitelists-"+today+".tgz"); err != nil {
		app.LogIt.Error("Keine Dateien exportiert, weil kein Backup erstellt werden konnte: " + err.Error())
		return err
	}
	if err := helpers.TarGzDir(app.Config.BlocklistPath, app.Config.BackupPath+"blocklists-"+today+".tgz"); err != nil {
		app.LogIt.Error("Keine Dateien exportiert, weil kein Backup erstellt werden konnte: " + err.Error())
		return err
	}
	if err := ExportAllGroups(database, app.Config.WhitelistPath, app.Config.BlocklistPath); err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Export der Datenbank: %v", err))
		return err
	}
	fmt.Println("Konfigurationen aus der DB in die listen geschrieben.")
	app.LogIt.Info("Konfigurationen aus der DB in die listen geschrieben.")
	if err := ReloadApache(); err != nil {
		fmt.Println("Konfiguration exportiert, aber Apache-Reload fehlgeschlagen:", err)
		return err
	}
	fmt.Println("Apache wurde neu geladen.")
	return nil
}
