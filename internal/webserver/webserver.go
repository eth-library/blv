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

func NewRouter(database *sql.DB) *gin.Engine {
	r := gin.Default()
	r.SetTrustedProxies(app.Config.TrustedProxies)
	r.LoadHTMLGlob(app.Config.WebfilesPath + "templates/*.html")
	// Statische Dateien bereitstellen
	r.Static("/static", app.Config.WebfilesPath+"static")

	// HTML: Startseite mit Formularen
	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", gin.H{
			"title": "IP Blocklist Manager",
		})
	})

	// HTML: IP prüfen mit realer DB-Logik
	r.POST("/check", func(c *gin.Context) {
		ipStr := strings.TrimSpace(c.PostForm("ip"))
		if ipStr == "" {
			c.HTML(http.StatusBadRequest, "index.html", gin.H{
				"title": "IP Blocklist Manager",
				"error": "Bitte eine IP-Adresse eingeben.",
			})
			return
		}
		parsed := net.ParseIP(ipStr)
		if parsed == nil {
			c.HTML(http.StatusBadRequest, "index.html", gin.H{
				"title": "IP Blocklist Manager",
				"error": fmt.Sprintf("Ungültige IP-Adresse: %s", ipStr),
			})
			return
		}
		ipUint := helpers.IPToUint32(parsed)
		if ipUint == 0 {
			c.HTML(http.StatusBadRequest, "index.html", gin.H{
				"title": "IP Blocklist Manager",
				"error": fmt.Sprintf("IP %s konnte nicht verarbeitet werden.", ipStr),
			})
			return
		}

		p, err := db.FindPoolByIP(database, ipUint)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "index.html", gin.H{
				"title": "IP Blocklist Manager",
				"error": fmt.Sprintf("Fehler bei der DB-Abfrage: %v", err),
			})
			return
		}

		if p == nil {
			c.HTML(http.StatusOK, "index.html", gin.H{
				"title":  "IP Blocklist Manager",
				"result": fmt.Sprintf("IP %s ist nicht geblockt.", ipStr),
			})
			return
		}

		c.HTML(http.StatusOK, "index.html", gin.H{
			"title":    "IP Blocklist Manager",
			"result":   fmt.Sprintf("IP %s ist geblockt (CIDR: %s).", ipStr, p.CIDR),
			"poolName": p.Name,
			"comment":  p.CommentString(),
		})
	})
	r.POST("/reset", func(c *gin.Context) {
		err := functions.ResetDB(database)
		c.HTML(http.StatusSeeOther, "pools.html", gin.H{
			"error": err,
		})
	})

	// Übersicht aller Pools
	r.GET("/pools", func(c *gin.Context) {
		names, err := db.ListPoolNames(database)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "pools.html", gin.H{
				"title": "Pools",
				"error": fmt.Sprintf("Fehler beim Laden der Pools: %v", err),
			})
			return
		}
		c.HTML(http.StatusOK, "pools.html", gin.H{
			"title": "Pools",
			"pools": names,
		})
	})

	// Detailseite für einen Pool
	r.GET("/pools/:name", func(c *gin.Context) {
		poolName := c.Param("name")
		entries, err := db.ListByPool(database, poolName)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "pool_detail.html", gin.H{
				"title": "Pool " + poolName,
				"error": fmt.Sprintf("Fehler beim Laden des Pools: %v", err),
			})
			return
		}
		errCode := c.Query("err")

		c.HTML(http.StatusOK, "pool_detail.html", gin.H{
			"title":   "Pool " + poolName,
			"pool":    poolName,
			"entries": entries,
			"error":   errCode,
		})
	})

	// Pool löschen
	r.POST("/pools/:name/delete", func(c *gin.Context) {
		poolName := c.Param("name")
		_ = db.DeletePool(database, poolName)
		c.Redirect(http.StatusSeeOther, "/pools/")
	})
	// Eintrag hinzufügen
	r.POST("/pools/:name/addIP", func(c *gin.Context) {
		poolName := c.Param("name")
		cidr := strings.TrimSpace(c.PostForm("cidr"))
		comment := strings.TrimSpace(c.PostForm("comment"))
		if cidr == "" {
			c.Redirect(http.StatusSeeOther, "/pools/"+poolName+"?err=cidr_empty")
			return
		}
		if err := db.InsertPool(database, cidr, poolName, comment); err != nil {
			c.Redirect(http.StatusSeeOther, "/pools/"+poolName+"?err="+err.Error())
			return
		}
		c.Redirect(http.StatusSeeOther, "/pools/"+poolName)
	})

	// Eintrag löschen
	r.POST("/pools/:name/deleteIP", func(c *gin.Context) {
		poolName := c.Param("name")
		cidr := c.PostForm("cidr")
		if cidr != "" {
			_ = db.DeleteByPoolAndCIDR(database, poolName, cidr)
		}
		c.Redirect(http.StatusSeeOther, "/pools/"+poolName)
	})

	// HTML: Upload einer *.conf mit ImportConf
	r.POST("/pools/upload", func(c *gin.Context) {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			c.HTML(http.StatusBadRequest, "pools.html", gin.H{
				"title": "IP Blocklist Manager",
				"error": "Datei wurde nicht übermittelt.",
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
				"title": "IP Blocklist Manager",
				"error": "Fehler beim Öffnen der Datei.",
			})
			return
		}
		defer f.Close()

		count, err := functions.ImportConf(database, f, poolName)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "index.html", gin.H{
				"title": "IP Blocklist Manager",
				"error": fmt.Sprintf("Importfehler: %v", err),
			})
			return
		}

		names, err := db.ListPoolNames(database)
		c.HTML(http.StatusOK, "pools.html", gin.H{
			"title":   "IP Blocklist Manager",
			"message": fmt.Sprintf("Liste '%s' importiert, %d Einträge übernommen.", poolName, count),
			"pools":   names,
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

	return r
}
