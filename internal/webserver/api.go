package webserver

import (
	"crypto/subtle"
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/SvenKethz/fairdb/internal/autoblock"
	app "github.com/SvenKethz/fairdb/internal/configuration"
	"github.com/SvenKethz/fairdb/internal/db"
	"github.com/SvenKethz/fairdb/internal/functions"
)

// bearerAuth prüft den Authorization-Header gegen den konfigurierten
// API_TOKEN (siehe app.APIToken, geladen aus der envFile). Ohne konfigurierten
// Token ist das API komplett deaktiviert (503) - es gibt bewusst keinen
// eingebauten Default-Token wie bei den Admin-Zugangsdaten.
func bearerAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if app.APIToken == "" {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "API ist nicht konfiguriert (kein API_TOKEN gesetzt)"})
			return
		}
		const prefix = "Bearer "
		header := c.GetHeader("Authorization")
		if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "fehlender oder ungültiger Authorization-Header"})
			return
		}
		token := header[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(token), []byte(app.APIToken)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "ungültiger Token"})
			return
		}
		c.Next()
	}
}

// RegisterAPIRoutes registriert das Bearer-Auth-geschützte JSON-API unter
// /api/v1 (Gruppen-Status abfragen, block/whitelist/deactivate setzen, sowie
// eine .conf-Datei als neuen bzw. ergänzten Pool zu einer Gruppe hochladen -
// siehe POST .../pools). Einzelne Pool-Einträge (IP-Ebene) bleiben bewusst
// der WebUI vorbehalten.
func RegisterAPIRoutes(r *gin.RouterGroup, database *sql.DB, autoBlockManager *autoblock.Manager) {
	api := r.Group("/api/v1", bearerAuth())

	api.GET("/groups/:name", func(c *gin.Context) {
		groupName := c.Param("name")
		status, found, err := db.GetGroupStatus(database, groupName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !found {
			c.JSON(http.StatusNotFound, gin.H{"error": "Gruppe nicht gefunden"})
			return
		}
		pools, err := poolSummariesForGroup(database, groupName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		poolNames := make([]string, 0, len(pools))
		for _, p := range pools {
			poolNames = append(poolNames, p.Name)
		}
		c.JSON(http.StatusOK, gin.H{
			"name":   groupName,
			"status": groupStatusLabel(status),
			"pools":  poolNames,
		})
	})

	api.POST("/groups/:name/block", func(c *gin.Context) {
		groupName := c.Param("name")
		// AutoBlock und manuelle Aktionen schließen sich gegenseitig aus
		// (siehe disableAutoBlockForGroup) - eine manuelle API-Aktion schaltet
		// einen laufenden AutoBlock komplett ab, nicht nur den aktuellen Block.
		disableAutoBlockForGroup(database, autoBlockManager, groupName)
		wCount, bCount, err, reloadErr := functions.BlockGroup(database, groupName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		resp := gin.H{"name": groupName, "status": "blocked", "exported": wCount + bCount}
		if reloadErr != nil {
			resp["reloadError"] = reloadErr.Error()
		}
		c.JSON(http.StatusOK, resp)
	})

	api.POST("/groups/:name/whitelist", func(c *gin.Context) {
		groupName := c.Param("name")
		disableAutoBlockForGroup(database, autoBlockManager, groupName)
		conflicts, wCount, bCount, err, reloadErr := functions.WhitelistGroup(database, groupName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if conflicts != nil {
			c.JSON(http.StatusConflict, gin.H{
				"error":     "Einträge sind anderswo geblockt - bitte erst lösen",
				"conflicts": conflicts,
			})
			return
		}
		resp := gin.H{"name": groupName, "status": "whitelisted", "exported": wCount + bCount}
		if reloadErr != nil {
			resp["reloadError"] = reloadErr.Error()
		}
		c.JSON(http.StatusOK, resp)
	})

	api.POST("/groups/:name/deactivate", func(c *gin.Context) {
		groupName := c.Param("name")
		disableAutoBlockForGroup(database, autoBlockManager, groupName)
		wCount, bCount, err, reloadErr := functions.DeactivateGroup(database, groupName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		resp := gin.H{"name": groupName, "status": "inactive", "exported": wCount + bCount}
		if reloadErr != nil {
			resp["reloadError"] = reloadErr.Error()
		}
		c.JSON(http.StatusOK, resp)
	})

	// POST .../pools: eine .conf/.txt-Datei als neuen (oder ergänzten, falls
	// der Poolname bereits existiert) Pool importieren und dieser Gruppe
	// zuweisen - API-Äquivalent zum WebUI-Formular "Datei in diese Gruppe
	// hochladen" (siehe admin.POST(".../uploadPool") in webserver.go, gleiche
	// Funktionen darunter: functions.ImportConf + db.AssignPoolToGroup).
	// Löst bewusst KEINEN Export/Apache-Reload aus (wie das WebUI-Pendant) -
	// dafür anschließend block/whitelist/deactivate aufrufen.
	api.POST("/groups/:name/pools", func(c *gin.Context) {
		groupName := c.Param("name")
		fileHeader, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Datei wurde nicht übermittelt (Feld 'file' erwartet)"})
			return
		}

		poolName := strings.TrimSpace(c.PostForm("poolName"))
		if poolName == "" {
			poolName = strings.TrimSuffix(fileHeader.Filename, filepath.Ext(fileHeader.Filename))
		}
		if poolName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "kein Poolname ermittelbar (weder 'poolName' noch aus dem Dateinamen)"})
			return
		}

		status := c.PostForm("status")
		if status != "" && status != "w" && status != "b" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger status - erlaubt sind 'w', 'b' oder leer (inaktiv)"})
			return
		}

		f, err := fileHeader.Open()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Datei konnte nicht geöffnet werden: " + err.Error()})
			return
		}
		defer f.Close()

		if err := functions.ImportConf(database, f, poolName, status); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Importfehler: " + err.Error()})
			return
		}
		if err := db.AssignPoolToGroup(database, poolName, groupName); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Pool importiert, aber Gruppenzuweisung fehlgeschlagen: " + err.Error()})
			return
		}
		entries, err := db.ListByPool(database, poolName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Pool importiert und zugewiesen, aber Nachzählen fehlgeschlagen: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"group":          groupName,
			"pool":           poolName,
			"status":         status,
			"poolEntryCount": len(entries),
			"note":           "Gruppe wurde nicht automatisch (re-)aktiviert - dafür anschließend block/whitelist/deactivate aufrufen",
		})
	})
}
