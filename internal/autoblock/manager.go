package autoblock

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"sync"
	"time"

	app "github.com/SvenKethz/fairdb/internal/configuration"
	"github.com/SvenKethz/fairdb/internal/db"
	"github.com/SvenKethz/fairdb/internal/functions"
)

// ModeThreshold und ModeScraperPain sind die beiden Werte, die
// db.AutoBlockSettings.Mode annehmen kann - siehe evaluate/evaluateScraperPain.
const (
	ModeThreshold   = "threshold"
	ModeScraperPain = "scraperspain"
)

// Manager startet und verwaltet pro Gruppe mit aktiviertem AutoBlock eine
// eigene, langlebige Goroutine. Je nach gewähltem Modus (siehe
// db.AutoBlockSettings.Mode) wertet ein Tick entweder den RateMonitor gegen
// den Schwellwert aus (ModeThreshold, siehe Plan "AutoBlock:
// Scraping-Erkennung über Apache mod_status") oder betreibt einen endlosen
// Zufallsauswahl-Zyklus über einen Teil der Gruppen-Pools (ModeScraperPain).
// Eine Instanz pro fairDB-Prozess, siehe main.go. monitor ist nil, wenn keine
// statusURL konfiguriert ist - ModeThreshold ist dann nicht nutzbar (siehe
// Save), ModeScraperPain funktioniert unabhängig davon.
type Manager struct {
	rootCtx         context.Context
	database        *sql.DB
	monitor         *RateMonitor
	interval        time.Duration
	variancePercent int
	scraperPainMax  int

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func NewManager(ctx context.Context, database *sql.DB, monitor *RateMonitor, cfg app.AutoBlockConfig) *Manager {
	return &Manager{
		rootCtx:         ctx,
		database:        database,
		monitor:         monitor,
		interval:        time.Duration(cfg.MeasureIntervalSeconds) * time.Second,
		variancePercent: cfg.ThresholdVariancePercent,
		scraperPainMax:  cfg.ScraperPainMaxPools,
		cancels:         make(map[string]context.CancelFunc),
	}
}

// ScraperPainMaxPools gibt das konfigurierte Limit zurück (0/negativ = kein
// Limit) - z. B. für die Live-Anzeige im WebUI (siehe addAutoBlockContext).
func (m *Manager) ScraperPainMaxPools() int {
	return m.scraperPainMax
}

// Monitor gibt den zugrundeliegenden RateMonitor zurück (z. B. für die
// Statusanzeige im WebUI), oder nil, wenn keine statusURL konfiguriert ist.
func (m *Manager) Monitor() *RateMonitor {
	return m.monitor
}

// StartAll startet für jede Gruppe mit enabled=1 (siehe db.ListEnabledAutoBlock)
// die Überwachungs-Goroutine. Beim Programmstart aufgerufen - ein evtl. beim
// letzten Lauf aktiver Block (triggered_until in der Vergangenheit) wird
// dabei ganz normal beim nächsten Tick der jeweiligen Goroutine erkannt und
// zurückgesetzt, kein gesonderter Rehydrations-Code nötig.
func (m *Manager) StartAll() error {
	settingsList, err := db.ListEnabledAutoBlock(m.database)
	if err != nil {
		return err
	}
	for _, s := range settingsList {
		m.start(s.GroupName)
	}
	return nil
}

// Save persistiert die vom Admin im WebUI eingegebene AutoBlock-Konfiguration
// einer Gruppe (immer, unabhängig von enabled - siehe unten) und
// startet/stoppt ihre Überwachungs-Goroutine entsprechend.
//
// Wichtig: die Werte werden auch dann gespeichert, wenn enabled=false ist.
// Eine frühere Version speicherte beim Deaktivieren nur den enabled-Flag und
// verwarf die im selben Formular eingegebenen Schwellwerte/Blockdauern
// stillschweigend - im Extremfall (erstes Speichern einer Gruppe mit noch
// nicht gesetzter "aktiviert"-Checkbox) landete dadurch gar keine Zeile in
// der DB und das Formular erschien beim nächsten Aufruf leer. Save
// vereinheitlicht das: es gibt keinen separaten "nur Werte merken,
// ohne zu (de)aktivieren"-Pfad mehr.
//
// Es darf immer nur eine Gruppe gleichzeitig AutoBlock aktiviert haben,
// unabhängig vom Modus (im Schwellwert-Modus, weil die Ratenmessung
// serverweit ist und nicht zwischen Gruppen unterscheidet, siehe
// db.GetEnabledAutoBlockGroup - dieselbe Regel gilt bewusst auch für
// ModeScraperPain, um die Zahl gleichzeitig laufender Zufallszyklen und
// damit einhergehender Apache-Reloads zu begrenzen) - der Versuch, eine
// zweite Gruppe zu aktivieren, wird abgelehnt.
// idx_group_autoblock_singleton_enabled (siehe db.CreateTables) erzwingt das
// zusätzlich hart in der DB, falls zwei Admin-Requests diese Prüfung
// gleichzeitig passieren.
func (m *Manager) Save(groupName string, enabled bool, mode string, thresholdRPS float64, scraperPainPercent int, minSeconds, maxSeconds int) error {
	existing, err := db.GetAutoBlockSettings(m.database, groupName)
	if err != nil {
		return err
	}

	if enabled {
		if mode == ModeThreshold && m.monitor == nil {
			return fmt.Errorf("Schwellwert-basierter AutoBlock ist nicht konfiguriert (keine statusURL) - für diese Gruppe steht nur Scraper's Pain zur Verfügung")
		}
		// Der Schwellwert-Modus blockt bei Auslösung immer die komplette
		// Gruppe (siehe evaluateThreshold/functions.BlockGroup) - bei sehr
		// großen Gruppen würde das denselben zu langen Apache-Reload
		// auslösen, den scraperPainMax für Scraper's Pain gerade begrenzt.
		// Dasselbe Limit gilt daher auch hier als Obergrenze für die
		// Gruppengröße, nicht als Obergrenze für die Blockmenge.
		if mode == ModeThreshold && m.scraperPainMax > 0 {
			poolNames, err := db.ListPoolNamesInGroup(m.database, groupName)
			if err != nil {
				return err
			}
			if len(poolNames) > m.scraperPainMax {
				return fmt.Errorf("Schwellwert-basierter AutoBlock ist für Gruppen mit mehr als %d Pools deaktiviert (aktuell %d Pools) - der volle Gruppen-Block würde einen zu langen Apache-Reload auslösen. Nutzen Sie stattdessen Scraper's Pain", m.scraperPainMax, len(poolNames))
			}
		}
		enabledGroup, found, err := db.GetEnabledAutoBlockGroup(m.database)
		if err != nil {
			return err
		}
		if found && enabledGroup != groupName {
			return fmt.Errorf("AutoBlock ist bereits für Gruppe %q aktiviert - es darf immer nur eine Gruppe gleichzeitig aktiv sein", enabledGroup)
		}

		// Übergang deaktiviert -> aktiviert ("Haken setzen"): die Gruppe
		// zuerst auf inaktiv zurücksetzen, egal welcher manuelle Zustand
		// vorher galt (whitelisted/blocked/inaktiv) - AutoBlock übernimmt
		// immer mit sauberer Baseline (siehe Plan "Entweder AutoBlock oder
		// manuell"). Fehler hier werden nur geloggt, nicht zurückgegeben -
		// eine fehlgeschlagene Reload soll das Aktivieren von AutoBlock
		// selbst nicht verhindern.
		if existing == nil || !existing.Enabled {
			if _, _, deactivateErr, reloadErr := functions.DeactivateGroup(m.database, groupName); deactivateErr != nil {
				app.LogIt.Error(fmt.Sprintf("AutoBlock %s: Baseline-Reset vor Aktivierung fehlgeschlagen: %v", groupName, deactivateErr))
			} else if reloadErr != nil {
				app.LogIt.Error(fmt.Sprintf("AutoBlock %s: Baseline-Reset erfolgreich, aber Apache-Reload fehlgeschlagen: %v", groupName, reloadErr))
			}
		}
	} else if err := m.stopAndRevertIfActive(groupName); err != nil {
		return err
	}

	// Ein Moduswechsel bei einer gerade aktiven Gruppe (z. B. Schwellwert ->
	// Scraper's Pain während ein automatischer Block läuft) muss den alten
	// Zustand zuerst modusgerecht zurücksetzen - sonst bliebe ein veralteter
	// Active/TriggeredUntil-Zustand bis zu dessen ursprünglichem Ablauf
	// bestehen und würde die neue Konfiguration nicht ausgewertet.
	if existing != nil && existing.Active && existing.Mode != mode {
		m.revertMode(groupName, existing.Mode)
	}

	if err := db.UpsertAutoBlockSettings(m.database, groupName, enabled, mode, thresholdRPS, scraperPainPercent, minSeconds, maxSeconds); err != nil {
		return err
	}
	if enabled {
		m.start(groupName)
	}
	return nil
}

// Stop beendet die Überwachungs-Goroutine einer Gruppe sofort und setzt einen
// gerade laufenden automatischen Block zurück, OHNE die gespeicherte
// Konfiguration zu verändern. Für Aufrufer ohne neu eingegebene
// Formularwerte, die nichts zu speichern haben - aktuell die
// Gruppenlöschung (siehe webserver.go), die die AutoBlock-Zeile ohnehin
// direkt im Anschluss löscht.
func (m *Manager) Stop(groupName string) error {
	return m.stopAndRevertIfActive(groupName)
}

func (m *Manager) stopAndRevertIfActive(groupName string) error {
	m.stop(groupName)
	settings, err := db.GetAutoBlockSettings(m.database, groupName)
	if err != nil {
		return err
	}
	if settings != nil && settings.Active {
		m.revertMode(groupName, settings.Mode)
	}
	return nil
}

func (m *Manager) start(groupName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, running := m.cancels[groupName]; running {
		return
	}
	groupCtx, cancel := context.WithCancel(m.rootCtx)
	m.cancels[groupName] = cancel
	go m.runGroupLoop(groupCtx, groupName)
}

func (m *Manager) stop(groupName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, ok := m.cancels[groupName]; ok {
		cancel()
		delete(m.cancels, groupName)
	}
}

func (m *Manager) runGroupLoop(ctx context.Context, groupName string) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.evaluate(groupName)
		}
	}
}

// evaluate ist ein einzelner Tick der Gruppen-AutoBlock-Goroutine: verzweigt
// nach Modus in die Schwellwert- oder die Scraper's-Pain-Auswertung.
func (m *Manager) evaluate(groupName string) {
	settings, err := db.GetAutoBlockSettings(m.database, groupName)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("AutoBlock %s: Einstellungen konnten nicht gelesen werden: %v", groupName, err))
		return
	}
	if settings == nil || !settings.Enabled {
		// wurde zwischenzeitlich deaktiviert/gelöscht - die eigene Goroutine
		// sollte bereits gestoppt worden sein (siehe Disable), sicherheitshalber trotzdem raus.
		return
	}

	if settings.Mode == ModeScraperPain {
		m.evaluateScraperPain(groupName, settings)
		return
	}
	m.evaluateThreshold(groupName, settings)
}

// evaluateThreshold: bei laufendem Block wird nur die Ablaufzeit geprüft,
// sonst der aktuelle RateMonitor-Schnitt gegen den (neu gewürfelten)
// Schwellwert.
func (m *Manager) evaluateThreshold(groupName string, settings *db.AutoBlockSettings) {
	if settings.Active {
		if settings.TriggeredUntil != nil && !time.Now().Before(*settings.TriggeredUntil) {
			m.revertMode(groupName, ModeThreshold)
		}
		return
	}

	status, found, err := db.GetGroupStatus(m.database, groupName)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("AutoBlock %s: Gruppenstatus konnte nicht gelesen werden: %v", groupName, err))
		return
	}
	// Eine explizit whitelisted Gruppe wird nie automatisch geblockt - die
	// Admin-Entscheidung hat Vorrang.
	if !found || status == "w" {
		return
	}

	if m.monitor == nil {
		// Keine statusURL konfiguriert - kann eigentlich nicht passieren
		// (Save lässt ModeThreshold ohne Monitor nicht aktivieren), aber
		// sicherheitshalber statt Nil-Pointer-Zugriff einfach nichts tun.
		return
	}
	snap := m.monitor.Latest()
	if !snap.Healthy {
		return
	}

	threshold := jitter(settings.ThresholdRPS, m.variancePercent)
	if snap.AvgRPS <= threshold {
		return
	}

	duration := randomDuration(settings.BlockDurationMinSeconds, settings.BlockDurationMaxSeconds)
	until := time.Now().Add(duration)

	_, _, err, reloadErr := functions.BlockGroup(m.database, groupName)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("AutoBlock %s: automatischer Block fehlgeschlagen: %v", groupName, err))
		return
	}
	if reloadErr != nil {
		app.LogIt.Error(fmt.Sprintf("AutoBlock %s: geblockt, aber Apache-Reload fehlgeschlagen: %v", groupName, reloadErr))
	}
	if err := db.SetAutoBlockActive(m.database, groupName, until); err != nil {
		app.LogIt.Error(fmt.Sprintf("AutoBlock %s: Aktiv-Status konnte nicht gespeichert werden: %v", groupName, err))
	}
	app.LogIt.Info(fmt.Sprintf("AutoBlock %s: automatisch geblockt bis %s (Ø-RPS %.2f > Schwelle %.2f)", groupName, until.Format(time.RFC3339), snap.AvgRPS, threshold))
}

// evaluateScraperPain: läuft der aktuelle Zyklus noch, nichts tun. Ist er
// abgelaufen, sofort einen neuen Zyklus starten (kein Leerlauf zwischen
// Zyklen - das ist der zentrale Unterschied zu evaluateThreshold, das nach
// Ablauf einfach inaktiv bleibt, bis der Schwellwert erneut überschritten
// wird). Frisch aktiviert oder gerade manuell zurückgesetzt (Active=false):
// eine explizit whitelistete Gruppe wird - wie im Schwellwert-Modus - nie
// automatisch geblockt, sonst würde ein manuelles Gruppen-Whitelisting durch
// den nächsten Tick sofort wieder mit einer neuen Zufallsauswahl überschrieben.
func (m *Manager) evaluateScraperPain(groupName string, settings *db.AutoBlockSettings) {
	if settings.Active {
		if settings.TriggeredUntil != nil && !time.Now().Before(*settings.TriggeredUntil) {
			m.startScraperPainCycle(groupName, settings)
		}
		return
	}

	status, found, err := db.GetGroupStatus(m.database, groupName)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Gruppenstatus konnte nicht gelesen werden: %v", groupName, err))
		return
	}
	if !found || status == "w" {
		return
	}

	m.startScraperPainCycle(groupName, settings)
}

// startScraperPainCycle würfelt eine neue Pool-Auswahl (Umfang siehe
// settings.ScraperPainPercent, Kandidaten ohne individuell whitelistete
// Pools) und eine neue Blockdauer, löst die vorherige Auswahl auf und
// aktiviert die Gruppe (Export + Apache-Reload). Der Gruppenstatus selbst
// bleibt dabei durchgängig "" - es ist immer nur ein Teil der Pools
// geblockt, nie die ganze Gruppe (siehe poolSummariesForGroup im WebUI, das
// das bereits als "gemischt/inaktiv" darstellt).
func (m *Manager) startScraperPainCycle(groupName string, settings *db.AutoBlockSettings) {
	candidates, err := db.ListPoolNamesInGroupExcludingWhitelisted(m.database, groupName)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Pools konnten nicht gelesen werden: %v", groupName, err))
		return
	}
	if len(candidates) == 0 {
		app.LogIt.Debug(fmt.Sprintf("Scraper's Pain %s: keine blockierbaren Pools vorhanden (alle individuell whitelisted oder Gruppe leer)", groupName))
		return
	}

	count := scraperPainCount(len(candidates), settings.ScraperPainPercent, m.scraperPainMax)
	uncappedCount := scraperPainCount(len(candidates), settings.ScraperPainPercent, 0)
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	selected := candidates[:count]
	stillSelected := make(map[string]bool, len(selected))
	for _, poolName := range selected {
		stillSelected[poolName] = true
	}

	// Jeden Kandidaten, der diesmal nicht ausgewählt wurde, explizit auf
	// inaktiv zurücksetzen - nicht nur die Pools der vorherigen Auswahl
	// (group_scraperpain_pools). Sonst bliebe beim allerersten Zyklus (kein
	// "vorher") ein Pool, der zufällig nicht zu den Erst-Kandidaten gehörte
	// aber z. B. noch von der Neuanlage her als "b" markiert war, fälschlich
	// dauerhaft geblockt statt nur die gewürfelte Auswahl.
	for _, poolName := range candidates {
		if !stillSelected[poolName] {
			if err := db.DeactivatePool(m.database, poolName); err != nil {
				app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Pool %s konnte nicht freigegeben werden: %v", groupName, poolName, err))
			}
		}
	}
	for _, poolName := range selected {
		if err := db.BlockPool(m.database, poolName); err != nil {
			app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Pool %s konnte nicht geblockt werden: %v", groupName, poolName, err))
			return
		}
	}
	if err := db.SetScraperPainActivePools(m.database, groupName, selected); err != nil {
		app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Auswahl konnte nicht gespeichert werden: %v", groupName, err))
	}

	duration := randomDuration(settings.BlockDurationMinSeconds, settings.BlockDurationMaxSeconds)
	until := time.Now().Add(duration)
	if err := db.SetAutoBlockActive(m.database, groupName, until); err != nil {
		app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Aktiv-Status konnte nicht gespeichert werden: %v", groupName, err))
	}

	if _, _, exportErr, reloadErr := functions.ActivateGroup(m.database, groupName); exportErr != nil {
		app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Export fehlgeschlagen: %v", groupName, exportErr))
	} else if reloadErr != nil {
		app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Apache-Reload fehlgeschlagen: %v", groupName, reloadErr))
	}
	capNote := ""
	if count < uncappedCount {
		capNote = fmt.Sprintf(" (Umfang wäre %d gewesen, auf scraperPainMaxPools=%d begrenzt)", uncappedCount, m.scraperPainMax)
	}
	app.LogIt.Info(fmt.Sprintf("Scraper's Pain %s: %d von %d Pools geblockt bis %s%s", groupName, len(selected), len(candidates), until.Format(time.RFC3339), capNote))
}

// scraperPainCount rechnet den konfigurierten Prozentanteil in eine
// konkrete Pool-Anzahl um: aufgerundet, mindestens 1 (sofern Kandidaten
// vorhanden), höchstens alle Kandidaten - und danach zusätzlich auf maxPools
// gedeckelt (0/negativ = kein Limit). Der Deckel schützt vor sehr langen
// Apache-Reloads bei sehr großen Gruppen: die Reload-Dauer hängt an der Zahl
// der Require-Direktiven in der exportierten .conf-Datei, siehe
// app.AutoBlockConfig.ScraperPainMaxPools.
func scraperPainCount(total, percent, maxPools int) int {
	if total <= 0 {
		return 0
	}
	count := (total*percent + 99) / 100
	if count < 1 {
		count = 1
	}
	if count > total {
		count = total
	}
	if maxPools > 0 && count > maxPools {
		count = maxPools
	}
	return count
}

// revertMode beendet einen laufenden automatischen Block modusabhängig: im
// Schwellwert-Modus wird die gesamte Gruppe zurückgesetzt, bei Scraper's
// Pain nur die zuletzt ausgewählten Pools.
func (m *Manager) revertMode(groupName, mode string) {
	if mode == ModeScraperPain {
		m.revertScraperPain(groupName)
		return
	}
	_, _, err, reloadErr := functions.DeactivateGroup(m.database, groupName)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("AutoBlock %s: automatischer Revert fehlgeschlagen: %v", groupName, err))
		return
	}
	if reloadErr != nil {
		app.LogIt.Error(fmt.Sprintf("AutoBlock %s: Revert erfolgreich, aber Apache-Reload fehlgeschlagen: %v", groupName, reloadErr))
	}
	if err := db.ClearAutoBlockActive(m.database, groupName); err != nil {
		app.LogIt.Error(fmt.Sprintf("AutoBlock %s: Aktiv-Status konnte nicht zurückgesetzt werden: %v", groupName, err))
		return
	}
	app.LogIt.Info(fmt.Sprintf("AutoBlock %s: automatischer Block beendet, Gruppe wieder inaktiv", groupName))
}

func (m *Manager) revertScraperPain(groupName string) {
	poolNames, err := db.GetScraperPainActivePools(m.database, groupName)
	if err != nil {
		app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: aktuelle Auswahl konnte nicht gelesen werden: %v", groupName, err))
		return
	}
	for _, poolName := range poolNames {
		if err := db.DeactivatePool(m.database, poolName); err != nil {
			app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Pool %s konnte nicht freigegeben werden: %v", groupName, poolName, err))
		}
	}
	if err := db.ClearScraperPainActivePools(m.database, groupName); err != nil {
		app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Auswahl konnte nicht gelöscht werden: %v", groupName, err))
	}
	if err := db.ClearAutoBlockActive(m.database, groupName); err != nil {
		app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Aktiv-Status konnte nicht zurückgesetzt werden: %v", groupName, err))
		return
	}
	if len(poolNames) > 0 {
		if _, _, exportErr, reloadErr := functions.ActivateGroup(m.database, groupName); exportErr != nil {
			app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Export fehlgeschlagen: %v", groupName, exportErr))
		} else if reloadErr != nil {
			app.LogIt.Error(fmt.Sprintf("Scraper's Pain %s: Apache-Reload fehlgeschlagen: %v", groupName, reloadErr))
		}
	}
	app.LogIt.Info(fmt.Sprintf("Scraper's Pain %s: beendet, %d Pools wieder freigegeben", groupName, len(poolNames)))
}

// jitter würfelt value um ±variancePercent - bei jeder Auswertung neu, damit
// der effektive Schwellwert für Scraper nicht vorhersagbar ist.
func jitter(value float64, variancePercent int) float64 {
	if variancePercent <= 0 {
		return value
	}
	factor := 1 + (rand.Float64()*2-1)*float64(variancePercent)/100
	return value * factor
}

// randomDuration würfelt eine Blockdauer im konfigurierten Intervall.
func randomDuration(minSeconds, maxSeconds int) time.Duration {
	if maxSeconds <= minSeconds {
		return time.Duration(minSeconds) * time.Second
	}
	span := maxSeconds - minSeconds
	return time.Duration(minSeconds+rand.Intn(span)) * time.Second
}
