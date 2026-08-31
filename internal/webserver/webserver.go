package webserver

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/SvenKethz/fairdb/internal/autoblock"
	app "github.com/SvenKethz/fairdb/internal/configuration"
	"github.com/SvenKethz/fairdb/internal/db"
	"github.com/SvenKethz/fairdb/internal/functions"
	"github.com/SvenKethz/fairdb/internal/helpers"
)

// groupStatusLabel übersetzt den internen Status ("w"/"b"/"") in eine
// sprechende Bezeichnung für UI und API.
func groupStatusLabel(status string) string {
	switch status {
	case "w":
		return "whitelisted"
	case "b":
		return "blocked"
	default:
		return "inaktiv"
	}
}

// PoolSummary fasst den Status eines Pools innerhalb einer Gruppe zusammen.
type PoolSummary struct {
	Name              string
	Status            string // "w", "b" oder "" (gemischt/keine Einträge)
	ScraperPainActive bool   // true, wenn der aktuelle Scraper's-Pain-Zyklus der Gruppe diesen Pool geblockt hat
}

func poolSummariesForGroup(database *sql.DB, groupName string) ([]PoolSummary, error) {
	poolNames, err := db.ListPoolNamesInGroup(database, groupName)
	if err != nil {
		return nil, err
	}
	scraperPainActive, err := db.GetScraperPainActivePools(database, groupName)
	if err != nil {
		return nil, err
	}
	scraperPainActiveSet := make(map[string]bool, len(scraperPainActive))
	for _, poolName := range scraperPainActive {
		scraperPainActiveSet[poolName] = true
	}
	summaries := make([]PoolSummary, 0, len(poolNames))
	for _, poolName := range poolNames {
		entries, err := db.ListByPool(database, poolName)
		if err != nil {
			return nil, err
		}
		wCount, bCount := functions.GetStatusCount(entries)
		status := ""
		if wCount == 0 && bCount != 0 {
			status = "b"
		}
		if bCount == 0 && wCount != 0 {
			status = "w"
		}
		summaries = append(summaries, PoolSummary{Name: poolName, Status: status, ScraperPainActive: scraperPainActiveSet[poolName]})
	}
	return summaries, nil
}

// addAutoBlockContext reichert ctx um die AutoBlock-Einstellung/den Status
// einer Gruppe sowie den aktuellen globalen Ratenschnitt an (für die
// Gruppen-Detailseite). Die Karte ist immer sichtbar (Scraper's Pain braucht
// keine statusURL) - "autoBlockMonitorAvailable" steuert nur, ob der
// Schwellwert-Teilbereich nutzbar ist.
func addAutoBlockContext(ctx gin.H, database *sql.DB, autoBlockManager *autoblock.Manager, groupName string) {
	monitor := autoBlockManager.Monitor()
	ctx["autoBlockMonitorAvailable"] = monitor != nil
	if monitor != nil {
		ctx["autoBlockMonitor"] = monitor.Latest()
	}
	ctx["autoBlockVariancePercent"] = app.Config.AutoBlock.ThresholdVariancePercent
	ctx["autoBlockWindowMinutes"] = app.Config.AutoBlock.MeasureWindowMinutes
	settings, err := db.GetAutoBlockSettings(database, groupName)
	if err != nil {
		app.LogIt.Debug(fmt.Sprintf("Fehler beim Laden der AutoBlock-Einstellung für %s: %v", groupName, err))
		return
	}
	ctx["autoBlockSettings"] = settings
	ctx["autoBlockModeThreshold"] = autoblock.ModeThreshold
	ctx["autoBlockModeScraperPain"] = autoblock.ModeScraperPain
	// Blockdauer wird intern (DB, Manager, randomDuration) durchgängig in
	// Sekunden gehalten - im WebUI aber für Menschen in Minuten angezeigt/
	// eingegeben (siehe autoblock-Handler, der beim Speichern zurück in
	// Sekunden umrechnet). Rundung hier ist bewusst unkritisch: die Werte
	// werden ohnehin immer in vollen Minuten neu eingegeben. Immer (auch ohne
	// gespeicherte Settings) setzen, damit das Template nicht auf einen
	// fehlenden Map-Key trifft (rendert sonst als "<no value>").
	minMinutes, maxMinutes := 0, 0
	if settings != nil {
		minMinutes = settings.BlockDurationMinSeconds / 60
		maxMinutes = settings.BlockDurationMaxSeconds / 60
	}
	ctx["autoBlockMinMinutes"] = minMinutes
	ctx["autoBlockMaxMinutes"] = maxMinutes

	if settings != nil && settings.Mode == autoblock.ModeScraperPain && settings.Active {
		activePools, err := db.GetScraperPainActivePools(database, groupName)
		if err != nil {
			app.LogIt.Debug(fmt.Sprintf("Fehler beim Laden der Scraper's-Pain-Auswahl für %s: %v", groupName, err))
		} else {
			ctx["scraperPainActivePools"] = activePools
		}
	}

	// Es darf immer nur eine Gruppe gleichzeitig AutoBlock aktiviert haben
	// (siehe Manager.Save) - für jede andere Gruppe wird die Aktivierung im
	// Formular ausgegraut.
	lockedBy, found, err := db.GetEnabledAutoBlockGroup(database)
	if err != nil {
		app.LogIt.Debug(fmt.Sprintf("Fehler beim Prüfen der AutoBlock-Sperre für %s: %v", groupName, err))
		return
	}
	if found && lockedBy != groupName {
		ctx["autoBlockLockedBy"] = lockedBy
	}
}

// ConfFileInfo beschreibt eine bereits exportierte .conf-Datei für die
// Aktivieren-Übersicht (welche Dateien liegen aktuell in whitelistPath/
// blocklistPath).
type ConfFileInfo struct {
	Name    string
	ModTime string
}

func listConfFiles(path string) ([]ConfFileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	files := make([]ConfFileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".conf" {
			continue
		}
		modTime := ""
		if info, err := e.Info(); err == nil {
			modTime = info.ModTime().Format("2006-01-02 15:04")
		}
		files = append(files, ConfFileInfo{Name: e.Name(), ModTime: modTime})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// NewRouter baut den fairDB-Webserver auf. autoBlockManager ist nie nil
// (siehe main.go) - dessen RateMonitor kann es aber sein, wenn keine
// statusURL konfiguriert ist; dann steht nur der Schwellwert-Modus nicht
// zur Verfügung, Scraper's Pain funktioniert unabhängig davon.
func NewRouter(database *sql.DB, BasePath string, autoBlockManager *autoblock.Manager) *gin.Engine {
	dr := gin.Default()
	dr.SetTrustedProxies(app.Config.TrustedProxies)
	dr.LoadHTMLGlob(app.Config.WebfilesPath + "templates/*.html")
	r := dr.Group(BasePath)
	// Statische Dateien bereitstellen
	r.Static("/static", app.Config.WebfilesPath+"static")
	r.StaticFile("/favicon.ico", app.Config.WebfilesPath+"/static/favicon.ico")

	// Bearer-Auth-geschütztes JSON-API für externe Systeme
	RegisterAPIRoutes(r, database)

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", gin.H{
			"title":    "IP Blocklist Manager",
			"BasePath": BasePath,
		})
	})

	r.POST("/check", func(c *gin.Context) {
		ipStr := strings.TrimSpace(c.PostForm("ip"))
		if ipStr == "" {
			c.HTML(http.StatusBadRequest, "index.html", gin.H{
				"title":    "IP Blocklist Manager",
				"error":    "Bitte eine IP-Adresse eingeben.",
				"BasePath": BasePath,
			})
			return
		}
		parsed := net.ParseIP(ipStr)
		if parsed == nil {
			c.HTML(http.StatusBadRequest, "index.html", gin.H{
				"title":    "IP Blocklist Manager",
				"error":    fmt.Sprintf("Ungültige IP-Adresse: %s", ipStr),
				"BasePath": BasePath,
			})
			return
		}
		ipUint := helpers.IPToUint32(parsed)
		if ipUint == 0 {
			c.HTML(http.StatusBadRequest, "index.html", gin.H{
				"title":    "IP Blocklist Manager",
				"error":    fmt.Sprintf("IP %s konnte nicht verarbeitet werden.", ipStr),
				"BasePath": BasePath,
			})
			return
		}

		foundEntry, err := db.FindPoolByIP(database, ipUint)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "index.html", gin.H{
				"title":    "IP Blocklist Manager",
				"error":    fmt.Sprintf("Fehler bei der DB-Abfrage: %v", err),
				"BasePath": BasePath,
			})
			return
		}

		if foundEntry == nil {
			c.HTML(http.StatusOK, "index.html", gin.H{
				"title":    "IP Blocklist Manager",
				"message":  fmt.Sprintf("IP %s ist nicht registriert.", ipStr),
				"BasePath": BasePath,
			})
			return
		}
		var result string
		switch foundEntry.Status {
		case "w":
			result = fmt.Sprintf("IP %s ist whitelisted (CIDR: %s).", ipStr, foundEntry.CIDR)
		case "b":
			result = fmt.Sprintf("IP %s ist geblockt (CIDR: %s).", ipStr, foundEntry.CIDR)
		}
		c.HTML(http.StatusOK, "index.html", gin.H{
			"title":    "IP Blocklist Manager",
			"message":  result,
			"poolName": foundEntry.Name,
			"comment":  foundEntry.Comment,
			"status":   foundEntry.Status,
			"BasePath": BasePath,
		})
	})
	// Übersicht aller Pools
	r.GET("/pools", func(c *gin.Context) {
		names, err := db.ListPoolNames(database)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "pools.html", gin.H{
				"title":    "Pools",
				"error":    fmt.Sprintf("Fehler beim Laden der Pools: %v", err),
				"BasePath": BasePath,
			})
			return
		}
		c.HTML(http.StatusOK, "pools.html", gin.H{
			"title":    "Pools",
			"pools":    names,
			"BasePath": BasePath,
		})
	})

	// Übersicht aller Gruppen
	r.GET("/groups", func(c *gin.Context) {
		groups, err := db.ListGroups(database)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "groups.html", gin.H{
				"title":    "Gruppen",
				"error":    fmt.Sprintf("Fehler beim Laden der Gruppen: %v", err),
				"BasePath": BasePath,
			})
			return
		}
		c.HTML(http.StatusOK, "groups.html", gin.H{
			"title":    "Gruppen",
			"groups":   groups,
			"BasePath": BasePath,
		})
	})

	// Admin-Bereich
	// Zugangsdaten kommen aus der optionalen envFile (siehe app.AdminUser/
	// app.AdminPassword), Default ist admin/1234, falls keine envFile
	// konfiguriert ist oder die Werte dort fehlen.
	admin := dr.Group("/admin", gin.BasicAuth(gin.Accounts{
		app.AdminUser: app.AdminPassword,
	}))

	// Adminseite
	admin.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "admin.html", gin.H{
			"title":    "Administration",
			"BasePath": BasePath,
		})
	})
	// Konfiguration aktivieren: zeigt die aktuell exportierten Whitelist-/
	// Blocklist-Dateien und bietet den Button zum (Neu-)Aktivieren.
	admin.GET("/activate", func(c *gin.Context) {
		whitelistFiles, wErr := listConfFiles(app.Config.WhitelistPath)
		blocklistFiles, bErr := listConfFiles(app.Config.BlocklistPath)
		var errors []string
		if wErr != nil {
			errors = append(errors, fmt.Sprintf("Whitelist-Verzeichnis (%s): %v", app.Config.WhitelistPath, wErr))
		}
		if bErr != nil {
			errors = append(errors, fmt.Sprintf("Blocklist-Verzeichnis (%s): %v", app.Config.BlocklistPath, bErr))
		}
		if errParam := c.Query("error"); errParam != "" {
			errors = append(errors, errParam)
		}
		c.HTML(http.StatusOK, "activate.html", gin.H{
			"title":          "Konfiguration aktivieren",
			"whitelistPath":  app.Config.WhitelistPath,
			"blocklistPath":  app.Config.BlocklistPath,
			"whitelistFiles": whitelistFiles,
			"blocklistFiles": blocklistFiles,
			"message":        c.Query("message"),
			"errors":         errors,
			"BasePath":       BasePath,
		})
	})
	// Aktiviert die Konfiguration: exportiert die komplette DB in die
	// konfigurierten Verzeichnisse und löst den Apache-Reload aus.
	admin.POST("/activate", func(c *gin.Context) {
		if err := functions.ExportDB2Conf(database); err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/activate?error="+url.QueryEscape(err.Error()))
			return
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/activate?message="+url.QueryEscape("Konfiguration aktiviert, Apache wurde neu geladen."))
	})

	// Gruppenübersicht (Admin)
	admin.GET("/groups", func(c *gin.Context) {
		groups, err := db.ListGroups(database)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "groups.html", gin.H{
				"title":    "Gruppen",
				"error":    fmt.Sprintf("Fehler beim Laden der Gruppen: %v", err),
				"BasePath": BasePath,
			})
			return
		}
		c.HTML(http.StatusOK, "groups.html", gin.H{
			"title":    "Gruppen",
			"groups":   groups,
			"BasePath": BasePath,
		})
	})

	// Gruppe anlegen
	admin.POST("/groups", func(c *gin.Context) {
		groupName := strings.TrimSpace(c.PostForm("name"))
		if groupName == "" {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups?error=Gruppenname fehlt")
			return
		}
		if err := db.EnsureGroup(database, groupName); err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups?error="+err.Error())
			return
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName)
	})

	// Detailseite für eine Gruppe
	admin.GET("/groups/:name", func(c *gin.Context) {
		groupName := c.Param("name")
		status, found, err := db.GetGroupStatus(database, groupName)
		if err != nil || !found {
			c.HTML(http.StatusNotFound, "group_detail.html", gin.H{
				"title":    "Gruppe " + groupName,
				"error":    "Gruppe nicht gefunden",
				"BasePath": BasePath,
			})
			return
		}
		pools, err := poolSummariesForGroup(database, groupName)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "group_detail.html", gin.H{
				"title":    "Gruppe " + groupName,
				"error":    fmt.Sprintf("Fehler beim Laden der Gruppe: %v", err),
				"BasePath": BasePath,
			})
			return
		}
		ctx := gin.H{
			"title":       "Gruppe " + groupName,
			"group":       groupName,
			"groupStatus": status,
			"pools":       pools,
			"error":       c.Query("error"),
			"message":     c.Query("message"),
			"BasePath":    BasePath,
		}
		addAutoBlockContext(ctx, database, autoBlockManager, groupName)
		c.HTML(http.StatusOK, "group_detail.html", ctx)
	})

	// Gruppe whitelisten (blockte Pools verhindern das Whitelisten, siehe db.WhitelistPool)
	admin.POST("/groups/:name/whitelist", func(c *gin.Context) {
		groupName := c.Param("name")
		// Eine manuelle Aktion hat immer Vorrang vor einem laufenden AutoBlock -
		// sonst würde dessen Revert-Timer die manuelle Entscheidung später überschreiben.
		_ = db.ClearAutoBlockActive(database, groupName)
		conflicts, _, _, err, reloadErr := functions.WhitelistGroup(database, groupName)
		if err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+err.Error())
			return
		}
		if conflicts != nil {
			c.HTML(http.StatusOK, "found.html", gin.H{
				"title":    "Gruppe " + groupName,
				"error":    fmt.Sprintf("%v Einträge sind anderswo geblockt - bitte erst lösen", len(conflicts)),
				"entries":  conflicts,
				"poolName": groupName,
				"BasePath": BasePath,
			})
			return
		}
		if reloadErr != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error=Gruppe whitelisted, aber Apache-Reload fehlgeschlagen: "+reloadErr.Error())
			return
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName)
	})

	// Gruppe blocken
	admin.POST("/groups/:name/block", func(c *gin.Context) {
		groupName := c.Param("name")
		// siehe Kommentar bei /whitelist: manuelle Aktion räumt einen
		// laufenden AutoBlock-Zustand auf, damit dessen Revert-Timer diese
		// manuelle Entscheidung nicht später zurücknimmt.
		_ = db.ClearAutoBlockActive(database, groupName)
		_, _, err, reloadErr := functions.BlockGroup(database, groupName)
		if err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+err.Error())
			return
		}
		if reloadErr != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error=Gruppe geblockt, aber Apache-Reload fehlgeschlagen: "+reloadErr.Error())
			return
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName)
	})

	// Gruppe manuell (re-)aktivieren (Export + Apache-Reload), ohne Statusänderung
	admin.POST("/groups/:name/activate", func(c *gin.Context) {
		groupName := c.Param("name")
		wCount, bCount, exportErr, reloadErr := functions.ActivateGroup(database, groupName)
		if exportErr != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+exportErr.Error())
			return
		}
		if reloadErr != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error=Exportiert ("+fmt.Sprintf("%v", wCount+bCount)+" Einträge), aber Apache-Reload fehlgeschlagen: "+reloadErr.Error())
			return
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName)
	})

	// Gruppe löschen (enthaltene Pools werden ungruppiert, nicht gelöscht)
	admin.POST("/groups/:name/delete", func(c *gin.Context) {
		groupName := c.Param("name")
		// Läuft gerade eine AutoBlock-Überwachung für diese Gruppe, muss sie
		// gestoppt werden, bevor die Gruppe verschwindet (siehe DeleteGroup,
		// das auch die AutoBlock-Einstellung mit löscht).
		_ = autoBlockManager.Stop(groupName)
		_ = db.DeleteGroup(database, groupName)
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups")
	})

	// AutoBlock einer Gruppe konfigurieren/aktivieren/deaktivieren (Modus
	// "threshold" oder "scraperspain", siehe internal/autoblock). Die
	// eingegebenen Werte (Schwellwert/Umfang, Blockdauer-Spanne) werden immer
	// gespeichert, auch beim Deaktivieren - sonst würden beim ersten
	// Speichern einer noch nicht aktivierten Gruppe (Checkbox unangetastet)
	// die eingegebenen Werte stillschweigend verworfen und das Formular
	// erschiene beim nächsten Aufruf leer. Ist das jeweils andere Modus-Feld
	// (Schwellwert bzw. Umfang) leer/ungültig übermittelt - z. B. weil es im
	// Formular gerade ausgeblendet war - fällt es auf den zuletzt
	// gespeicherten Wert zurück, statt den anderen Modus zu verwerfen.
	admin.POST("/groups/:name/autoblock", func(c *gin.Context) {
		groupName := c.Param("name")
		existing, _ := db.GetAutoBlockSettings(database, groupName)

		mode := c.PostForm("mode")
		if mode != autoblock.ModeScraperPain {
			mode = autoblock.ModeThreshold
		}
		enabled := c.PostForm("enabled") != ""

		thresholdRPS, errT := strconv.ParseFloat(strings.TrimSpace(c.PostForm("thresholdRPS")), 64)
		if errT != nil && existing != nil {
			thresholdRPS, errT = existing.ThresholdRPS, nil
		}
		scraperPainPercent, errP := strconv.Atoi(strings.TrimSpace(c.PostForm("scraperPainPercent")))
		if errP != nil && existing != nil {
			scraperPainPercent, errP = existing.ScraperPainPercent, nil
		}
		minMinutes, errMin := strconv.Atoi(strings.TrimSpace(c.PostForm("blockDurationMinMinutes")))
		maxMinutes, errMax := strconv.Atoi(strings.TrimSpace(c.PostForm("blockDurationMaxMinutes")))
		// Formular/Anzeige arbeiten in Minuten, DB/Manager/randomDuration
		// intern durchgängig in Sekunden (siehe addAutoBlockContext) - hier an
		// der Formulargrenze umgerechnet.
		minSeconds, maxSeconds := minMinutes*60, maxMinutes*60

		valid := errMin == nil && errMax == nil && minMinutes > 0 && maxMinutes >= minMinutes
		if mode == autoblock.ModeThreshold {
			valid = valid && errT == nil && thresholdRPS > 0
		} else {
			valid = valid && errP == nil && scraperPainPercent >= 1 && scraperPainPercent <= 100
		}

		if !valid {
			if enabled {
				errMsg := "Ungültige AutoBlock-Werte (0 < Blockdauer-Min <= Blockdauer-Max erforderlich"
				if mode == autoblock.ModeThreshold {
					errMsg += ", Schwellwert > 0)"
				} else {
					errMsg += ", Umfang 1-100 %)"
				}
				c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+url.QueryEscape(errMsg))
				return
			}
			// Deaktivieren ohne gültige neue Werte (z. B. Formular manuell
			// geleert): bestehende Konfiguration unangetastet lassen, nur
			// stoppen/zurücksetzen und den enabled-Flag umschalten.
			if err := autoBlockManager.Stop(groupName); err != nil {
				c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+url.QueryEscape(err.Error()))
				return
			}
			if err := db.SetAutoBlockEnabled(database, groupName, false); err != nil {
				c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+url.QueryEscape(err.Error()))
				return
			}
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?message="+url.QueryEscape("AutoBlock deaktiviert."))
			return
		}

		if err := autoBlockManager.Save(groupName, enabled, mode, thresholdRPS, scraperPainPercent, minSeconds, maxSeconds); err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+url.QueryEscape(err.Error()))
			return
		}
		message := "AutoBlock deaktiviert."
		if enabled {
			message = "AutoBlock aktiviert."
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?message="+url.QueryEscape(message))
	})

	// Pool einer Gruppe zuweisen
	admin.POST("/groups/:name/assignPool", func(c *gin.Context) {
		groupName := c.Param("name")
		poolName := strings.TrimSpace(c.PostForm("poolName"))
		if poolName == "" {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error=Poolname fehlt")
			return
		}
		if err := db.AssignPoolToGroup(database, poolName, groupName); err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+err.Error())
			return
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName)
	})

	// Datei-Upload direkt in eine Gruppe: legt (analog zu /admin/pools/upload)
	// einen neuen Pool aus der hochgeladenen Datei an und weist ihn sofort
	// dieser Gruppe zu.
	admin.POST("/groups/:name/uploadPool", func(c *gin.Context) {
		groupName := c.Param("name")
		fileHeader, err := c.FormFile("file")
		if err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+url.QueryEscape("Datei wurde nicht übermittelt."))
			return
		}

		poolName := strings.TrimSuffix(fileHeader.Filename, filepath.Ext(fileHeader.Filename))
		if poolName == "" {
			poolName = "default"
		}

		f, err := fileHeader.Open()
		if err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+url.QueryEscape("Fehler beim Öffnen der Datei."))
			return
		}
		defer f.Close()

		zielStatus := c.PostForm("zielStatus")

		if err := functions.ImportConf(database, f, poolName, zielStatus); err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+url.QueryEscape("Importfehler: "+err.Error()))
			return
		}
		if err := db.AssignPoolToGroup(database, poolName, groupName); err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?error="+url.QueryEscape("Pool '"+poolName+"' importiert, aber Gruppenzuweisung fehlgeschlagen: "+err.Error()))
			return
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/groups/"+groupName+"?message="+url.QueryEscape("Pool '"+poolName+"' importiert und der Gruppe zugewiesen."))
	})

	// Detailseite für einen Pool
	admin.GET("/pools/:name", func(c *gin.Context) {
		poolName := c.Param("name")
		entries, err := db.ListByPool(database, poolName)
		wCount, bCount := functions.GetStatusCount(entries)
		var poolStatus string
		if wCount == 0 && bCount != 0 {
			poolStatus = "b"
		}
		if bCount == 0 && wCount != 0 {
			poolStatus = "w"
		}
		if err != nil {
			c.HTML(http.StatusInternalServerError, "pool_detail.html", gin.H{
				"title":    "Pool " + poolName,
				"error":    fmt.Sprintf("Fehler beim Laden des Pools: %v", err),
				"BasePath": BasePath,
			})
			return
		}
		errCode := c.Query("error")
		groupName, _, err := db.GetPoolGroup(database, poolName)
		if err != nil {
			app.LogIt.Debug(fmt.Sprintf("Fehler beim Ermitteln der Gruppe von Pool %s: %v", poolName, err))
		}

		c.HTML(http.StatusOK, "pool_detail.html", gin.H{
			"title":      "Pool " + poolName,
			"pool":       poolName,
			"poolStatus": poolStatus,
			"group":      groupName,
			"entries":    entries,
			"error":      errCode,
			"BasePath":   BasePath,
		})
	})

	// Pool whitelisten
	admin.POST("/pools/:name/whitelist", func(c *gin.Context) {
		poolName := c.Param("name")
		foundEntries, err := db.WhitelistPool(database, poolName)
		if err != nil {
			c.Redirect(http.StatusInternalServerError, BasePath+"/admin/pools/"+poolName+"?error=Fehler beim whitelisten")
		}
		if foundEntries != nil {
			c.HTML(http.StatusOK, "found.html", gin.H{
				"title":    "Pool " + poolName,
				"error":    fmt.Sprintf("%v Einträge sind geblockt - bitte erst lösen", len(foundEntries)),
				"entries":  foundEntries,
				"poolName": poolName,
			})
			return
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/pools/"+poolName)
	})

	// Pool blocken
	admin.POST("/pools/:name/block", func(c *gin.Context) {
		poolName := c.Param("name")
		_ = db.BlockPool(database, poolName)
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/pools/"+poolName)
	})

	// Pool löschen
	admin.POST("/pools/:name/delete", func(c *gin.Context) {
		poolName := c.Param("name")
		_ = db.DeletePool(database, poolName)
		// "/admin/pools/" (ohne Namen) ist keine registrierte Route (nur
		// "/admin/pools/:name") - auf die tatsächlich existierende
		// Poolübersicht umleiten, wie auch die anderen Templates es tun.
		c.Redirect(http.StatusSeeOther, BasePath+"/pools")
	})

	// Eintrag hinzufügen
	admin.POST("/pools/:name/addIP", func(c *gin.Context) {
		poolName := c.Param("name")
		cidr := strings.TrimSpace(c.PostForm("cidr"))
		comment := strings.TrimSpace(c.PostForm("comment"))
		if cidr == "" {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/pools/"+poolName+"?error=cidr_empty")
			return
		}
		existingEntry, err := db.InsertEntry(database, cidr, poolName, comment, "b")
		if err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/admin/pools/"+poolName+"?error="+err.Error())
			return
		}
		if existingEntry != nil {
			var result string
			switch existingEntry.Status {
			case "w":
				result = fmt.Sprintf("CIDR %s ist whitelisted und wird nicht hinzugefügt", existingEntry.CIDR)
			case "b":
				result = fmt.Sprintf("CIDR %s ist geblockt und wird nicht hinzugefügt", existingEntry.CIDR)
			}
			entries, _ := db.ListByPool(database, poolName)
			c.HTML(http.StatusOK, "pool_detail.html", gin.H{
				"title":    "IP Blocklist Manager",
				"error":    result,
				"poolName": existingEntry.Name,
				"comment":  existingEntry.Comment,
				"status":   existingEntry.Status,
				"entries":  entries,
				"BasePath": BasePath,
			})
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/pools/"+poolName)
	})

	// Eintrag whitelisten
	admin.POST("/pools/:name/whitelistIP", func(c *gin.Context) {
		poolName := c.Param("name")
		entryID := c.PostForm("entryID")
		var m string
		if entryID != "" {
			if err := db.WhitelistByID(database, entryID); err != nil {
				app.LogIt.Debug(fmt.Sprintf("Fehler beim Whitelisten der ID %s : %v", entryID, err))
			}
		} else {
			m = "?error=Fehler beim Whitelisten - keine ID übergeben"
			app.LogIt.Debug(m)
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/pools/"+poolName+m)
	})

	// Eintrag blocken
	admin.POST("/pools/:name/blockIP", func(c *gin.Context) {
		poolName := c.Param("name")
		entryID := c.PostForm("entryID")
		var m string
		if entryID != "" {
			if err := db.BlockByID(database, entryID); err != nil {
				app.LogIt.Debug(fmt.Sprintf("Fehler beim Blocken der ID %s : %v", entryID, err))
			}
		} else {
			m = "?error=Fehler beim Blocken - keine ID übergeben"
			app.LogIt.Debug(m)
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/pools/"+poolName+m)
	})

	// Eintrag löschen
	admin.POST("/pools/:name/deleteIP", func(c *gin.Context) {
		poolName := c.Param("name")
		entryID := c.PostForm("entryID")
		if entryID != "" {
			_ = db.DeleteByID(database, entryID)
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/admin/pools/"+poolName)
	})

	// HTML: Upload einer *.conf mit ImportConf
	admin.POST("/pools/upload", func(c *gin.Context) {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			c.HTML(http.StatusBadRequest, "pools.html", gin.H{
				"title":    "IP Blocklist Manager",
				"error":    "Datei wurde nicht übermittelt.",
				"BasePath": BasePath,
			})
			return
		}

		poolName := strings.TrimSuffix(fileHeader.Filename, filepath.Ext(fileHeader.Filename))
		if poolName == "" {
			poolName = "default"
		}

		f, err := fileHeader.Open()
		if err != nil {
			c.HTML(http.StatusInternalServerError, "index.html", gin.H{
				"title":    "IP Blocklist Manager",
				"error":    "Fehler beim Öffnen der Datei.",
				"BasePath": BasePath,
			})
			return
		}
		defer f.Close()

		zielStatus := c.PostForm("zielStatus")

		err = functions.ImportConf(database, f, poolName, zielStatus)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "index.html", gin.H{
				"title":    "IP Blocklist Manager",
				"error":    fmt.Sprintf("Importfehler: %v", err),
				"BasePath": BasePath,
			})
			return
		}
		names, err := db.ListPoolNames(database)
		c.HTML(http.StatusOK, "pools.html", gin.H{
			"title":    "IP Blocklist Manager",
			"message":  fmt.Sprintf("Liste '%s' importiert.", poolName),
			"pools":    names,
			"BasePath": BasePath,
		})
	})

	return dr
}
