package db

import (
	"database/sql"
	"fmt"
	"net"
	"regexp"

	// _ "github.com/mattn/go-sqlite3"
	_ "modernc.org/sqlite"

	app "github.com/SvenKethz/blv/internal/configuration"
	"github.com/SvenKethz/blv/internal/helpers"
)

type PoolEntry struct {
	ID         int
	StartIPInt uint32
	EndIPInt   uint32
	CIDR       string
	Name       string
	Comment    string
	Status     string
}

func Open(path string) (*sql.DB, error) {
	return sql.Open("sqlite", fmt.Sprintf("file:%s?_journal_mode=WAL", path))
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
	   CREATE TABLE IF NOT EXISTS lut (
	       id INTEGER PRIMARY KEY AUTOINCREMENT,
	       ip_int INTEGER NOT NULL,
	       name TEXT
	   );
	   CREATE INDEX IF NOT EXISTS host_name ON lut (name);
	   `
	_, err := database.Exec(sqlStmt)
	return err
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
	_, err = dbConn.Exec(
		"INSERT INTO pools(start_ip_int, end_ip_int, cidr, name, comment, status) VALUES(?, ?, ?, ?, ?, ?)",
		startIP, endIP, cidrString, name, comment, status,
	)

	return nil, err
}

func InsertLutItem(dbConn *sql.DB, ip_addr string, name string) error {
	ipNet := net.ParseIP(ip_addr)
	ip_int := helpers.IPToUint32(ipNet)
	_, err := dbConn.Exec(
		"INSERT INTO lut(ip_int, name) VALUES(?, ?)",
		ip_int, name,
	)
	return err
}

func FindPoolByIP(dbConn *sql.DB, ipUint uint32) (*PoolEntry, error) {
	row := dbConn.QueryRow(`
        SELECT id, start_ip_int, end_ip_int, cidr, name, comment, status
        FROM pools
        WHERE ? BETWEEN start_ip_int AND end_ip_int
        ORDER BY end_ip_int - start_ip_int ASC
        LIMIT 1
    `, ipUint)

	p := &PoolEntry{}
	if err := row.Scan(&p.ID, &p.StartIPInt, &p.EndIPInt, &p.CIDR, &p.Name, &p.Comment, &p.Status); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// func FindPoolByIP(dbConn *sql.DB, ipUint uint32) ([]PoolEntry, error) {
// 	app.LogIt.Debug(fmt.Sprintf("trying to find %v", ipUint))
// 	rows, err := dbConn.Query(`
// 	       SELECT id, start_ip_int, end_ip_int, cidr, name, comment, status
// 	       FROM pools
// 	       WHERE ? BETWEEN start_ip_int AND end_ip_int
// 	       ORDER BY end_ip_int - start_ip_int ASC
// 	   `, ipUint)
// 	if err != nil {
// 		return nil, err
// 	}
// 	defer rows.Close()
// 	var res []PoolEntry
// 	for rows.Next() {
// 		var p PoolEntry
// 		if err := rows.Scan(&p.ID, &p.StartIPInt, &p.EndIPInt, &p.CIDR, &p.Name, &p.Comment, &p.Status); err != nil {
// 			return nil, err
// 		}
// 		res = append(res, p)
// 	}
// 	app.LogIt.Debug(fmt.Sprintf("found %d entries", len(res)))
// 	return res, rows.Err()
// }

func ListByPool(dbConn *sql.DB, poolName string) ([]PoolEntry, error) {
	rows, err := dbConn.Query(`
        SELECT id, start_ip_int, end_ip_int, cidr, name, comment, status
        FROM pools
        WHERE name = ?
        ORDER BY status, cidr
    `, poolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []PoolEntry
	for rows.Next() {
		var p PoolEntry
		if err := rows.Scan(&p.ID, &p.StartIPInt, &p.EndIPInt, &p.CIDR, &p.Name, &p.Comment, &p.Status); err != nil {
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

// Einen Pool whitelisten
func WhitelistPool(dbConn *sql.DB, poolName string) error {
	_, err := dbConn.Exec(`UPDATE pools SET status = "w" WHERE name = ?`, poolName)
	return err
}

// Einen Pool blocken
func BlockPool(dbConn *sql.DB, poolName string) error {
	_, err := dbConn.Exec(`UPDATE pools SET status = "b" WHERE name = ?`, poolName)
	return err
}

// Einen Pool löschen
func DeletePool(dbConn *sql.DB, poolName string) error {
	_, err := dbConn.Exec(`DELETE FROM pools WHERE name = ?`, poolName)
	return err
}
