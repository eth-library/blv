package utils

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

func SetupLogging(logcfg LogConfig, ApplicationName string) *slog.Logger {
	// filename := ApplicationName + ".log"
	filename := ApplicationName + "_" + time.Now().Format("20060102_150405") + ".log"
	if logcfg.LogFolder == "" {
		cwd, _ := os.Getwd()
		logcfg.LogFolder = cwd + "/logs/"
		fmt.Println("no LogFolder provided")
	}
	// check, if logfile exists (eg after crash) and move it
	// set up regular log rotation with unix's logrotate
	// (e.g. https://medium.com/rahasak/golang-logging-with-unix-logrotate-41ec2672b439)
	if FileExists(logcfg.LogFolder + filename) {

		today := time.Now().Format("2006-01-02")
		newfilename := filename + "_" + today
		if FileExists(logcfg.LogFolder + newfilename) {
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
