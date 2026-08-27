package webserver

import (
	"crypto/subtle"
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"

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
// /api/v1 (Gruppen-Status abfragen bzw. block/whitelist/deactivate setzen).
// Pools werden bewusst ausschließlich über die WebUI verwaltet, nicht über
// dieses API.
func RegisterAPIRoutes(r *gin.RouterGroup, database *sql.DB) {
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
		// Eine manuelle API-Aktion hat Vorrang vor einem laufenden AutoBlock -
		// sonst würde dessen Revert-Timer sie später überschreiben.
		_ = db.ClearAutoBlockActive(database, groupName)
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
		_ = db.ClearAutoBlockActive(database, groupName)
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
		_ = db.ClearAutoBlockActive(database, groupName)
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

}
