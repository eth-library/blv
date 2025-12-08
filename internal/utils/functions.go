package utils

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/SvenKethz/blv/internal/db"
)

func ImportConf(database *sql.DB, r io.Reader, poolName string) (int, error) {
	scanner := bufio.NewScanner(r)
	imported := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "Require not ip") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		cidr := parts[3]
		if err := db.InsertPool(database, cidr, poolName); err != nil {
			return imported, fmt.Errorf("Fehler beim Import von %s: %w", cidr, err)
		}
		imported++
	}

	if err := scanner.Err(); err != nil {
		return imported, err
	}
	return imported, nil
}
