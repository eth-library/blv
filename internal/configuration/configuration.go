package app

import (
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/SvenKethz/fairdb/internal/helpers"
	"gopkg.in/yaml.v3"
)

var (
	_, ApplicationName = helpers.SeparateFileFromPath(os.Args[0])
	Config             ApplicationConfig
	LogIt              *slog.Logger
)

// configuration structures
// ========================

type ApplicationConfig struct {
	DbPath         string    `yaml:"dbPath"`
	ListPath       string    `yaml:"listPath"`
	OutputPath     string    `yaml:"outputPath"`
	BackupPath     string    `yaml:"backupPath"`
	WebfilesPath   string    `yaml:"webfilesPath"`
	BasePath       string    `yaml:"basePath"`
	WebPort        int       `yaml:"webPort"`
	TrustedProxies []string  `yaml:"trustedProxies"`
	DateLayout          string    `yaml:"DateLayout"`
	OutputFolder        string    `yaml:"OutputFolder"`
	DefaultFile2analyze string    `yaml:"DefaultLog2analyze"`
	LogType             string    `yaml:"LogType"`
	LogFormat           string    `yaml:"LogFormat"`
	Logcfg         LogConfig `yaml:"LogConfig"`
}

type LogConfig struct {
	LogLevel  string `yaml:"LogLevel"`
	LogFolder string `yaml:"LogFolder"`
}

func Initialize(appName string, cfgPath string) {
	Config.Initialize(&cfgPath)
	LogIt = SetupLogging(Config.Logcfg, appName)
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
		DbPath:         "./fairdb.db",
		ListPath:       "./",
		BackupPath:     "./backup/",
		OutputPath:     "./output/",
		WebfilesPath:   "./html/",
		BasePath:       "",
		WebPort:        8080,
		TrustedProxies: []string{"127.0.0.1"},
		DateLayout:   "02/Jan/2006:15:04:05 -0700",
		OutputFolder: "./output/",
		LogType:      "apache",
		LogFormat:    "%h %l %u %t \"%r\" %>s %O \"%{Referer}i\" \"%{User-Agent}i\"",
		Logcfg: LogConfig{
			LogLevel:  "INFO",
			LogFolder: "./logs/",
		},
	}
}

func (c *ApplicationConfig) CheckConfig() {
	// TODO: hier könnte noch ein DateLayoutCheck rein
	helpers.Checknaddtrailingslash(&c.Logcfg.LogFolder)
	helpers.Checknaddtrailingslash(&c.BackupPath)
	helpers.Checknaddtrailingslash(&c.OutputPath)
	helpers.Checknaddtrailingslash(&c.ListPath)
	// check if the log folder exists
	if !helpers.CheckIfDir(c.Logcfg.LogFolder) {
		helpers.ToBeCreated(c.Logcfg.LogFolder)
	}
	helpers.Checknaddtrailingslash(&c.OutputFolder)
	// check if the output folder exists
	if !helpers.CheckIfDir(c.OutputFolder) {
		helpers.ToBeCreated(c.OutputFolder)
	}
}

// further stuctures and functions
// ================================

type File2Parse struct {
	FileName string
}

func SetupLogging(logcfg LogConfig, ApplicationName string) *slog.Logger {
	filename := ApplicationName
	if logcfg.LogLevel == "Debug" {
		filename += ".log"
	} else {
		filename += "_" + time.Now().Format("20060102_150405") + ".log"
	}
	if logcfg.LogFolder == "" {
		cwd, _ := os.Getwd()
		logcfg.LogFolder = cwd + "/logs/"
		fmt.Println("no LogFolder provided")
	}
	// check, if logfile exists (eg after crash) and move it
	// set up regular log rotation with unix's logrotate
	// (e.g. https://medium.com/rahasak/golang-logging-with-unix-logrotate-41ec2672b439)
	if helpers.FileExists(logcfg.LogFolder + filename) {

		today := time.Now().Format("2006-01-02")
		newfilename := filename + "_" + today
		if helpers.FileExists(logcfg.LogFolder + newfilename) {
			counter := 0
			logfiles, err := os.ReadDir(logcfg.LogFolder)
			if err != nil {
				fmt.Println("ERROR", fmt.Sprint(err))
			}
			for _, file := range logfiles {
				if strings.HasPrefix(file.Name(), newfilename) {
					counter++
				}
			}
			newfilename = newfilename + "." + fmt.Sprint(counter)
		}
		fmt.Println("logfile " + logcfg.LogFolder + filename + " exists,")
		fmt.Println("will move it to " + logcfg.LogFolder + newfilename)
		err := os.Rename(logcfg.LogFolder+filename, logcfg.LogFolder+newfilename)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

	}
	logSource := false
	logLevel := new(slog.LevelVar)
	logFile, err := os.OpenFile(logcfg.LogFolder+filename, os.O_CREATE|os.O_WRONLY, 0o666)
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
