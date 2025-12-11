# Blocklistenverwaltung


## Installation
Die Installation besteht aus drei Elementen:
  - das binary blv
  - die html-Dateien / Templates
  - die Datenbank

### systemd.service
Die ausführbare Datei blv sollte in ein bin-Verzeichnis verschoben werden, z.B. /usr/local/bin/blv

Die Datei blv.service muss entsprechend angepasst und in das Verzeichnis /usr/lib/systemd/system/ kopiert werden.

Beispiel blv.service:
```
[Unit]
Description=Blocklistenverwaltung Service
After=network.target
StartLimitIntervalSec=30

[Service]
User=blv
Group=blv
Environment="GIN_MODE=release"
ExecStart=/usr/local/bin/blv
ExecStop=/bin/kill -TERM $MAINPID
ExecReload=/usr/local/bin/blv -reset
Restart=on-failure
RestartSec=15

[Install]
WantedBy=multi-user.target
```

### logging
Die Datei blvlog muss angepasst und in das Verzeichnis /etc/logrotate.d kopiert werden.

Beispiel blvlog:
```
/var/log/blv/blv.log {
    su blv blv
    monthly
    missingok
    rotate 60
    create
    copytruncate
    dateext
    dateformat -%Y-%m
    dateyesterday
    delaycompress
}
```

## Konfiguration
Die Konfiguration wird unter `/etc/blv/conf.d/blv.yml` erwartet.
Beispiel config.yml:
```
dbPath: "/opt/blv/blv.db"
outputPath: "/etc/apache2/lists/blv-output/"
blocklistPath: "/etc/apache2/lists/blocklists/"
whitelistPath: "/etc/apache2/lists/whitelists/""
webfilesPath: "./html/"
webPort: 8080
basePath: 

LogConfig:
  LogLevel: Debug
  LogFolder: "./logs/"
```

## start/stop
Grundsätzlich wird die Applikation als Service via systemd gestartet. Sie lässt sich aber auch zum Test oder zum Anlegen oder Zurücksetzen der DB (init / reset) lokal starten.

Der Start/Stop als systemservice funktioniert wie bei allen Services:
```
systemctl blv start
systemctl blv stop
```