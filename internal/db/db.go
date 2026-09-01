package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	// _ "github.com/mattn/go-sqlite3"
	_ "modernc.org/sqlite"

	app "github.com/SvenKethz/fairdb/internal/configuration"
	"github.com/SvenKethz/fairdb/internal/helpers"
)

type PoolEntry struct {
	ID         int
	StartIPInt uint32
	EndIPInt   uint32
	CIDR       string
	Name       string
	GroupName  string
	Comment    string
	Status     string
}

type GroupEntry struct {
	ID     int
	Name   string
	Status string
}

func Open(path string) (*sql.DB, error) {
	// modernc.org/sqlite kennt nur "_pragma=..." als DSN-Parameter (das
	// vorherige "_journal_mode=WAL" wurde vom Treiber stillschweigend
	// ignoriert, die DB lief also nie wirklich im WAL-Modus). busy_timeout
	// lässt konkurrierende Schreibzugriffe kurz warten statt sofort mit
	// SQLITE_BUSY zu scheitern.
	return sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path))
}

func CleanDB(database *sql.DB) error {
	app.LogIt.Debug("CleanDB")
	const sqlStmt = `
	   DROP TABLE IF EXISTS pools;
	   DROP INDEX IF EXISTS idx_ip_range;
	   `
	_, err := database.Exec(sqlStmt)
	// err := errors.New("")
	return err
}

func CreateTables(database *sql.DB) error {
	app.LogIt.Debug("CreateTables")
	const sqlStmt = `
	   CREATE TABLE IF NOT EXISTS pools (
	       id INTEGER PRIMARY KEY AUTOINCREMENT,
	       start_ip_int INTEGER NOT NULL,
	       end_ip_int INTEGER NOT NULL,
	       cidr TEXT NOT NULL,
	       name TEXT,
	       comment TEXT,
	       status TEXT
	   );
	   CREATE INDEX IF NOT EXISTS idx_ip_range ON pools (start_ip_int, end_ip_int);
	   CREATE INDEX IF NOT EXISTS idx_pool_name ON pools (name);
	   CREATE INDEX IF NOT EXISTS idx_pool_status_range ON pools (status, start_ip_int, end_ip_int);
	   CREATE TABLE IF NOT EXISTS lut (
	       id INTEGER PRIMARY KEY AUTOINCREMENT,
	       ip_int INTEGER NOT NULL,
	       name TEXT
	   );
	   CREATE INDEX IF NOT EXISTS host_name ON lut (name);
	   CREATE TABLE IF NOT EXISTS groups (
	       id INTEGER PRIMARY KEY AUTOINCREMENT,
	       name TEXT NOT NULL UNIQUE,
	       status TEXT NOT NULL DEFAULT ''
	   );
	   CREATE TABLE IF NOT EXISTS group_autoblock (
	       group_name TEXT PRIMARY KEY,
	       enabled INTEGER NOT NULL DEFAULT 0,
	       mode TEXT NOT NULL DEFAULT 'threshold',
	       threshold_rps REAL NOT NULL DEFAULT 0,
	       scraper_pain_percent INTEGER NOT NULL DEFAULT 0,
	       block_duration_min_seconds INTEGER NOT NULL DEFAULT 0,
	       block_duration_max_seconds INTEGER NOT NULL DEFAULT 0,
	       active INTEGER NOT NULL DEFAULT 0,
	       triggered_until TEXT
	   );
	   CREATE UNIQUE INDEX IF NOT EXISTS idx_group_autoblock_singleton_enabled ON group_autoblock (enabled) WHERE enabled = 1;
	   CREATE TABLE IF NOT EXISTS group_scraperpain_pools (
	       group_name TEXT NOT NULL,
	       pool_name TEXT NOT NULL,
	       PRIMARY KEY (group_name, pool_name)
	   );
	   `
	if _, err := database.Exec(sqlStmt); err != nil {
		return err
	}
	if err := migrateGroupNameColumn(database); err != nil {
		return err
	}
	return migrateGroupAutoblockColumns(database)
}

// migrateGroupAutoblockColumns ergänzt die mode- und
// scraper_pain_percent-Spalten auf bestehenden group_autoblock-Tabellen
// (vor Einführung von "Scraper's Pain", siehe internal/autoblock), ohne
// vorhandene Konfigurationen anzutasten - eine bestehende Zeile bekommt
// dabei automatisch mode='threshold' (Spalten-Default), entspricht also
// genau ihrem bisherigen (einzigen) Verhalten.
func migrateGroupAutoblockColumns(database *sql.DB) error {
	rows, err := database.Query(`PRAGMA table_info(group_autoblock)`)
	if err != nil {
		return err
	}

	hasMode, hasPercent := false, false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			rows.Close()
			return err
		}
		switch name {
		case "mode":
			hasMode = true
		case "scraper_pain_percent":
			hasPercent = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if !hasMode {
		app.LogIt.Info("migriere Schema: füge mode zu group_autoblock hinzu")
		if _, err := database.Exec(`ALTER TABLE group_autoblock ADD COLUMN mode TEXT NOT NULL DEFAULT 'threshold'`); err != nil {
			return err
		}
	}
	if !hasPercent {
		app.LogIt.Info("migriere Schema: füge scraper_pain_percent zu group_autoblock hinzu")
		if _, err := database.Exec(`ALTER TABLE group_autoblock ADD COLUMN scraper_pain_percent INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

// migrateGroupNameColumn ergänzt die group_name-Spalte auf bestehenden
// pools-Tabellen (ältere Datenbanken vor Einführung der Gruppen), ohne
// vorhandene Daten anzutasten.
func migrateGroupNameColumn(database *sql.DB) error {
	rows, err := database.Query(`PRAGMA table_info(pools)`)
	if err != nil {
		return err
	}

	hasGroupName := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "group_name" {
			hasGroupName = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	// Cursor explizit vor den folgenden Schreibzugriffen schließen (nicht
	// erst per defer beim Funktionsende) - ein offen gehaltener Lese-Cursor
	// auf einer anderen gepoolten Verbindung kann sonst mit dem ALTER TABLE/
	// den Backfill-Schreibzugriffen kollidieren (SQLITE_BUSY).
	rows.Close()
	if hasGroupName {
		_, err := database.Exec(`CREATE INDEX IF NOT EXISTS idx_pool_group_name ON pools (group_name)`)
		return err
	}
	app.LogIt.Info("migriere Schema: füge group_name zu pools hinzu")
	if _, err := database.Exec(`ALTER TABLE pools ADD COLUMN group_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := database.Exec(`CREATE INDEX IF NOT EXISTS idx_pool_group_name ON pools (group_name)`); err != nil {
		return err
	}
	return backfillGroupsFromExistingPools(database)
}

// backfillGroupsFromExistingPools weist jedem bereits vorhandenen Pool (vor
// Einführung der Gruppen) eine gleichnamige Gruppe zu - genau wie beim Anlegen
// eines neuen Pools (siehe resolvePoolGroup). Ohne dieses Backfill wären
// bestehende Produktivdaten nach der Migration keiner Gruppe zugeordnet und
// über den gruppenbasierten Export nicht mehr aktivierbar. Der Gruppenstatus
// wird aus den vorhandenen Einträgen abgeleitet (nur "b" -> blocked, nur "w"
// -> whitelisted, gemischt/leer -> inaktiv), damit sich am Verhalten für
// bereits produktiv aktivierte Listen zunächst nichts ändert.
func backfillGroupsFromExistingPools(database *sql.DB) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Ein einziger Scan über die gesamte Tabelle (GROUP BY name, status)
	// statt einer COUNT-Abfrage pro Pool und Status: auf großen Produktiv-DBs
	// (mehrere 100k Zeilen, ohne Index auf name/status) machte Letzteres den
	// Start spürbar langsam bis hin zu Timeouts.
	rows, err := tx.Query(`SELECT name, status, count(*) FROM pools GROUP BY name, status`)
	if err != nil {
		return err
	}
	type counts struct{ w, b int }
	byName := make(map[string]*counts)
	var names []string
	for rows.Next() {
		var name, status string
		var n int
		if err := rows.Scan(&name, &status, &n); err != nil {
			rows.Close()
			return err
		}
		c, ok := byName[name]
		if !ok {
			c = &counts{}
			byName[name] = c
			names = append(names, name)
		}
		switch status {
		case "w":
			c.w = n
		case "b":
			c.b = n
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	app.LogIt.Info(fmt.Sprintf("migriere Schema: lege für %d bestehende Pools passende Gruppen an", len(names)))

	for _, name := range names {
		c := byName[name]
		status := ""
		if c.w == 0 && c.b != 0 {
			status = "b"
		}
		if c.b == 0 && c.w != 0 {
			status = "w"
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO groups(name, status) VALUES(?, ?)`, name, status); err != nil {
			return err
		}
	}
	// group_name in einem Rutsch für alle Zeilen setzen statt pro Pool.
	if _, err := tx.Exec(`UPDATE pools SET group_name = name WHERE group_name = ''`); err != nil {
		return err
	}
	return tx.Commit()
}

// resolvePoolGroup liefert die Gruppe, in der ein Pool bereits geführt wird.
// Existiert der Pool noch nicht, wird eine neue, gleichnamige Gruppe angelegt
// und zurückgegeben - jeder manuell (per CIDR-Formular) angelegte Pool ist
// damit sofort exportierbar, auch ohne dass ein Admin ihn explizit einer
// Gruppe zuweist. Wird von InsertEntry verwendet.
func resolvePoolGroup(dbConn *sql.DB, poolName string) (string, error) {
	groupName, found, err := GetPoolGroup(dbConn, poolName)
	if err != nil {
		return "", err
	}
	if found {
		return groupName, nil
	}
	if err := EnsureGroup(dbConn, poolName); err != nil {
		return "", err
	}
	return poolName, nil
}

// existingPoolGroup liefert die Gruppe eines bereits existierenden Pools,
// oder "" (ungruppiert) wenn der Pool neu ist - anders als resolvePoolGroup
// wird dabei NIE automatisch eine neue Gruppe angelegt. Für den Datei-Import
// (Upload): hochgeladene Dateien sollen nicht automatisch eine gleichnamige
// Gruppe erzeugen, sondern erst nach expliziter Zuweisung durch den Admin
// (oder direkt via /admin/groups/:name/uploadPool) einer Gruppe angehören.
func existingPoolGroup(dbConn *sql.DB, poolName string) (string, error) {
	groupName, found, err := GetPoolGroup(dbConn, poolName)
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	return groupName, nil
}

func InsertEntry(dbConn *sql.DB, cidrString, name, comment, status string) (*PoolEntry, error) {
	if len(comment) > 60 {
		comment = comment[:60]
	}
	re := regexp.MustCompile(`/\d{1,2}$`)
	if !re.MatchString(cidrString) {
		cidrString += "/32"
	}
	startIP, endIP, err := helpers.GetIPRange(cidrString)
	if err != nil {
		return nil, fmt.Errorf("ungültiger CIDR %s: %w", cidrString, err)
	}
	if foundEntry, _ := FindPoolByIP(dbConn, startIP); foundEntry != nil {
		return foundEntry, nil
	}
	if foundEntry, _ := FindPoolByIP(dbConn, endIP); foundEntry != nil {
		return foundEntry, nil
	}
	groupName, err := resolvePoolGroup(dbConn, name)
	if err != nil {
		return nil, err
	}
	_, err = dbConn.Exec(
		"INSERT INTO pools(start_ip_int, end_ip_int, cidr, name, group_name, comment, status) VALUES(?, ?, ?, ?, ?, ?, ?)",
		startIP, endIP, cidrString, name, groupName, comment, status,
	)

	return nil, err
}

func InsertPoollistEntry(dbConn *sql.DB, cidrString, name, comment, status string) error {
	if len(comment) > 60 {
		comment = comment[:60]
	}
	if !strings.Contains(cidrString, "/") {
		cidrString += "/32"
	}
	startIP, endIP, err := helpers.GetIPRange(cidrString)
	if err != nil {
		return fmt.Errorf("ungültiger CIDR %s: %w", cidrString, err)
	}
	groupName, err := existingPoolGroup(dbConn, name)
	if err != nil {
		return err
	}
	_, err = dbConn.Exec(
		"INSERT INTO pools(start_ip_int, end_ip_int, cidr, name, group_name, comment, status) VALUES(?, ?, ?, ?, ?, ?, ?)",
		startIP, endIP, cidrString, name, groupName, comment, status,
	)

	return err
}

func FindPoolByIP(dbConn *sql.DB, ipUint uint32) (*PoolEntry, error) {
	row := dbConn.QueryRow(`
        SELECT id, start_ip_int, end_ip_int, cidr, name, group_name, comment, status
        FROM pools
        WHERE ? BETWEEN start_ip_int AND end_ip_int
        ORDER BY end_ip_int - start_ip_int ASC
        LIMIT 1
    `, ipUint)

	p := &PoolEntry{}
	if err := row.Scan(&p.ID, &p.StartIPInt, &p.EndIPInt, &p.CIDR, &p.Name, &p.GroupName, &p.Comment, &p.Status); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

func ListByPool(dbConn *sql.DB, poolName string) ([]PoolEntry, error) {
	rows, err := dbConn.Query(`
        SELECT id, start_ip_int, end_ip_int, cidr, name, group_name, comment, status
        FROM pools
        WHERE name = ?
        ORDER BY status, start_ip_int
    `, poolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []PoolEntry
	for rows.Next() {
		var p PoolEntry
		if err := rows.Scan(&p.ID, &p.StartIPInt, &p.EndIPInt, &p.CIDR, &p.Name, &p.GroupName, &p.Comment, &p.Status); err != nil {
			return nil, err
		}
		res = append(res, p)
	}
	return res, rows.Err()
}

// ListByGroup liefert alle Einträge einer Gruppe, sortiert nach Pool und Status
// (Grundlage für den gruppierten Export in je einen Abschnitt pro Pool).
func ListByGroup(dbConn *sql.DB, groupName string) ([]PoolEntry, error) {
	rows, err := dbConn.Query(`
        SELECT id, start_ip_int, end_ip_int, cidr, name, group_name, comment, status
        FROM pools
        WHERE group_name = ?
        ORDER BY name, status, start_ip_int
    `, groupName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []PoolEntry
	for rows.Next() {
		var p PoolEntry
		if err := rows.Scan(&p.ID, &p.StartIPInt, &p.EndIPInt, &p.CIDR, &p.Name, &p.GroupName, &p.Comment, &p.Status); err != nil {
			return nil, err
		}
		res = append(res, p)
	}
	return res, rows.Err()
}

// Alle unterschiedlichen Pool-Namen
func ListPoolNames(dbConn *sql.DB) ([]string, error) {
	rows, err := dbConn.Query(`SELECT DISTINCT name FROM pools ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// ListPoolNamesInGroup liefert die unterschiedlichen Pool-Namen einer Gruppe.
func ListPoolNamesInGroup(dbConn *sql.DB, groupName string) ([]string, error) {
	rows, err := dbConn.Query(`SELECT DISTINCT name FROM pools WHERE group_name = ? ORDER BY name`, groupName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// CountDistinctPoolNamesInGroup liefert die Anzahl unterschiedlicher Pool-
// Namen einer Gruppe, optional gefiltert per Teilstring (SQLite LIKE ist für
// ASCII-Buchstaben von Haus aus case-insensitive) - Grundlage für die
// Pagination der Pools-Tabelle auf der Gruppen-Detailseite.
func CountDistinctPoolNamesInGroup(dbConn *sql.DB, groupName, filter string) (int, error) {
	var count int
	var err error
	if filter == "" {
		err = dbConn.QueryRow(`SELECT COUNT(DISTINCT name) FROM pools WHERE group_name = ?`, groupName).Scan(&count)
	} else {
		err = dbConn.QueryRow(`SELECT COUNT(DISTINCT name) FROM pools WHERE group_name = ? AND name LIKE ?`, groupName, "%"+filter+"%").Scan(&count)
	}
	return count, err
}

// ListPoolNamesInGroupPage liefert eine alphabetisch sortierte Seite
// (limit/offset) der unterschiedlichen Pool-Namen einer Gruppe, optional
// gefiltert per Teilstring - siehe CountDistinctPoolNamesInGroup.
func ListPoolNamesInGroupPage(dbConn *sql.DB, groupName, filter string, limit, offset int) ([]string, error) {
	var rows *sql.Rows
	var err error
	if filter == "" {
		rows, err = dbConn.Query(`SELECT DISTINCT name FROM pools WHERE group_name = ? ORDER BY name LIMIT ? OFFSET ?`, groupName, limit, offset)
	} else {
		rows, err = dbConn.Query(`SELECT DISTINCT name FROM pools WHERE group_name = ? AND name LIKE ? ORDER BY name LIMIT ? OFFSET ?`, groupName, "%"+filter+"%", limit, offset)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// PoolStatusCounts liefert je Namen aus poolNames die Anzahl der Einträge
// mit Status "w" bzw. "b" - eine einzige Query für beliebig viele Pools
// einer Gruppe, statt (wie früher) eine Query pro Pool. Für eine sehr große
// Gruppe (mehrere tausend Pools) war das zuvor die eigentliche Bremse beim
// Laden der Gruppen-Detailseite, nicht die Größe der gerenderten Tabelle.
func PoolStatusCounts(dbConn *sql.DB, groupName string, poolNames []string) (map[string]struct{ W, B int }, error) {
	counts := make(map[string]struct{ W, B int }, len(poolNames))
	if len(poolNames) == 0 {
		return counts, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(poolNames)), ",")
	args := make([]any, 0, len(poolNames)+1)
	args = append(args, groupName)
	for _, n := range poolNames {
		args = append(args, n)
	}
	query := fmt.Sprintf(`SELECT name, status, COUNT(*) FROM pools WHERE group_name = ? AND name IN (%s) GROUP BY name, status`, placeholders)
	rows, err := dbConn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var name, status string
		var n int
		if err := rows.Scan(&name, &status, &n); err != nil {
			return nil, err
		}
		c := counts[name]
		switch status {
		case "w":
			c.W = n
		case "b":
			c.B = n
		}
		counts[name] = c
	}
	return counts, rows.Err()
}

// GetPoolGroup liefert die Gruppe, der ein (bereits existierender) Pool
// zugeordnet ist. found=false, wenn der Pool noch keine Einträge hat.
func GetPoolGroup(dbConn *sql.DB, poolName string) (groupName string, found bool, err error) {
	row := dbConn.QueryRow(`SELECT group_name FROM pools WHERE name = ? LIMIT 1`, poolName)
	if err := row.Scan(&groupName); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return groupName, true, nil
}

func WhitelistByID(dbConn *sql.DB, entryID string) error {
	_, err := dbConn.Exec(`UPDATE pools SET status = "w" WHERE id = ?`, entryID)
	return err
}

func BlockByID(dbConn *sql.DB, entryID string) error {
	_, err := dbConn.Exec(`UPDATE pools SET status = "b" WHERE id = ?`, entryID)
	return err
}

func DeleteByID(dbConn *sql.DB, entryID string) error {
	_, err := dbConn.Exec(`DELETE FROM pools WHERE id = ?`, entryID)
	return err
}

// SetEntryStatus setzt den Status eines einzelnen Eintrags direkt, auch auf
// "" (anders als WhitelistByID/BlockByID). Für den Abgleich der DB mit dem
// tatsächlichen Apache-Zustand beim Programmstart gedacht (siehe
// functions.SyncDBWithApacheState).
func SetEntryStatus(dbConn *sql.DB, id int, status string) error {
	_, err := dbConn.Exec(`UPDATE pools SET status = ? WHERE id = ?`, status, id)
	return err
}

// findOverlappingBlacklistForPool liefert in einer einzigen Abfrage alle
// anderswo (nicht poolName selbst) geblockten Einträge, deren CIDR-Bereich
// irgendeinen Eintrag von poolName überlappt - ein Self-Join statt einer
// Prüfung pro einzelner IP-Adresse oder auch nur einer Query pro Eintrag
// (siehe WhitelistPool: bei CIDR-Bereichen wie /8, 16,7 Mio. Adressen, und
// Pools mit tausenden Einträgen würde beides den Server praktisch einfrieren
// bzw. bei sehr großen Gruppen mehrere Minuten dauern).
func findOverlappingBlacklistForPool(dbConn *sql.DB, poolName string) ([]PoolEntry, error) {
	rows, err := dbConn.Query(`
        SELECT DISTINCT o.id, o.start_ip_int, o.end_ip_int, o.cidr, o.name, o.group_name, o.comment, o.status
        FROM pools p
        JOIN pools o
          ON o.status = 'b' AND o.name != p.name
         AND o.start_ip_int <= p.end_ip_int AND o.end_ip_int >= p.start_ip_int
        WHERE p.name = ?
    `, poolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []PoolEntry
	for rows.Next() {
		var p PoolEntry
		if err := rows.Scan(&p.ID, &p.StartIPInt, &p.EndIPInt, &p.CIDR, &p.Name, &p.GroupName, &p.Comment, &p.Status); err != nil {
			return nil, err
		}
		res = append(res, p)
	}
	return res, rows.Err()
}

// Einen Pool whitelisten
func WhitelistPool(dbConn *sql.DB, poolName string) ([]PoolEntry, error) {
	app.LogIt.Debug("whitelisting pool " + poolName)
	foundEntries, err := findOverlappingBlacklistForPool(dbConn, poolName)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler bei der Konfliktprüfung für Pool %s: %v", poolName, err))
		return nil, err
	}
	if foundEntries != nil {
		return foundEntries, nil
	}
	_, err = dbConn.Exec(`UPDATE pools SET status = "w" WHERE name = ?`, poolName)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Fehler beim Update des pools %s: %v", poolName, err))
		return nil, err
	}
	// TODO: hier noch eine eventuell existierende blocklist.conf sichern und löschen
	return nil, err
}

// Einen Pool blocken
func BlockPool(dbConn *sql.DB, poolName string) error {
	_, err := dbConn.Exec(`UPDATE pools SET status = "b" WHERE name = ?`, poolName)
	// TODO: hier noch eine eventuell existierende whitelist.conf sichern und löschen
	return err
}

// BlockPoolPartial blockt nur die ersten maxEntries Einträge eines Pools
// (nach id, also Einfügereihenfolge) und setzt den Rest des Pools explizit
// auf inaktiv zurück - für den zuletzt eingefügten Pool eines Scraper's-
// Pain-Zyklus, dessen komplette Größe das konfigurierte maxRequireLines-
// Budget überschreiten würde (siehe internal/autoblock.startScraperPainCycle).
// Wie BlockPool überschreibt das auch individuell whitelistete Einträge
// dieses Pools.
func BlockPoolPartial(dbConn *sql.DB, poolName string, maxEntries int) error {
	tx, err := dbConn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE pools SET status = '' WHERE name = ?`, poolName); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE pools SET status = 'b' WHERE id IN (SELECT id FROM pools WHERE name = ? ORDER BY id LIMIT ?)`, poolName, maxEntries); err != nil {
		return err
	}
	return tx.Commit()
}

// DeactivatePool setzt den Status aller Einträge eines Pools zurück auf
// inaktiv (weder whitelisted noch blocked). Kein Konflikt möglich (im
// Gegensatz zu WhitelistPool), da eine Deaktivierung nie eine anderswo
// bestehende Sperre unterläuft.
func DeactivatePool(dbConn *sql.DB, poolName string) error {
	_, err := dbConn.Exec(`UPDATE pools SET status = '' WHERE name = ?`, poolName)
	return err
}

// Einen Pool löschen
func DeletePool(dbConn *sql.DB, poolName string) error {
	_, err := dbConn.Exec(`DELETE FROM pools WHERE name = ?`, poolName)
	return err
}

// ===============
// Gruppen
// ===============

// EnsureGroup legt eine Gruppe an, falls sie noch nicht existiert (Status
// zunächst inaktiv). Bereits vorhandene Gruppen bleiben unverändert.
func EnsureGroup(dbConn *sql.DB, groupName string) error {
	_, err := dbConn.Exec(`INSERT OR IGNORE INTO groups(name, status) VALUES(?, '')`, groupName)
	return err
}

// ListGroups liefert alle Gruppen mit ihrem Status.
func ListGroups(dbConn *sql.DB) ([]GroupEntry, error) {
	rows, err := dbConn.Query(`SELECT id, name, status FROM groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []GroupEntry
	for rows.Next() {
		var g GroupEntry
		if err := rows.Scan(&g.ID, &g.Name, &g.Status); err != nil {
			return nil, err
		}
		res = append(res, g)
	}
	return res, rows.Err()
}

// GetGroupStatus liefert den Status einer Gruppe ("", "w" oder "b").
// found=false, wenn die Gruppe nicht existiert.
func GetGroupStatus(dbConn *sql.DB, groupName string) (status string, found bool, err error) {
	row := dbConn.QueryRow(`SELECT status FROM groups WHERE name = ?`, groupName)
	if err := row.Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return status, true, nil
}

// GroupFullyAtStatus prüft, ob eine Gruppe UND alle ihre Pools bereits den
// angegebenen Status haben - dann bewirkt eine erneute Block/Whitelist/
// Deactivate-Aktion inhaltlich nichts und der teure Export+Apache-Reload kann
// übersprungen werden. Wichtig für Aufrufer, die denselben API-Endpunkt
// wiederholt aufrufen (z. B. ein Skript, das eine Gruppe blockt solange eine
// Rate-Schwelle überschritten ist): ohne diese Prüfung würde jeder Aufruf
// einen vollständigen Reload auslösen, obwohl sich am Ergebnis nichts ändert
// - bei einem Apache-Reload von ~60s führt das zu einer nie endenden
// Reload-Kette.
func GroupFullyAtStatus(dbConn *sql.DB, groupName, status string) (bool, error) {
	currentStatus, found, err := GetGroupStatus(dbConn, groupName)
	if err != nil {
		return false, err
	}
	if !found || currentStatus != status {
		return false, nil
	}
	var mismatched int
	row := dbConn.QueryRow(`SELECT COUNT(*) FROM pools WHERE group_name = ? AND status != ?`, groupName, status)
	if err := row.Scan(&mismatched); err != nil {
		return false, err
	}
	return mismatched == 0, nil
}

// SetGroupStatus setzt den Status einer Gruppe (legt sie bei Bedarf an).
func SetGroupStatus(dbConn *sql.DB, groupName, status string) error {
	if err := EnsureGroup(dbConn, groupName); err != nil {
		return err
	}
	_, err := dbConn.Exec(`UPDATE groups SET status = ? WHERE name = ?`, status, groupName)
	return err
}

// DeleteGroup löscht eine Gruppe. Enthaltene Pools/Einträge bleiben bestehen,
// werden aber ungruppiert (group_name = ”), damit keine Daten verloren gehen.
// Eine eventuell vorhandene AutoBlock-Konfiguration der Gruppe wird mit
// gelöscht (siehe DeleteAutoBlockSettings) - sonst bliebe ein verwaister
// Datensatz zurück, der nach einer gleichnamigen Neuanlage der Gruppe
// unerwartet wieder auftaucht.
func DeleteGroup(dbConn *sql.DB, groupName string) error {
	if _, err := dbConn.Exec(`UPDATE pools SET group_name = '' WHERE group_name = ?`, groupName); err != nil {
		return err
	}
	if err := DeleteAutoBlockSettings(dbConn, groupName); err != nil {
		return err
	}
	_, err := dbConn.Exec(`DELETE FROM groups WHERE name = ?`, groupName)
	return err
}

// AssignPoolToGroup verschiebt alle Einträge eines Pools in eine (bei Bedarf
// neu angelegte) Gruppe.
func AssignPoolToGroup(dbConn *sql.DB, poolName, groupName string) error {
	if err := EnsureGroup(dbConn, groupName); err != nil {
		return err
	}
	_, err := dbConn.Exec(`UPDATE pools SET group_name = ? WHERE name = ?`, groupName, poolName)
	return err
}

// SyncGroupStatusesFromPools berechnet für jede Gruppe mit mindestens einem
// zugeordneten Pool-Eintrag den Gruppenstatus aus dem tatsächlichen Status
// ihrer Pools neu (nur "b" -> blocked, nur "w" -> whitelisted, gemischt/leer
// -> inaktiv - dieselbe Regel wie backfillGroupsFromExistingPools). Für den
// Abgleich der DB mit dem tatsächlichen Apache-Zustand beim Programmstart
// gedacht (siehe functions.SyncDBWithApacheState), nachdem dort die
// Einzelstatus der Pool-Einträge bereits korrigiert wurden.
func SyncGroupStatusesFromPools(database *sql.DB) error {
	rows, err := database.Query(`
        SELECT group_name, status, count(*)
        FROM pools
        WHERE group_name != ''
        GROUP BY group_name, status
    `)
	if err != nil {
		return err
	}
	// total zählt alle Einträge der Gruppe (jeder Status), nicht nur w/b -
	// ohne total wäre "nur b" (siehe Doc-Kommentar oben) nicht von "b und
	// daneben noch inaktive Einträge" unterscheidbar. Genau letzteres ist
	// seit Scraper's Pain (siehe internal/autoblock) der Normalfall: dort
	// ist immer nur ein zufälliger Teil der Pools "b", der Rest bleibt "" -
	// ohne total-Prüfung würde dieser Sync-Lauf (bei jedem Programmstart,
	// siehe functions.SyncDBWithApacheState) die Gruppe fälschlich auf
	// vollständig "b" setzen und damit die Scraper's-Pain-Teilauswahl
	// überschreiben.
	type counts struct{ w, b, total int }
	byGroup := make(map[string]*counts)
	for rows.Next() {
		var groupName, status string
		var n int
		if err := rows.Scan(&groupName, &status, &n); err != nil {
			rows.Close()
			return err
		}
		c, ok := byGroup[groupName]
		if !ok {
			c = &counts{}
			byGroup[groupName] = c
		}
		c.total += n
		switch status {
		case "w":
			c.w = n
		case "b":
			c.b = n
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for groupName, c := range byGroup {
		status := ""
		if c.w == c.total {
			status = "w"
		} else if c.b == c.total {
			status = "b"
		}
		if err := SetGroupStatus(database, groupName, status); err != nil {
			return err
		}
	}
	return nil
}

// ===============
// AutoBlock
// ===============

// autoBlockTimeFormat wird für triggered_until verwendet (Text-Spalte,
// SQLite kennt keinen nativen Zeitstempel-Typ).
const autoBlockTimeFormat = time.RFC3339

// AutoBlockSettings ist die je Gruppe konfigurierte Scraping-Schutz-
// Einstellung (siehe internal/autoblock) plus deren aktuellen Laufzeitstatus
// (Active/TriggeredUntil - wird von der Gruppen-AutoBlock-Goroutine selbst
// gepflegt, nicht vom Admin).
type AutoBlockSettings struct {
	GroupName               string
	Enabled                 bool
	Mode                    string // "threshold" oder "scraperspain"
	ThresholdRPS            float64
	ScraperPainPercent      int
	BlockDurationMinSeconds int
	BlockDurationMaxSeconds int
	Active                  bool
	TriggeredUntil          *time.Time
}

func scanAutoBlockSettings(row interface {
	Scan(dest ...any) error
}) (*AutoBlockSettings, error) {
	var s AutoBlockSettings
	var enabled, active int
	var triggeredUntil sql.NullString
	if err := row.Scan(&s.GroupName, &enabled, &s.Mode, &s.ThresholdRPS, &s.ScraperPainPercent, &s.BlockDurationMinSeconds, &s.BlockDurationMaxSeconds, &active, &triggeredUntil); err != nil {
		return nil, err
	}
	s.Enabled = enabled != 0
	s.Active = active != 0
	if triggeredUntil.Valid && triggeredUntil.String != "" {
		if t, err := time.Parse(autoBlockTimeFormat, triggeredUntil.String); err == nil {
			s.TriggeredUntil = &t
		}
	}
	return &s, nil
}

// GetAutoBlockSettings liefert die AutoBlock-Einstellung einer Gruppe, oder
// nil, falls für sie noch nie eine Einstellung gespeichert wurde (kein Fehler).
func GetAutoBlockSettings(dbConn *sql.DB, groupName string) (*AutoBlockSettings, error) {
	row := dbConn.QueryRow(`
        SELECT group_name, enabled, mode, threshold_rps, scraper_pain_percent, block_duration_min_seconds, block_duration_max_seconds, active, triggered_until
        FROM group_autoblock
        WHERE group_name = ?
    `, groupName)
	s, err := scanAutoBlockSettings(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return s, nil
}

// ListEnabledAutoBlock liefert alle Gruppen mit aktivierter AutoBlock-
// Überwachung - Grundlage für autoblock.Manager.StartAll beim Programmstart.
func ListEnabledAutoBlock(dbConn *sql.DB) ([]AutoBlockSettings, error) {
	rows, err := dbConn.Query(`
        SELECT group_name, enabled, mode, threshold_rps, scraper_pain_percent, block_duration_min_seconds, block_duration_max_seconds, active, triggered_until
        FROM group_autoblock
        WHERE enabled = 1
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []AutoBlockSettings
	for rows.Next() {
		s, err := scanAutoBlockSettings(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, *s)
	}
	return res, rows.Err()
}

// GetEnabledAutoBlockGroup liefert den Namen der Gruppe, die gerade
// AutoBlock aktiviert hat (enabled=1), falls es eine gibt. Wegen
// idx_group_autoblock_singleton_enabled (siehe CreateTables) kann es nie mehr
// als eine geben - nur eine Gruppe darf gleichzeitig AutoBlock aktiviert
// haben, da die Ratenmessung serverweit ist und nicht zwischen Gruppen
// unterscheidet (siehe autoblock.Manager.Enable).
func GetEnabledAutoBlockGroup(dbConn *sql.DB) (groupName string, found bool, err error) {
	row := dbConn.QueryRow(`SELECT group_name FROM group_autoblock WHERE enabled = 1 LIMIT 1`)
	if err := row.Scan(&groupName); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return groupName, true, nil
}

// UpsertAutoBlockSettings legt die AutoBlock-Konfiguration einer Gruppe an
// oder überschreibt sie (vom Admin im WebUI gepflegt). Active/TriggeredUntil
// (Laufzeitstatus, siehe SetAutoBlockActive/ClearAutoBlockActive) bleiben
// dabei unangetastet.
func UpsertAutoBlockSettings(dbConn *sql.DB, groupName string, enabled bool, mode string, thresholdRPS float64, scraperPainPercent int, minSeconds, maxSeconds int) error {
	_, err := dbConn.Exec(`
        INSERT INTO group_autoblock(group_name, enabled, mode, threshold_rps, scraper_pain_percent, block_duration_min_seconds, block_duration_max_seconds)
        VALUES(?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(group_name) DO UPDATE SET
            enabled = excluded.enabled,
            mode = excluded.mode,
            threshold_rps = excluded.threshold_rps,
            scraper_pain_percent = excluded.scraper_pain_percent,
            block_duration_min_seconds = excluded.block_duration_min_seconds,
            block_duration_max_seconds = excluded.block_duration_max_seconds
    `, groupName, boolToInt(enabled), mode, thresholdRPS, scraperPainPercent, minSeconds, maxSeconds)
	return err
}

// SetAutoBlockEnabled schaltet nur das enabled-Flag einer bereits
// bestehenden AutoBlock-Konfiguration um, ohne Schwellwert/Blockdauer zu
// berühren. Existiert noch keine Konfiguration für die Gruppe, ist der
// Aufruf ein No-op (0 betroffene Zeilen, kein Fehler) - Fallback für den
// Fall "Formular deaktivieren, aber keine gültigen neuen Werte übermittelt"
// (siehe webserver.go), wo Manager.Save mangels validierter Werte nicht
// aufgerufen werden kann.
func SetAutoBlockEnabled(dbConn *sql.DB, groupName string, enabled bool) error {
	_, err := dbConn.Exec(`UPDATE group_autoblock SET enabled = ? WHERE group_name = ?`, boolToInt(enabled), groupName)
	return err
}

// SetAutoBlockActive markiert eine Gruppe als gerade automatisch geblockt,
// bis spätestens 'until' (siehe autoblock.Manager). Setzt voraus, dass für
// die Gruppe bereits eine Zeile existiert (siehe UpsertAutoBlockSettings).
func SetAutoBlockActive(dbConn *sql.DB, groupName string, until time.Time) error {
	_, err := dbConn.Exec(`UPDATE group_autoblock SET active = 1, triggered_until = ? WHERE group_name = ?`, until.Format(autoBlockTimeFormat), groupName)
	return err
}

// ClearAutoBlockActive beendet einen laufenden AutoBlock-Zustand einer
// Gruppe (Revert durch autoblock.Manager selbst, oder weil ein Admin die
// Gruppe manuell block/whitelist/deaktiviert hat - siehe webserver.go/api.go).
// Ist die Gruppe gar nicht aktiv bzw. existiert noch keine Einstellung, ist
// der Aufruf ein No-op.
func ClearAutoBlockActive(dbConn *sql.DB, groupName string) error {
	_, err := dbConn.Exec(`UPDATE group_autoblock SET active = 0, triggered_until = NULL WHERE group_name = ?`, groupName)
	return err
}

// DeleteAutoBlockSettings löscht die AutoBlock-Konfiguration einer Gruppe
// (z. B. weil die Gruppe selbst gelöscht wird, siehe DeleteGroup) inklusive
// einer evtl. laufenden Scraper's-Pain-Auswahl (siehe
// ClearScraperPainActivePools) - sonst bliebe bei gleichnamiger Neuanlage
// der Gruppe eine verwaiste Auswahl von einem früheren Lauf bestehen.
func DeleteAutoBlockSettings(dbConn *sql.DB, groupName string) error {
	if err := ClearScraperPainActivePools(dbConn, groupName); err != nil {
		return err
	}
	_, err := dbConn.Exec(`DELETE FROM group_autoblock WHERE group_name = ?`, groupName)
	return err
}

// ===============
// Scraper's Pain (Pool-Auswahl des laufenden Zyklus)
// ===============

// SetScraperPainActivePools ersetzt die für groupName gespeicherte Auswahl
// vollständig durch poolNames - aufgerufen bei jedem neuen Scraper's-Pain-
// Zyklus (siehe internal/autoblock.startScraperPainCycle), damit beim
// nächsten Zyklusende/Deaktivieren/Prozess-Neustart genau die aktuell
// geblockten Pools wieder erkannt und freigegeben werden können.
func SetScraperPainActivePools(dbConn *sql.DB, groupName string, poolNames []string) error {
	tx, err := dbConn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM group_scraperpain_pools WHERE group_name = ?`, groupName); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO group_scraperpain_pools(group_name, pool_name) VALUES(?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, poolName := range poolNames {
		if _, err := stmt.Exec(groupName, poolName); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetScraperPainActivePools liefert die Pools, die der aktuelle Scraper's-
// Pain-Zyklus einer Gruppe geblockt hat.
func GetScraperPainActivePools(dbConn *sql.DB, groupName string) ([]string, error) {
	rows, err := dbConn.Query(`SELECT pool_name FROM group_scraperpain_pools WHERE group_name = ? ORDER BY pool_name`, groupName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// ClearScraperPainActivePools löscht die gespeicherte Auswahl einer Gruppe
// (Zyklusende, Deaktivieren oder Moduswechsel - siehe internal/autoblock).
func ClearScraperPainActivePools(dbConn *sql.DB, groupName string) error {
	_, err := dbConn.Exec(`DELETE FROM group_scraperpain_pools WHERE group_name = ?`, groupName)
	return err
}

// PoolCandidate ist ein Pool samt Gesamtgröße (Anzahl Einträge, unabhängig
// vom Status) - Grundlage für die Zeilen-basierte Auswahl in Scraper's Pain
// (siehe internal/autoblock.startScraperPainCycle), da genau diese Zahl an
// "Require not ip"-Zeilen im Blocklist-Export entsteht, sobald der Pool
// vollständig geblockt wird (BlockPool setzt ALLE Einträge auf "b").
type PoolCandidate struct {
	Name string
	Size int
}

// ListPoolCandidatesForScraperPain liefert die Pools einer Gruppe, die nicht
// individuell vollständig whitelisted sind (alle Einträge Status "w") -
// Kandidaten für Scraper's Pain (siehe internal/autoblock): ein individuell
// whitelisteter Pool wird nie automatisch geblockt, analog dazu, dass eine
// whitelistete Gruppe im Schwellwert-Modus nie automatisch geblockt wird.
// Zusätzlich zum Namen liefert jeder Kandidat seine Gesamt-Eintragszahl.
func ListPoolCandidatesForScraperPain(dbConn *sql.DB, groupName string) ([]PoolCandidate, error) {
	entries, err := ListByGroup(dbConn, groupName)
	if err != nil {
		return nil, err
	}
	type counts struct{ w, total int }
	byName := make(map[string]*counts)
	var order []string
	for _, e := range entries {
		c, ok := byName[e.Name]
		if !ok {
			c = &counts{}
			byName[e.Name] = c
			order = append(order, e.Name)
		}
		c.total++
		if e.Status == "w" {
			c.w++
		}
	}
	var candidates []PoolCandidate
	for _, name := range order {
		if c := byName[name]; c.w != c.total {
			candidates = append(candidates, PoolCandidate{Name: name, Size: c.total})
		}
	}
	return candidates, nil
}

// CountEntriesInGroup liefert die Gesamtzahl aller Einträge einer Gruppe
// (unabhängig vom Status) - das ist die Zeilenzahl, die ein voller
// Gruppen-Block (Schwellwert-Modus) im Blocklist-Export erzeugen würde,
// siehe app.AutoBlockConfig.MaxRequireLines.
func CountEntriesInGroup(dbConn *sql.DB, groupName string) (int, error) {
	var count int
	err := dbConn.QueryRow(`SELECT COUNT(*) FROM pools WHERE group_name = ?`, groupName).Scan(&count)
	return count, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
