# fairDB

fairDB verwaltet IP-Whitelists und -Blocklisten für Apache (`Require [not] ip`).
IP-Bereiche (CIDR) werden in **Pools** gepflegt, mehrere Pools werden zu
**Gruppen** zusammengefasst. Eine Gruppe hat einen Status (whitelisted /
blocked / inaktiv); wird sie aktiviert, schreibt fairDB die zugehörigen
`.conf`-Dateien in die konfigurierten Verzeichnisse und lädt Apache
automatisch neu.

Die SQLite-Datenbank ist die **alleinige Source of Truth**: fairDB liest nie
bestehende `.conf`-Dateien ein (weder eigene Exporte noch fremde) - Import
geschieht ausschließlich über den Datei-Upload im Admin-Bereich, der neue
Pools/Einträge in die DB einfügt.

## Installation
Die Installation besteht aus drei Elementen:
  - das Binary `fairdb` (Ergebnis von `go build .`)
  - die `html/`-Dateien (Templates + static)
  - die SQLite-Datenbank

### systemd.service
Die ausführbare Datei sollte in ein bin-Verzeichnis verschoben werden, z. B.
`/usr/local/bin/fairdb`.

Beispiel `fairdb.service` (unter `/usr/lib/systemd/system/`):
```
[Unit]
Description=fairDB Service
After=network.target
StartLimitIntervalSec=30

[Service]
User=fairdb
Group=fairdb
Environment="GIN_MODE=release"
ExecStart=/usr/local/bin/fairdb
ExecStop=/bin/kill -TERM $MAINPID
Restart=on-failure
RestartSec=15

[Install]
WantedBy=multi-user.target
```

### logging
Beispiel Logrotate-Konfiguration (unter `/etc/logrotate.d/fairdb`):
```
/var/log/fairdb/fairdb.log {
    su fairdb fairdb
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
Die Konfiguration wird unter `/etc/fairdb/conf.d/fairdb.yml` erwartet
(überschreibbar per `-c <pfad>`).

| Feld                  | Bedeutung                                                                 |
|------------------------|----------------------------------------------------------------------------|
| `dbPath`               | Pfad zur SQLite-Datenbank                                                 |
| `whitelistPath`        | Live-Verzeichnis, in das Whitelist-`.conf`-Dateien exportiert werden      |
| `blocklistPath`        | Live-Verzeichnis, in das Blocklist-`.conf`-Dateien exportiert werden      |
| `backupPath`           | Verzeichnis für datierte `.tgz`-Sicherungen vor jeder Aktivierung         |
| `webfilesPath`         | Verzeichnis mit `templates/` und `static/`                               |
| `basePath`             | Optionales URL-Präfix (leer = keins)                                     |
| `webPort`              | Port des Webservers                                                      |
| `trustedProxies`       | Liste vertrauenswürdiger Proxy-IPs (gin)                                 |
| `envFile`               | Pfad zur `.env`-Datei mit Zugangsdaten (siehe unten)                      |
| `apacheReloadCommand`  | Befehl inkl. Argumenten, der Apache neu lädt (Default: siehe unten)       |
| `LogConfig.LogLevel`   | `Debug`/`Info`/`Warning`/`Error`                                         |
| `LogConfig.LogFolder`  | Verzeichnis für die Log-Dateien                                          |

Alle Verzeichnisse (`whitelistPath`, `blocklistPath`, `backupPath`,
`LogConfig.LogFolder`) werden beim Start automatisch angelegt, falls sie noch
nicht existieren - ohne Rückfrage, damit der unbeaufsichtigte Start als
systemd-Service funktioniert.

Beispiel `fairdb.yml`:
```yaml
dbPath: "/opt/fairdb/fairdb.db"
whitelistPath: "/etc/apache2/lists/whitelists/"
blocklistPath: "/etc/apache2/lists/blocklists/"
backupPath: "/opt/fairdb/backup/"
webfilesPath: "/opt/fairdb/html/"
webPort: 8080
basePath:

envFile: "/etc/fairdb/fairdb.env"
apacheReloadCommand: ["sudo", "systemctl", "reload", "apache2"]

LogConfig:
  LogLevel: Debug
  LogFolder: "/opt/fairdb/logs/"
```

### Zugangsdaten (`envFile`)
Admin-Zugangsdaten und der API-Token stehen bewusst nicht in `fairdb.yml`,
sondern in einer separaten, per `envFile` referenzierten `.env`-Datei
(sinnvollerweise mit restriktiven Dateirechten, z. B. `chmod 600`, gehört dem
Service-User):
```
ADMIN_USER=admin
ADMIN_PASSWORD=<sicheres Passwort>
API_TOKEN=<zufälliger Token, z. B. openssl rand -hex 32>
```
Ohne `envFile` bzw. ohne gesetzte Werte gilt für den Admin-Bereich (Basic
Auth) der Default `admin`/`1234`. Ohne `API_TOKEN` ist das Bearer-API
deaktiviert (liefert `503`).

### Apache-Reload
Nach jeder Aktivierung - Gruppe blocken/whitelisten/(re-)aktivieren, globales
„Konfiguration aktivieren“ im Admin-Bereich, entsprechende API-Aufrufe -
führt fairDB den in `apacheReloadCommand` konfigurierten Befehl aus (als
Argument-Array, nicht über eine Shell) und schreibt Erfolg bzw. Fehler ins
Logfile. Schlägt der Reload fehl, bleiben die bereits geschriebenen
`.conf`-Dateien trotzdem gültig; der Fehler wird im Admin-Bereich bzw. in der
API-Antwort angezeigt.

Der Service-User benötigt dafür ein passendes NOPASSWD-sudoers-Recht, z. B.
in `/etc/sudoers.d/fairdb`:
```
fairdb ALL=(root) NOPASSWD: /usr/bin/systemctl reload apache2
```

### Backups
Vor jeder Aktivierung über den globalen „Konfiguration aktivieren“-Button
sichert fairDB den *aktuellen* Inhalt von `whitelistPath` und `blocklistPath`
je als datiertes `.tgz`-Archiv nach `backupPath` (z. B.
`whitelists-2026-08-26.tgz`, `blocklists-2026-08-26.tgz`), bevor die neuen
Dateien geschrieben werden. Mehrere Aktivierungen am selben Tag überschreiben
dasselbe Tages-Archiv.

## Gruppen und Pools
- Ein **Pool** ist eine benannte Liste von CIDR-Einträgen, jeder Eintrag hat
  einen eigenen Status (`w` whitelisted, `b` blocked, oder keiner).
- Eine **Gruppe** fasst mehrere Pools zusammen und hat selbst einen Status.
  Nur Gruppen erzeugen Live-Dateien: `whitelistPath/<gruppe>.conf` bzw.
  `blocklistPath/<gruppe>.conf`, intern gegliedert in einen Abschnitt pro
  Pool:
  ```
  #----
  # BLOCKLIST Airtel Networks Kenya Limited, KE
  #--------------------------------------------------------------------------------
  Require not ip  102.0.1.0/24
  Require not ip  102.0.10.0/24
  ```
- Ein manuell über das CIDR-Formular angelegter Pool bekommt automatisch eine
  gleichnamige Gruppe und ist damit sofort eigenständig aktivierbar.
- Ein per **Datei-Upload** importierter Pool bekommt **keine** automatische
  Gruppe - er bleibt ungruppiert, bis er explizit einer Gruppe zugewiesen
  wird (entweder nachträglich über „Pool zuweisen“ auf der Gruppenseite, oder
  direkt beim Hochladen über den Upload-Bereich auf der Gruppenseite selbst).
- Eine Gruppen-Aktion (blocken/whitelisten) setzt alle enthaltenen Pools und
  aktiviert die Gruppe sofort (Export + Apache-Reload). Konflikte (ein
  Eintrag ist in einem anderen Pool bereits geblockt) verhindern das
  Whitelisten und werden angezeigt.

## Web-Oberfläche
Öffentlich, ohne Login:
- `/` - IP-Adresse prüfen
- `/pools` - Poolübersicht
- `/groups` - Gruppenübersicht

Admin-Bereich unter `/admin` (Basic Auth):
- `/admin/pools/<pool>` - CIDRs hinzufügen/löschen/whitelisten/blocken
- `/admin/pools/upload` - Datei hochladen → neuer/ergänzter Pool (ungruppiert)
- `/admin/groups` - Gruppen anlegen/auflisten
- `/admin/groups/<gruppe>` - Pools zuweisen, Datei direkt in die Gruppe
  hochladen, Gruppe blocken/whitelisten/aktivieren/löschen
- `/admin/activate` - zeigt die aktuell exportierten Whitelist-/
  Blocklist-Dateien und den Button zum (Neu-)Aktivieren der gesamten
  Konfiguration (Export aller Gruppen + Apache-Reload)

### Bulk-Upload
Viele Dateien lassen sich einfach per Schleife direkt in eine Gruppe laden
(Poolname = Dateiname ohne Endung; der Endpoint antwortet immer mit `303`,
Erfolg/Fehler steckt im `Location`-Query-Parameter):
```bash
for f in /pfad/zu/dateien/*.conf; do
  loc=$(curl -s -o /dev/null -w '%{redirect_url}' \
    -u "$ADMIN_USER:$ADMIN_PASSWORD" \
    -X POST "$BASE_URL/admin/groups/$GROUP/uploadPool" \
    -F "file=@$f")
  [[ "$loc" == *"error="* ]] && echo "FEHLER: $f -> $loc" || echo "OK: $f"
done
```
Optional lässt sich mit `-F "zielStatus=b"` bzw. `-F "zielStatus=w"` der
Status jedes importierten Eintrags direkt mitgeben.

## API
Ein Bearer-Auth-geschütztes JSON-API steht unter `/api/v1` bereit (Token
siehe `envFile` oben; ohne konfigurierten Token antwortet das API mit `503`).
Das API arbeitet ausschließlich auf **Gruppen-Ebene** - einzelne Pools werden
bewusst nur über die WebUI verwaltet:
```
GET  /api/v1/groups/:name              Status abfragen (blocked/whitelisted/inaktiv)
POST /api/v1/groups/:name/block        Gruppe blocken (Export + Reload)
POST /api/v1/groups/:name/whitelist    Gruppe whitelisten (Export + Reload)
POST /api/v1/groups/:name/deactivate   Gruppe deaktivieren (weder w noch b, Export + Reload)
```
Eine Deaktivierung entfernt die Gruppe aus beiden Dateien (Whitelist und
Blocklist) - taucht danach in keiner der beiden mehr auf.
Beispiel:
```bash
curl -H "Authorization: Bearer <API_TOKEN>" https://.../api/v1/groups/Scraping-Netz
curl -H "Authorization: Bearer <API_TOKEN>" -X POST https://.../api/v1/groups/Scraping-Netz/block
```

## start/stop
Grundsätzlich wird die Applikation als Service via systemd gestartet. Sie
lässt sich aber auch lokal starten, z. B. zum Test oder um eine frische,
leere Datenbank anzulegen:
```
fairdb -c ./conf.d/fairdb.yml -init   # legt eine neue, leere DB an
fairdb -c ./conf.d/fairdb.yml         # normaler Start (inkl. Schema-Migration)
```
Ein `-reset` (DB aus bestehenden Dateien neu einlesen) gibt es nicht - die
Datenbank ist die alleinige Source of Truth, fairDB liest nie eigene oder
fremde Exporte zurück.

Start/Stop als systemd-Service:
```
systemctl start fairdb
systemctl stop fairdb
```
