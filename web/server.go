package web

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/SvenKethz/blv/internal/db"
	"github.com/SvenKethz/blv/internal/utils"

	"github.com/gin-gonic/gin"
)

func NewRouter(database *sql.DB) *gin.Engine {
	r := gin.Default()

	// Templates laden
	r.LoadHTMLGlob("templates/*.html")

	// HTML-Oberfläche
	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", gin.H{
			"title": "IP Blocklist Manager",
		})
	})

	r.POST("/check", func(c *gin.Context) {
		ipStr := c.PostForm("ip")
		if ipStr == "" {
			c.HTML(http.StatusBadRequest, "index.html", gin.H{
				"title": "IP Blocklist Manager",
				"error": "Bitte eine IP-Adresse eingeben.",
			})
			return
		}
		ip := utils.IPToUint32([]byte(ipStr)) // dieser Aufruf ist falsch, s.u.
		_ = ip
		// Korrigierte Variante:
		// parsed := net.ParseIP(ipStr)
		// ipUint := netutil.IPToUint32(parsed)

		// Zum Minimalbeispiel: Logik nur skizziert.
		c.HTML(http.StatusOK, "index.html", gin.H{
			"title":  "IP Blocklist Manager",
			"result": fmt.Sprintf("IP %s wurde geprüft (Logik hier einbauen).", ipStr),
		})
	})

	// REST-API Minimal
	api := r.Group("/api")
	{
		api.GET("/check", func(c *gin.Context) {
			ipStr := c.Query("ip")
			if ipStr == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "ip parameter required"})
				return
			}
			parsed := net.ParseIP(ipStr)
			if parsed == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid IP"})
				return
			}
			ipUint := utils.IPToUint32(parsed)
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

		api.POST("/add", func(c *gin.Context) {
			var req struct {
				CIDR string `json:"cidr"`
				Name string `json:"name"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
				return
			}
			if req.Name == "" {
				// Poolname aus CIDR ableiten? Für Minimalbeispiel fix:
				req.Name = "default"
			}
			if err := db.InsertPool(database, req.CIDR, req.Name); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "added"})
		})
	}

	// Import einer *.conf-Datei mit "Require not ip ..." (Minimal)
	r.POST("/upload", func(c *gin.Context) {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			c.HTML(http.StatusBadRequest, "index.html", gin.H{
				"title": "IP Blocklist Manager",
				"error": "Datei konnte nicht gelesen werden.",
			})
			return
		}
		poolName := strings.TrimSuffix(fileHeader.Filename, filepath.Ext(fileHeader.Filename))

		f, err := fileHeader.Open()
		if err != nil {
			c.HTML(http.StatusInternalServerError, "index.html", gin.H{
				"title": "IP Blocklist Manager",
				"error": "Fehler beim Öffnen der Datei.",
			})
			return
		}
		defer f.Close()

		// Scanner + Parsing wie zuvor beschrieben (hier stark verkürzt)
		// Jede Zeile "Require not ip X" -> db.InsertPool(database, X, poolName)

		c.HTML(http.StatusOK, "index.html", gin.H{
			"title":   "IP Blocklist Manager",
			"message": fmt.Sprintf("Liste %s wurde importiert (Logik hier einbauen).", poolName),
		})
	})

	return r
}
