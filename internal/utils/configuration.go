package utils

import (
	"fmt"
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

// configuration structures
// ========================

type ApplicationConfig struct {
	dbPath          string    `yaml:"dbPath"`
	webPort        string    `yaml:"webPort"`
	Logcfg              LogConfig `yaml:"LogConfig"`
}

type LogConfig struct {
	LogLevel  string `yaml:"LogLevel"`
	LogFolder string `yaml:"LogFolder"`
}

// ToDo: standardize logfile layout according to https://httpd.apache.org/docs/2.4/mod/mod_log_config.html#formats

func (config *ApplicationConfig) Initialize(configPath *string) {
	// 1. set defaults
	config.setDefaults()
	// 2. read config and run with defaults if not found
	file := GetCleanPath(*configPath)
	yamlFile, err := os.ReadFile(file)
	if err != nil {
		fmt.Println("could not read config from " + file + ", will run with defaults.")
	} else {
		if err = yaml.Unmarshal(yamlFile, &config); err != nil {
			log.Fatalln("ERROR parsing config", fmt.Sprint(err))
		}
	}

	config.CheckConfig()
}

func (config *ApplicationConfig) setDefaults() {
	*config = ApplicationConfig{
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
	checknaddtrailingslash(&c.Logcfg.LogFolder)
	// check if the log folder exists
	if !CheckIfDir(c.Logcfg.LogFolder) {
		ToBeCreated(c.Logcfg.LogFolder)
	}
	checknaddtrailingslash(&c.OutputFolder)
	// check if the output folder exists
	if !CheckIfDir(c.OutputFolder) {
		ToBeCreated(c.OutputFolder)
	}
}

// further stuctures and functions
// ================================

type File2Parse struct {
	FileName string
}
