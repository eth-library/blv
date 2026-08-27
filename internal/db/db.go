package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"

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
	   `
	if _, err := database.Exec(sqlStmt); err != nil {
		return err
	}
	return migrateGroupNameColumn(database)
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
func DeleteGroup(dbConn *sql.DB, groupName string) error {
	if _, err := dbConn.Exec(`UPDATE pools SET group_name = '' WHERE group_name = ?`, groupName); err != nil {
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
