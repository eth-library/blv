package app

import (
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/SvenKethz/fairdb/internal/helpers"
	"gopkg.in/yaml.v3"
)

const (
	DefaultAdminUser     = "admin"
	DefaultAdminPassword = "1234"
)

var (
	_, ApplicationName = helpers.SeparateFileFromPath(os.Args[0])
	Config             ApplicationConfig
	LogIt              *slog.Logger

	// aus der optionalen envFile geladen (siehe ApplicationConfig.EnvFile);
	// AdminUser/AdminPassword schützen den Admin-Bereich (Basic Auth), APIToken
	// den Bearer-geschützten API-Zugriff. Ohne envFile bzw. ohne gesetzte Werte
	// gelten die Defaults; ein leerer APIToken deaktiviert das API.
	AdminUser     = DefaultAdminUser
	AdminPassword = DefaultAdminPassword
	APIToken      = ""
)

// configuration structures
// ========================

type ApplicationConfig struct {
	DbPath              string          `yaml:"dbPath"`
	WhitelistPath       string          `yaml:"whitelistPath"`
	BlocklistPath       string          `yaml:"blocklistPath"`
	BackupPath          string          `yaml:"backupPath"`
	WebfilesPath        string          `yaml:"webfilesPath"`
	BasePath            string          `yaml:"basePath"`
	WebPort             int             `yaml:"webPort"`
	TrustedProxies      []string        `yaml:"trustedProxies"`
	EnvFile             string          `yaml:"envFile"`
	ApacheReloadCommand []string        `yaml:"apacheReloadCommand"`
	Logcfg              LogConfig       `yaml:"LogConfig"`
	AutoBlock           AutoBlockConfig `yaml:"autoBlock"`
}

type LogConfig struct {
	LogLevel  string `yaml:"LogLevel"`
	LogFolder string `yaml:"LogFolder"`
}

// AutoBlockConfig steuert die serverweite Request-Ratenmessung (Basis für den
// automatischen Scraping-Schutz je Gruppe, siehe internal/autoblock). Leerer
// StatusURL deaktiviert das Feature komplett - es wird dann weder ein
// RateMonitor noch ein autoblock.Manager gestartet.
type AutoBlockConfig struct {
	StatusURL                string `yaml:"statusURL"`
	MeasureIntervalSeconds   int    `yaml:"measureIntervalSeconds"`
	MeasureWindowMinutes     int    `yaml:"measureWindowMinutes"`
	ThresholdVariancePercent int    `yaml:"thresholdVariancePercent"`
}

func Initialize(appName string, cfgPath string) {
	Config.Initialize(&cfgPath)
	LogIt = SetupLogging(Config.Logcfg, appName)
	loadCredentials(Config.EnvFile)
}

// loadCredentials liest AdminUser/AdminPassword/APIToken aus der in
// ApplicationConfig.EnvFile referenzierten .env-Datei. Fehlt die Datei oder
// einzelne Werte, bleiben die bereits gesetzten Defaults bestehen - Zugangsdaten
// gehören bewusst nicht in die YAML-Config.
func loadCredentials(envFile string) {
	if envFile == "" {
		LogIt.Info("keine envFile konfiguriert, verwende Default-Admin-Zugangsdaten und deaktiviertes API")
		return
	}
	values, err := helpers.ParseEnvFile(envFile)
	if err != nil {
		LogIt.Warn("konnte envFile nicht lesen, verwende Defaults: " + err.Error())
		return
	}
	if v, ok := values["ADMIN_USER"]; ok && v != "" {
		AdminUser = v
	}
	if v, ok := values["ADMIN_PASSWORD"]; ok && v != "" {
		AdminPassword = v
	}
	if v, ok := values["API_TOKEN"]; ok && v != "" {
		APIToken = v
	}
}

func (Config *ApplicationConfig) Initialize(ConfigPath *string) {
	// 1. set defaults
	Config.setDefaults()
	// 2. read config and run with defaults if not found
	file := helpers.GetCleanPath(*ConfigPath)
	yamlFile, err := os.ReadFile(file)
	if err != nil {
		fmt.Println("could not read config from " + file + ", will run with defaults.")
	} else {
		if err = yaml.Unmarshal(yamlFile, &Config); err != nil {
			log.Fatalln("ERROR parsing config", fmt.Sprint(err))
		}
	}

	Config.CheckConfig()
}

func (config *ApplicationConfig) setDefaults() {
	*config = ApplicationConfig{
		DbPath:              "./fairdb.db",
		WhitelistPath:       "./whitelists/",
		BlocklistPath:       "./blocklists/",
		BackupPath:          "./backup/",
		WebfilesPath:        "./html/",
		BasePath:            "",
		WebPort:             8080,
		TrustedProxies:      []string{"127.0.0.1"},
		EnvFile:             "",
		ApacheReloadCommand: []string{"sudo", "systemctl", "reload", "apache2"},
		Logcfg: LogConfig{
			LogLevel:  "INFO",
			LogFolder: "./logs/",
		},
		AutoBlock: AutoBlockConfig{
			StatusURL:                "",
			MeasureIntervalSeconds:   30,
			MeasureWindowMinutes:     10,
			ThresholdVariancePercent: 30,
		},
	}
}

// CheckConfig normalisiert Pfade und stellt sicher, dass alle vom Programm
// tatsächlich genutzten Verzeichnisse existieren. Läuft beim normalen Start
// unbeaufsichtigt (z. B. als systemd-Service), daher wird hier bewusst nicht
// interaktiv nachgefragt, sondern direkt angelegt.
func (c *ApplicationConfig) CheckConfig() {
	helpers.Checknaddtrailingslash(&c.Logcfg.LogFolder)
	helpers.Checknaddtrailingslash(&c.WhitelistPath)
	helpers.Checknaddtrailingslash(&c.BlocklistPath)
	helpers.Checknaddtrailingslash(&c.BackupPath)

	dirs := []string{c.Logcfg.LogFolder, c.WhitelistPath, c.BlocklistPath, c.BackupPath}
	for _, dir := range dirs {
		if err := helpers.EnsureDir(dir); err != nil {
			fmt.Println("Konnte Verzeichnis nicht anlegen: " + dir + ": " + err.Error())
		}
	}

	c.checkAutoBlockConfig()
}

// checkAutoBlockConfig warnt bei einer Fensterkonfiguration, die die
// Messung sinnlos macht (Intervall >= Fenster, siehe README), deaktiviert das
// Feature aber nicht - eine offensichtlich fehlerhafte, aber nicht
// katastrophale Konfiguration soll den unbeaufsichtigten Start nicht
// verhindern.
func (c *ApplicationConfig) checkAutoBlockConfig() {
	if c.AutoBlock.StatusURL == "" {
		return
	}
	windowSeconds := c.AutoBlock.MeasureWindowMinutes * 60
	if c.AutoBlock.MeasureIntervalSeconds <= 0 || windowSeconds <= 0 {
		fmt.Println("AutoBlock: measureIntervalSeconds und measureWindowMinutes müssen > 0 sein - AutoBlock bleibt ohne sinnvolle Messung")
		return
	}
	if windowSeconds < 3*c.AutoBlock.MeasureIntervalSeconds {
		fmt.Printf("AutoBlock: measureWindowMinutes (%dmin) ist kleiner als das 3-fache von measureIntervalSeconds (%ds) - die Messung deckt große Zeitanteile nicht ab, siehe README\n", c.AutoBlock.MeasureWindowMinutes, c.AutoBlock.MeasureIntervalSeconds)
	}
}

// further stuctures and functions
// ================================

type File2Parse struct {
	FileName string
}

// SetupLogging öffnet fairdb.log im konfigurierten LogFolder - fester Name,
// keine interne Zeitstempel-/Rotationslogik mehr. Rotation ist Aufgabe von
// logrotate (siehe README), genau wie bei Apache: fairDB öffnet die Datei im
// Append-Modus und schreibt einfach weiter, unabhängig davon, ob sie schon
// existiert (z. B. nach einem Neustart) oder gerade von logrotate rotiert
// wurde.
func SetupLogging(logcfg LogConfig, ApplicationName string) *slog.Logger {
	filename := ApplicationName + ".log"
	if logcfg.LogFolder == "" {
		cwd, _ := os.Getwd()
		logcfg.LogFolder = cwd + "/logs/"
		fmt.Println("no LogFolder provided")
	}
	logSource := false
	logLevel := new(slog.LevelVar)
	logFile, err := os.OpenFile(logcfg.LogFolder+filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: logLevel, AddSource: logSource}))
	// if logcfg.logLevel == "Debug" {
	logSource = true
	switch logcfg.LogLevel {
	case "Debug":
		logLevel.Set(slog.LevelDebug)
		logger.Debug("set log level to Debug")
	case "Info":
		logLevel.Set(slog.LevelInfo)
		logger.Info("set log level to Info")
	case "Warning":
		logLevel.Set(slog.LevelWarn)
		logger.Warn("set log level to Warn")
	case "Error":
		logLevel.Set(slog.LevelError)
		logger.Error("set log level to Error")
	}
	return logger
}
