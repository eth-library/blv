package webserver

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/SvenKethz/blv/internal/db"
	"github.com/SvenKethz/blv/internal/functions"
	"github.com/SvenKethz/blv/internal/utils"
)

func NewRouter(database *sql.DB) *gin.Engine {
	r := gin.Default()
	r.LoadHTMLGlob("templates/*.html")

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
		ipUint := utils.IPToUint32(parsed)
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
			"title":  "IP Blocklist Manager",
			"result": fmt.Sprintf("IP %s ist geblockt in Pool '%s' (CIDR: %s).", ipStr, p.Name, p.CIDR),
		})
	})

	// HTML: Upload einer *.conf mit ImportConf
	r.POST("/upload", func(c *gin.Context) {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			c.HTML(http.StatusBadRequest, "index.html", gin.H{
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

		c.HTML(http.StatusOK, "index.html", gin.H{
			"title":   "IP Blocklist Manager",
			"message": fmt.Sprintf("Liste '%s' importiert, %d Einträge übernommen.", poolName, count),
		})
	})

	// REST-API: IP prüfen (praktisch identisch zur HTML-Variante, aber JSON)
	api := r.Group("/api")
	{
		api.GET("/check", func(c *gin.Context) {
			ipStr := strings.TrimSpace(c.Query("ip"))
			if ipStr == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "ip parameter required"})
				return
			}
			parsed := net.ParseIP(ipStr)
			if parsed == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid IP"})
				return
			}
			ipUint := netutil.IPToUint32(parsed)
			if ipUint == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "IP could not be converted"})
				return
			}

			p, err := db.FindPoolByIP(database, ipUint)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if p == nil {
				c.JSON(http.StatusOK, gin.H{"blocked": false})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"blocked": true,
				"pool":    p.Name,
				"cidr":    p.CIDR,
			})
		})
	}

	return r
}
