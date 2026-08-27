package functions

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	app "github.com/SvenKethz/fairdb/internal/configuration"
	"github.com/SvenKethz/fairdb/internal/db"
	"github.com/SvenKethz/fairdb/internal/helpers"
)

// groupSectionHeader erkennt die von writeGroupConfFile geschriebenen
// Abschnitts-Header ("# WHITELIST <pool>" / "# BLOCKLIST <pool>"), die
// innerhalb einer Gruppen-Datei angeben, zu welchem Pool die nachfolgenden
// Einträge gehören.
var groupSectionHeader = regexp.MustCompile(`^#\s*(?:WHITELIST|BLOCKLIST)\s+(.+?)\s*$`)

// rangeKey identifiziert einen CIDR-Bereich anhand seiner IP-Grenzen statt
// als reiner String - robust gegenüber leicht abweichender Notation
// (z. B. "1.2.3.4" ohne "/32") zwischen .conf-Datei und DB.
type rangeKey struct{ start, end uint32 }

// parseConfAddresses liest eine einzelne .conf-Datei (flache Pool-Datei oder
// Gruppen-Datei mit "#----"-Abschnitten je Pool, siehe writeGroupConfFile)
// und liefert die enthaltenen CIDR-Bereiche je Pool. defaultPool gilt, bis
// der erste Abschnitts-Header gesehen wird (bzw. für Dateien ganz ohne
// Header, dem Format einer einzelnen unsegmentierten Pool-Datei).
func parseConfAddresses(path, defaultPool string) (map[string][]rangeKey, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	result := make(map[string][]rangeKey)
	currentPool := defaultPool
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if m := groupSectionHeader.FindStringSubmatch(line); m != nil {
				currentPool = m[1]
			}
			continue
		}
		if !strings.HasPrefix(line, "Require") && !helpers.StartsWithIP(line) {
			continue
		}
		if idx := strings.Index(line, "#"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}
		var cidr string
		for part := range strings.FieldsSeq(line) {
			if helpers.StartsWithIP(part) {
				cidr = part
			}
		}
		if cidr == "" {
			continue
		}
		if !strings.Contains(cidr, "/") {
			cidr += "/32"
		}
		start, end, err := helpers.GetIPRange(cidr)
		if err != nil {
			app.LogIt.Warn(fmt.Sprintf("Sync: ungültiger CIDR %q in %s, ignoriert: %v", cidr, path, err))
			continue
		}
		result[currentPool] = append(result[currentPool], rangeKey{start, end})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// collectKnownConfAddresses durchsucht dir nach .conf-Dateien, deren Name
// (ohne Endung) mit einem bekannten Pool- oder Gruppennamen übereinstimmt
// ("bekannte" Datei), und liefert die darin gefundenen CIDR-Bereiche je Pool.
// Unbekannte Dateien werden nicht angefasst, nur geloggt. Reine
// Leseoperation - es wird nie in dir geschrieben (insbesondere keine
// Status-/Marker-Datei).
func collectKnownConfAddresses(dir string, poolNames, groupNames map[string]bool) (map[string][]rangeKey, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	result := make(map[string][]rangeKey)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".conf")
		if !poolNames[name] && !groupNames[name] {
			app.LogIt.Debug("Sync: unbekannte Datei ignoriert: " + entry.Name())
			continue
		}
		addrs, err := parseConfAddresses(filepath.Join(dir, entry.Name()), name)
		if err != nil {
			return nil, fmt.Errorf("konnte %s nicht lesen: %w", entry.Name(), err)
		}
		for pool, ranges := range addrs {
			if !poolNames[pool] {
				app.LogIt.Warn(fmt.Sprintf("Sync: %s referenziert unbekannten Pool %q, ignoriert", entry.Name(), pool))
				continue
			}
			result[pool] = append(result[pool], ranges...)
		}
	}
	return result, nil
}

func toRangeSet(m map[string][]rangeKey) map[string]map[rangeKey]bool {
	out := make(map[string]map[rangeKey]bool, len(m))
	for pool, ranges := range m {
		set := make(map[rangeKey]bool, len(ranges))
		for _, r := range ranges {
			set[r] = true
		}
		out[pool] = set
	}
	return out
}

// SyncDBWithApacheState gleicht beim Programmstart den in der DB gespeicherten
// Status jedes Pool-Eintrags mit dem tatsächlichen Apache-Zustand ab. Dabei
// zählen ausschließlich .conf-Dateien in whitelistPath/blocklistPath, deren
// Name (ohne Endung) exakt einem Pool- oder Gruppennamen entspricht
// ("bekannte" Dateien) - alle anderen Dateien werden ignoriert und nicht
// angefasst.
//
// Für jeden bereits in der DB vorhandenen Eintrag gilt in diesem Moment die
// jeweilige .conf-Datei als Source of Truth: taucht sein CIDR in einer
// bekannten Whitelist-Datei auf, wird der Status auf "w" gesetzt, taucht er
// in einer Blocklist-Datei auf, auf "b". Fehlt er in beiden (z. B. weil die
// Datei komplett fehlt), wird der Status auf "" zurückgesetzt - das deckt
// sowohl einzelne manuell geänderte Einträge als auch ein manuell geleertes
// oder aus einer älteren Sicherung wiederhergestelltes Apache-Verzeichnis ab.
// Anschließend wird der Gruppenstatus aus dem neuen Pool-Status neu
// berechnet (siehe db.SyncGroupStatusesFromPools).
//
// Es werden dabei nie neue Pools oder Einträge angelegt (nur bereits in der
// DB vorhandene CIDRs werden abgeglichen) und nie in whitelistPath/
// blocklistPath geschrieben - reine Leseoperation, insbesondere keine
// Status-/Marker-Datei in den Listen-Ordnern.
func SyncDBWithApacheState(database *sql.DB, whitelistPath, blocklistPath string) error {
	poolNamesList, err := db.ListPoolNames(database)
	if err != nil {
		return fmt.Errorf("konnte Pool-Namen nicht lesen: %w", err)
	}
	if len(poolNamesList) == 0 {
		return nil
	}
	poolNames := make(map[string]bool, len(poolNamesList))
	for _, n := range poolNamesList {
		poolNames[n] = true
	}

	groups, err := db.ListGroups(database)
	if err != nil {
		return fmt.Errorf("konnte Gruppen nicht lesen: %w", err)
	}
	groupNames := make(map[string]bool, len(groups))
	for _, g := range groups {
		groupNames[g.Name] = true
	}

	whitelisted, err := collectKnownConfAddresses(whitelistPath, poolNames, groupNames)
	if err != nil {
		return fmt.Errorf("konnte Whitelist-Verzeichnis nicht lesen: %w", err)
	}
	blocked, err := collectKnownConfAddresses(blocklistPath, poolNames, groupNames)
	if err != nil {
		return fmt.Errorf("konnte Blocklist-Verzeichnis nicht lesen: %w", err)
	}

	wSet := toRangeSet(whitelisted)
	bSet := toRangeSet(blocked)

	changed := 0
	for _, poolName := range poolNamesList {
		entries, err := db.ListByPool(database, poolName)
		if err != nil {
			return fmt.Errorf("konnte Pool %s nicht lesen: %w", poolName, err)
		}
		for _, e := range entries {
			key := rangeKey{e.StartIPInt, e.EndIPInt}
			target := ""
			if wSet[poolName][key] {
				target = "w"
			} else if bSet[poolName][key] {
				target = "b"
			}
			if target != e.Status {
				if err := db.SetEntryStatus(database, e.ID, target); err != nil {
					return fmt.Errorf("konnte Status von Eintrag %d (Pool %s) nicht setzen: %w", e.ID, poolName, err)
				}
				changed++
			}
		}
	}

	if err := db.SyncGroupStatusesFromPools(database); err != nil {
		return fmt.Errorf("konnte Gruppenstatus nicht neu berechnen: %w", err)
	}

	if changed > 0 {
		app.LogIt.Info(fmt.Sprintf("Sync mit Apache-Konfiguration: %d Eintrag/Einträge an tatsächlichen Zustand angepasst", changed))
	} else {
		app.LogIt.Debug("Sync mit Apache-Konfiguration: keine Abweichungen gefunden")
	}
	return nil
}
