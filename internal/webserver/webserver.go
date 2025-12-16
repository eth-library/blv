package webserver

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	app "github.com/SvenKethz/blv/internal/configuration"
	"github.com/SvenKethz/blv/internal/db"
	"github.com/SvenKethz/blv/internal/functions"
	"github.com/SvenKethz/blv/internal/helpers"
)

func NewRouter(database *sql.DB, BasePath string) *gin.Engine {
	dr := gin.Default()
	dr.SetTrustedProxies(app.Config.TrustedProxies)
	dr.LoadHTMLGlob(app.Config.WebfilesPath + "templates/*.html")
	r := dr.Group(BasePath)
	// Statische Dateien bereitstellen
	r.Static("/static", app.Config.WebfilesPath+"static")
	r.StaticFile("/favicon.ico", app.Config.WebfilesPath+"/static/favicon.ico")

	// HTML: Startseite mit Formularen
	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", gin.H{
			"title":    "IP Blocklist Manager",
			"BasePath": BasePath,
		})
	})

	// HTML: IP prüfen mit realer DB-Logik
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

		entries, err := db.FindPoolByIP(database, ipUint)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "index.html", gin.H{
				"title":    "IP Blocklist Manager",
				"error":    fmt.Sprintf("Fehler bei der DB-Abfrage: %v", err),
				"BasePath": BasePath,
			})
			return
		}

		if entries == nil {
			c.HTML(http.StatusOK, "index.html", gin.H{
				"title":    "IP Blocklist Manager",
				"result":   fmt.Sprintf("IP %s ist nicht registriert.", ipStr),
				"BasePath": BasePath,
			})
			return
		}

		c.HTML(http.StatusOK, "found.html", gin.H{
			"title":   "IP Blocklist Manager",
			"result":  "Der Eintrag wurde gefunden",
			"entries": entries,
		})
	})

	r.POST("/reset", func(c *gin.Context) {
		err := functions.ResetDB(database)
		names, err := db.ListPoolNames(database)
		c.HTML(http.StatusOK, "pools.html", gin.H{
			"title":    "IP Blocklist Manager",
			"message":  fmt.Sprintf("%v Pools importiert", len(names)),
			"pools":    names,
			"error":    err,
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

	// Detailseite für einen Pool
	r.GET("/pools/:name", func(c *gin.Context) {
		poolName := c.Param("name")
		entries, err := db.ListByPool(database, poolName)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "pool_detail.html", gin.H{
				"title":    "Pool " + poolName,
				"error":    fmt.Sprintf("Fehler beim Laden des Pools: %v", err),
				"BasePath": BasePath,
			})
			return
		}
		errCode := c.Query("error")

		c.HTML(http.StatusOK, "pool_detail.html", gin.H{
			"title":    "Pool " + poolName,
			"pool":     poolName,
			"entries":  entries,
			"error":    errCode,
			"BasePath": BasePath,
		})
	})

	// Pool exportieren
	r.POST("/pools/:name/export", func(c *gin.Context) {
		poolName := c.Param("name")
		wCount, bCount, err := functions.ExportConf(database, poolName)
		count := wCount + bCount
		if err != nil {
			c.HTML(http.StatusSeeOther, "pool_detail.html", gin.H{
				"title":    "Pool " + poolName,
				"error":    fmt.Sprintf("Fehler beim Export des Pools: %v", err),
				"BasePath": BasePath,
			})
			return
		}
		c.HTML(http.StatusOK, "pool_detail.html", gin.H{
			"title":    "Pool " + poolName,
			"message":  fmt.Sprintf("%v items exportiert", count),
			"BasePath": BasePath,
		})
	})

	// Pool whitelisten
	r.POST("/pools/:name/whitelist", func(c *gin.Context) {
		poolName := c.Param("name")
		_ = db.WhitelistPool(database, poolName)
		c.Redirect(http.StatusSeeOther, BasePath+"/pools/"+poolName)
	})

	// Pool blocken
	r.POST("/pools/:name/block", func(c *gin.Context) {
		poolName := c.Param("name")
		_ = db.BlockPool(database, poolName)
		c.Redirect(http.StatusSeeOther, BasePath+"/pools/"+poolName)
	})

	// Pool löschen
	r.POST("/pools/:name/delete", func(c *gin.Context) {
		poolName := c.Param("name")
		_ = db.DeletePool(database, poolName)
		c.Redirect(http.StatusSeeOther, BasePath+"/pools/")
	})

	// Eintrag hinzufügen
	r.POST("/pools/:name/addIP", func(c *gin.Context) {
		poolName := c.Param("name")
		cidr := strings.TrimSpace(c.PostForm("cidr"))
		comment := strings.TrimSpace(c.PostForm("comment"))
		if cidr == "" {
			c.Redirect(http.StatusSeeOther, BasePath+"/pools/"+poolName+"?error=cidr_empty")
			return
		}
		existingEntries, err := db.InsertEntry(database, cidr, poolName, comment, "b")
		if err != nil {
			c.Redirect(http.StatusSeeOther, BasePath+"/pools/"+poolName+"?error="+err.Error())
			return
		}
		if existingEntries != nil {
			c.HTML(http.StatusOK, "found.html", gin.H{
				"title":   "IP Blocklist Manager",
				"result":  "Der Eintrag wurde gefunden und deshalb nicht hinzugefügt.",
				"entries": existingEntries,
			})
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/pools/"+poolName)
	})

	// Eintrag whitelisten
	r.POST("/pools/:name/whitelistIP", func(c *gin.Context) {
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
		c.Redirect(http.StatusSeeOther, BasePath+"/pools/"+poolName+m)
	})

	// Eintrag blocken
	r.POST("/pools/:name/blockIP", func(c *gin.Context) {
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
		c.Redirect(http.StatusSeeOther, BasePath+"/pools/"+poolName+m)
	})

	// Eintrag löschen
	r.POST("/pools/:name/deleteIP", func(c *gin.Context) {
		poolName := c.Param("name")
		entryID := c.PostForm("entryID")
		if entryID != "" {
			_ = db.DeleteByID(database, entryID)
		}
		c.Redirect(http.StatusSeeOther, BasePath+"/pools/"+poolName)
	})

	// HTML: Upload einer *.conf mit ImportConf
	r.POST("/pools/upload", func(c *gin.Context) {
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

		count, err := functions.ImportConf(database, f, poolName, zielStatus)
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
			"message":  fmt.Sprintf("Liste '%s' importiert, %d Einträge übernommen.", poolName, count),
			"pools":    names,
			"BasePath": BasePath,
		})
	})

	// REST-API: IP prüfen (praktisch identisch zur HTML-Variante, aber JSON)
	// api := r.Group("/api")
	// {
	// 	api.GET("/check", func(c *gin.Context) {
	// 		ipStr := strings.TrimSpace(c.Query("ip"))
	// 		if ipStr == "" {
	// 			c.JSON(http.StatusBadRequest, gin.H{"error": "ip parameter required"})
	// 			return
	// 		}
	// 		parsed := net.ParseIP(ipStr)
	// 		if parsed == nil {
	// 			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid IP"})
	// 			return
	// 		}
	// 		ipUint := utils.IPToUint32(parsed)
	// 		if ipUint == 0 {
	// 			c.JSON(http.StatusBadRequest, gin.H{"error": "IP could not be converted"})
	// 			return
	// 		}
	//
	// 		p, err := db.FindPoolByIP(database, ipUint)
	// 		if err != nil {
	// 			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	// 			return
	// 		}
	// 		if p == nil {
	// 			c.JSON(http.StatusOK, gin.H{"blocked": false})
	// 			return
	// 		}
	// 		c.JSON(http.StatusOK, gin.H{
	// 			"blocked": true,
	// 			"pool":    p.Name,
	// 			"cidr":    p.CIDR,
	// 			"comment": p.Comment,
	// 		})
	// 	})
	// 	api.POST("/add", func(c *gin.Context) {
	// 		var req struct {
	// 			CIDR    string `json:"cidr"`
	// 			Name    string `json:"name"`
	// 			Comment string `json:"comment"`
	// 		}
	// 		if err := c.ShouldBindJSON(&req); err != nil {
	// 			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
	// 			return
	// 		}
	// 		if req.Name == "" {
	// 			req.Name = "default"
	// 		}
	// 		if err := db.InsertPool(database, req.CIDR, req.Name, req.Comment); err != nil {
	// 			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	// 			return
	// 		}
	// 		c.JSON(http.StatusOK, gin.H{"status": "added"})
	// 	})
	// }

	return dr
}
