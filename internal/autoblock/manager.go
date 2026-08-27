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

// Manager startet und verwaltet pro Gruppe mit aktiviertem AutoBlock eine
// eigene, langlebige Goroutine, die in regelmäßigen Abständen den RateMonitor
// abfragt und über functions.BlockGroup/DeactivateGroup entscheidet (siehe
// Plan "AutoBlock: Scraping-Erkennung über Apache mod_status"). Eine Instanz
// pro fairDB-Prozess, siehe main.go.
type Manager struct {
	rootCtx         context.Context
	database        *sql.DB
	monitor         *RateMonitor
	interval        time.Duration
	variancePercent int

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
		cancels:         make(map[string]context.CancelFunc),
	}
}

// Monitor gibt den zugrundeliegenden RateMonitor zurück (z. B. für die
// Statusanzeige im WebUI).
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
// Es darf immer nur eine Gruppe gleichzeitig AutoBlock aktiviert haben (die
// Ratenmessung ist serverweit und unterscheidet nicht zwischen Gruppen,
// siehe db.GetEnabledAutoBlockGroup) - der Versuch, eine zweite Gruppe zu
// aktivieren, wird abgelehnt. idx_group_autoblock_singleton_enabled (siehe
// db.CreateTables) erzwingt das zusätzlich hart in der DB, falls zwei
// Admin-Requests diese Prüfung gleichzeitig passieren.
func (m *Manager) Save(groupName string, enabled bool, thresholdRPS float64, minSeconds, maxSeconds int) error {
	if enabled {
		existing, found, err := db.GetEnabledAutoBlockGroup(m.database)
		if err != nil {
			return err
		}
		if found && existing != groupName {
			return fmt.Errorf("AutoBlock ist bereits für Gruppe %q aktiviert - es darf immer nur eine Gruppe gleichzeitig aktiv sein", existing)
		}
	} else if err := m.stopAndRevertIfActive(groupName); err != nil {
		return err
	}

	if err := db.UpsertAutoBlockSettings(m.database, groupName, enabled, thresholdRPS, minSeconds, maxSeconds); err != nil {
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
		m.revert(groupName)
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

// evaluate ist ein einzelner Tick der Gruppen-AutoBlock-Goroutine: bei
// laufendem Block wird nur die Ablaufzeit geprüft, sonst der aktuelle
// RateMonitor-Schnitt gegen den (neu gewürfelten) Schwellwert.
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

	if settings.Active {
		if settings.TriggeredUntil != nil && !time.Now().Before(*settings.TriggeredUntil) {
			m.revert(groupName)
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

func (m *Manager) revert(groupName string) {
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
