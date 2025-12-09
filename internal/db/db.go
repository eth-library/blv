package db

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"

	"github.com/SvenKethz/blv/internal/utils"
)

type Pool struct {
	ID         int
	StartIPInt uint32
	EndIPInt   uint32
	CIDR       string
	Name       string
	Comment    sql.NullString // kann ja leer sein
}

// Hilfsfunktion: NullString zu normalem String ("" bei NULL)
func (p Pool) CommentString() string {
	if p.Comment.Valid {
		return p.Comment.String
	}
	return ""
}

func Open(path string) (*sql.DB, error) {
	return sql.Open("sqlite3", fmt.Sprintf("file:%s?_journal_mode=WAL", path))
}

func CreateTables(db *sql.DB) error {
	const sqlStmt = `
    CREATE TABLE IF NOT EXISTS pools (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        start_ip_int INTEGER NOT NULL,
        end_ip_int INTEGER NOT NULL,
        cidr TEXT NOT NULL,
        name TEXT,
        comment TEXT
    );
    CREATE INDEX IF NOT EXISTS idx_ip_range ON pools (start_ip_int, end_ip_int);
    `
	_, err := db.Exec(sqlStmt)
	return err
}

func InsertPool(dbConn *sql.DB, cidrString, name string, comment string) error {
	if len(comment) > 60 {
		comment = comment[:60]
	}
	startIP, endIP, err := utils.GetIPRange(cidrString)
	if err != nil {
		return fmt.Errorf("ungültiger CIDR %s: %w", cidrString, err)
	}
	_, err = dbConn.Exec(
		"INSERT INTO pools(start_ip_int, end_ip_int, cidr, name, comment) VALUES(?, ?, ?, ?, ?)",
		startIP, endIP, cidrString, name, comment,
	)
	return err
}

func FindPoolByIP(dbConn *sql.DB, ipUint uint32) (*Pool, error) {
	row := dbConn.QueryRow(`
        SELECT id, start_ip_int, end_ip_int, cidr, name, comment
        FROM pools
        WHERE ? BETWEEN start_ip_int AND end_ip_int
        ORDER BY end_ip_int - start_ip_int ASC
        LIMIT 1
    `, ipUint)

	p := &Pool{}
	if err := row.Scan(&p.ID, &p.StartIPInt, &p.EndIPInt, &p.CIDR, &p.Name, &p.Comment); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

func ListByPool(dbConn *sql.DB, poolName string) ([]Pool, error) {
	rows, err := dbConn.Query(`
        SELECT id, start_ip_int, end_ip_int, cidr, name, comment
        FROM pools
        WHERE name = ?
        ORDER BY cidr
    `, poolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []Pool
	for rows.Next() {
		var p Pool
		if err := rows.Scan(&p.ID, &p.StartIPInt, &p.EndIPInt, &p.CIDR, &p.Name, &p.Comment); err != nil {
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

// Einen Eintrag per CIDR und Pool löschen
func DeleteByPoolAndCIDR(dbConn *sql.DB, poolName, cidr string) error {
	_, err := dbConn.Exec(`DELETE FROM pools WHERE name = ? AND cidr = ?`, poolName, cidr)
	return err
}

// Einen Pool löschen
func DeletePool(dbConn *sql.DB, poolName string) error {
	fmt.Println("trying to delete Pool ", poolName)
	r, err := dbConn.Exec(`DELETE FROM pools WHERE name = ?`, poolName)
	fmt.Println("deleted ", r, " from ", poolName)
	return err
}
