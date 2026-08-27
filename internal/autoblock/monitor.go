// Package autoblock misst die serverweite Apache-Requestrate (über
// server-status?auto) und blockt Gruppen automatisch temporär, wenn ihr
// individueller, randomisierter Schwellwert über einen längeren Zeitraum
// überschritten wird. Siehe den Plan "AutoBlock: Scraping-Erkennung über
// Apache mod_status" für den vollständigen Entwurf.
package autoblock

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	app "github.com/SvenKethz/fairdb/internal/configuration"
)

// Snapshot ist der zuletzt berechnete Zustand der Ratenmessung - nicht
// blockierend über RateMonitor.Latest() abfragbar.
type Snapshot struct {
	AvgRPS    float64
	Healthy   bool
	SampledAt time.Time
}

// sample ist ein einzelner erfolgreicher Poll von statusURL.
type sample struct {
	at    time.Time
	total int64
}

// RateMonitor pollt periodisch server-status?auto und hält ein gleitendes
// Fenster der letzten MeasureWindowMinutes. Es gibt eine einzige Instanz pro
// fairDB-Prozess (siehe main.go); viele Gruppen-AutoBlock-Goroutinen lesen
// parallel über Latest() - dafür genügt ein atomar veröffentlichter Zeiger
// (ein Schreiber, viele nebenläufige Leser, keine Koordination nötig), ein
// Channel-basiertes Request/Reply-Protokoll wäre hier nur unnötige
// Komplexität.
type RateMonitor struct {
	statusURL string
	interval  time.Duration
	window    time.Duration
	client    *http.Client
	samples   []sample
	latest    atomic.Pointer[Snapshot]
}

func NewRateMonitor(cfg app.AutoBlockConfig) *RateMonitor {
	return newRateMonitor(cfg.StatusURL, time.Duration(cfg.MeasureIntervalSeconds)*time.Second, time.Duration(cfg.MeasureWindowMinutes)*time.Minute)
}

// NewRateMonitorWithWindow ist wie NewRateMonitor, erlaubt aber ein
// beliebiges (nicht auf ganze Minuten gerundetes) Fenster - für Tests, die
// mit kurzen Intervallen/Fenstern arbeiten wollen, ohne mehrere echte Minuten
// zu warten.
func NewRateMonitorWithWindow(cfg app.AutoBlockConfig, window time.Duration) *RateMonitor {
	return newRateMonitor(cfg.StatusURL, time.Duration(cfg.MeasureIntervalSeconds)*time.Second, window)
}

func newRateMonitor(statusURL string, interval, window time.Duration) *RateMonitor {
	m := &RateMonitor{
		statusURL: statusURL,
		interval:  interval,
		window:    window,
		client:    &http.Client{Timeout: 5 * time.Second},
	}
	m.latest.Store(&Snapshot{Healthy: false})
	return m
}

// Latest liefert den zuletzt berechneten Zustand - nicht blockierend, immer
// sofort verfügbar. Healthy=false bedeutet: server-status war zuletzt nicht
// erreichbar/parsebar oder es liegen noch nicht genug Daten fürs Fenster vor;
// Aufrufer dürfen dann keine AutoBlock-Entscheidung treffen (Fail-Open).
func (m *RateMonitor) Latest() Snapshot {
	return *m.latest.Load()
}

// Run pollt bis ctx beendet wird. Blockiert den aufrufenden Goroutine -
// von main.go per "go monitor.Run(ctx)" gestartet.
func (m *RateMonitor) Run(ctx context.Context) {
	m.poll()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll()
		}
	}
}

func (m *RateMonitor) poll() {
	now := time.Now()
	total, err := m.fetchTotalAccesses()
	if err != nil {
		app.LogIt.Warn(fmt.Sprintf("AutoBlock: server-status nicht erreichbar/lesbar (%s): %v", m.statusURL, err))
		m.publish(now)
		return
	}

	if len(m.samples) > 0 && total < m.samples[len(m.samples)-1].total {
		app.LogIt.Info("AutoBlock: Total Accesses ist gesunken (Apache-Neustart?), Messfenster wird neu aufgebaut")
		m.samples = nil
	}
	m.samples = append(m.samples, sample{at: now, total: total})

	cutoff := now.Add(-m.window)
	i := 0
	for i < len(m.samples) && m.samples[i].at.Before(cutoff) {
		i++
	}
	m.samples = m.samples[i:]

	m.publish(now)
}

// publish berechnet den aktuellen Snapshot aus m.samples und veröffentlicht
// ihn atomar. Healthy erfordert mindestens zwei Samples, die zusammen (fast)
// das komplette Fenster abdecken, UND einen frischen letzten Poll - sonst
// wäre der Schnitt entweder unbestimmt oder (bei ausgefallenem Apache)
// veraltet.
func (m *RateMonitor) publish(now time.Time) {
	snap := Snapshot{SampledAt: now}
	if len(m.samples) >= 2 {
		first := m.samples[0]
		last := m.samples[len(m.samples)-1]
		elapsed := last.at.Sub(first.at)
		staleness := now.Sub(last.at)
		if elapsed > 0 && staleness < 2*m.interval {
			snap.AvgRPS = float64(last.total-first.total) / elapsed.Seconds()
			snap.Healthy = elapsed >= m.window-m.interval
		}
	}
	m.latest.Store(&snap)
}

// fetchTotalAccesses lädt statusURL und extrahiert den Wert der Zeile
// "Total Accesses: <n>" (mod_status ?auto-Format: "Key: Value" pro Zeile).
func (m *RateMonitor) fetchTotalAccesses() (int64, error) {
	req, err := http.NewRequest(http.MethodGet, m.statusURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unerwarteter Status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found || strings.TrimSpace(key) != "Total Accesses" {
			continue
		}
		return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("Feld 'Total Accesses' nicht gefunden")
}
