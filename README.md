# fairDB

fairDB verwaltet IP-Whitelists und -Blocklisten für Apache (`Require [not] ip`).
IP-Bereiche (CIDR) werden in **Pools** gepflegt, mehrere Pools werden zu
**Gruppen** zusammengefasst. Eine Gruppe hat einen Status (whitelisted /
blocked / inaktiv); wird sie aktiviert, schreibt fairDB die zugehörigen
`.conf`-Dateien in die konfigurierten Verzeichnisse und lädt Apache
automatisch neu.

Die SQLite-Datenbank ist die **Source of Truth für den laufenden Betrieb**:
neue Pools/Einträge entstehen ausschließlich über das Web-UI/API oder den
Datei-Upload im Admin-Bereich. Beim **Start** gleicht fairDB einmalig den in
der DB gespeicherten Status jedes Eintrags mit dem tatsächlichen Inhalt von
`whitelistPath`/`blocklistPath` ab (siehe unten) - in diesem Moment sind die
vorhandenen `.conf`-Dateien maßgeblich, damit die DB nicht an Apache
vorbeidriftet (z. B. nach einem manuellen Eingriff im Apache-Verzeichnis oder
einer aus einer älteren Sicherung wiederhergestellten DB).

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

Reloads laufen serialisiert (nie zwei `systemctl reload`-Prozesse gleichzeitig)
und sind auf 90 Sekunden begrenzt - ein hängender/sehr langsamer Reload
blockiert damit nicht mehr unbegrenzt den auslösenden Request (WebUI oder
API), sondern liefert nach spätestens 90s einen Timeout-Fehler zurück.

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

## AutoBlock (automatischer Scraping-Schutz)
Pro Gruppe wählbar zwischen zwei Modi: **Schwellwert-basiert** (reagiert auf
eine gemessene serverweite Requestrate, siehe unten) und **Scraper's Pain**
(blockt fortlaufend einen zufälligen Teil der Gruppen-Pools, ohne jede
Ratenmessung - siehe eigener Abschnitt weiter unten).

### Schwellwert-basiert
fairDB sitzt nicht im Request-Pfad (es erzeugt nur `Require [not] ip`-
Direktiven, die Apache selbst auswertet) und hat daher keine eigene Sicht auf
tatsächliche Requests. Als Datenquelle pollt fairDB stattdessen periodisch
Apaches `mod_status` (`server-status?auto`) und berechnet daraus die
serverweite (nicht pro Gruppe/IP) Requestrate:
`RPS ≈ (TotalAccesses[n] − TotalAccesses[n−1]) / Δt`. Jede Gruppe mit
aktiviertem Schwellwert-Modus entscheidet unabhängig anhand ihres eigenen,
randomisierten Schwellwerts, ob *sie* sich deswegen selbst temporär blockt -
das Ziel ist ausdrücklich **kein** Schutz vor kurzen Peaks, sondern gegen
länger andauerndes Scraping.

Globale Einstellungen in `fairdb.yml` (Abschnitt `autoBlock`):
```yaml
autoBlock:
  statusURL: "http://127.0.0.1/server-status?auto"
  measureIntervalSeconds: 30   # y: wie oft neu gemessen/geprüft wird
  measureWindowMinutes: 10     # x: gleitendes Zeitfenster des Durchschnitts
  thresholdVariancePercent: 30 # +- Zufallsanteil auf den je Gruppe konfigurierten Schwellwert
  scraperPainMaxPools: 500     # Obergrenze für Scraper's Pain UND max. Gruppengröße für den Schwellwert-Modus, siehe unten
```
Ein leerer `statusURL` deaktiviert nur den Schwellwert-Modus (kein Overhead,
die Option ist im WebUI ausgegraut) - Scraper's Pain bleibt davon unberührt
verfügbar, da es keine Ratenmessung braucht.

Zeigt `statusURL` per HTTPS auf `localhost` oder eine Loopback-Adresse
(127.0.0.0/8, `::1`) - in der Praxis häufig mit einem nicht validen/
selbstsignierten Zertifikat, da diese Verbindung nie das System verlässt -,
deaktiviert fairDB die TLS-Zertifikatsprüfung für diese Anfrage (wie
`curl -k`) und loggt das einmalig als Warnung. Für jeden anderen Host bleibt
die normale Zertifikatsprüfung aktiv.

**Verhältnis von Messzeitraum (x) und Messintervall (y):** Bei jedem Tick
(alle y Sekunden) wird der gleitende x-Minuten-Durchschnitt neu berechnet und
sofort geprüft - y bestimmt also, wie schnell reagiert wird, x bestimmt, wie
unempfindlich der Durchschnitt gegenüber kurzen Peaks ist (ein kurzer
Ausschlag verschiebt einen 10-Minuten-Schnitt kaum, einen 2-Minuten-Schnitt
aber deutlich). Damit über die Laufzeit genug Messpunkte in jedem Fenster
liegen, sollte x mindestens das 3-fache von y sein (z. B. 30s/10min); fairDB
loggt beim Start eine Warnung, wenn das nicht der Fall ist.

**Verhalten, wenn `server-status?auto` nicht antwortet:** Fail-open, nie
fail-closed. Ist der letzte erfolgreiche Poll älter als 2×Intervall oder
liegen noch nicht genug Daten fürs Fenster vor, gilt die Messung als
"unhealthy" - keine Gruppe wird auf Basis fehlender/veralteter Daten
geblockt. Der Zustand wird auf der Gruppen-Detailseite angezeigt und
unterscheidet dabei zwei Fälle: "server-status nicht erreichbar" (echtes
Problem, server-status antwortet nicht) und "Datenbasis wird aufgebaut: X von
Y Minuten erfasst" (normale Aufwärmphase - direkt nach dem Start bzw. nach
einem Zähler-Reset von Apache dauert es bis zu `measureWindowMinutes` Minuten,
bis genug Daten für einen verlässlichen Durchschnitt vorliegen).

**Pro Gruppe** (Gruppen-Detailseite im Admin-Bereich): Schwellwert-Modus
aktivieren mit Schwellwert (Ø req/s, ganzzahlig eingegeben, wird bei jeder
Prüfung zusätzlich um `thresholdVariancePercent` verzerrt) und einer
Blockdauer-Spanne (min/max in Minuten, bei Auslösung wird eine zufällige
Dauer daraus gewählt). Intern (DB, Auswertung) wird die Blockdauer
weiterhin in Sekunden gehalten - die Umrechnung erfolgt ausschließlich an
der WebUI-Formulargrenze.

Für Gruppen mit mehr Pools als `scraperPainMaxPools` (Default 500) ist der
Schwellwert-Modus **nicht aktivierbar** (Option im WebUI ausgegraut, Server
lehnt einen direkten API-/Formular-Versuch ebenfalls ab): er blockt bei
Auslösung immer die komplette Gruppe, und eine so große Blockliste würde den
in der Praxis beobachteten sehr langen Apache-Reload auslösen (Apache parst
`Require [not] ip`-Direktiven beim Reload, nicht pro Request - die
Reload-Dauer hängt also an der Zeilenzahl). Für solche Gruppen bleibt nur
Scraper's Pain nutzbar, das die Blockmenge selbst begrenzt (siehe unten).

Egal welcher Modus: es darf **immer nur eine einzige Gruppe gleichzeitig**
AutoBlock aktiviert haben. Im Schwellwert-Modus, weil die Ratenmessung
serverweit ist und nicht zwischen Gruppen unterscheidet - sonst würden
mehrere Gruppen unabhängig auf dasselbe Lastsignal reagieren; dieselbe Regel
gilt bewusst auch für Scraper's Pain, um die Zahl gleichzeitig laufender
Zufallszyklen und damit einhergehender Apache-Reloads zu begrenzen. Ist
AutoBlock bereits für eine andere Gruppe aktiviert, ist das Formular auf der
Gruppenseite ausgegraut; erst nach dem Deaktivieren dort lässt sich AutoBlock
für eine andere Gruppe aktivieren. Das wird zusätzlich auf DB-Ebene hart
erzwungen (`idx_group_autoblock_singleton_enabled`).

Eine explizit **whitelisted** Gruppe wird nie automatisch geblockt. Läuft eine
manuelle Whitelist-/Block-/Deaktivierungs-Aktion (WebUI oder API), wird ein
gerade laufender AutoBlock sofort beendet, damit sein automatischer Revert
die manuelle Entscheidung später nicht überschreibt. Beim Deaktivieren von
AutoBlock für eine gerade automatisch geblockte Gruppe wird sofort
zurückgesetzt (Export + Apache-Reload), damit die Gruppe nicht dauerhaft
geblockt bleibt.

### Scraper's Pain
Statt auf eine Ratenmessung zu reagieren, blockiert dieser Modus fortlaufend
einen zufälligen Teil der Pools einer Gruppe: bei jedem Zyklus wird ein
zufälliger Anteil der Pools ausgewählt (**Umfang**, 1-100 %, bezogen auf die
Pools der Gruppe zum jeweiligen Zeitpunkt) und für eine zufällige Dauer aus
der (mit dem Schwellwert-Modus geteilten) Blockdauer-Spanne min/max
geblockt. Läuft die Dauer ab, wird sofort eine neue Auswahl im gleichen
Umfang und eine neue zufällige Dauer gewürfelt - ohne Pause dazwischen -, bis
der Modus wieder deaktiviert wird. Anders als der Schwellwert-Modus
funktioniert Scraper's Pain unabhängig von einer konfigurierten `statusURL`.

Individuell **whitelistete** Pools (Pool-Status `w`) werden nie in die
Zufallsauswahl einbezogen, auch wenn sie formal zur Gruppe gehören - analog
dazu, dass eine whitelistete Gruppe im Schwellwert-Modus nie automatisch
geblockt wird. Der Gruppenstatus selbst bleibt während Scraper's Pain
durchgängig inaktiv (`""`); nur einzelne Pools wechseln zwischen geblockt und
inaktiv, die Gruppen-Detailseite markiert das farbig in der Pools-Tabelle
(rot geblockt, grau inaktiv) und zeigt die aktuelle Blockanzahl im
Status-Banner ganz oben.

Sehr kurze Blockdauern (wenige Sekunden/Minuten) führen zu entsprechend
häufigen Export- und Apache-Reload-Vorgängen - für den produktiven Einsatz
empfiehlt sich eine Blockdauer-Spanne im Minuten- bis Stunden-Bereich.

**`scraperPainMaxPools`** deckelt zusätzlich, wie viele Pools ein einzelner
Zyklus höchstens gleichzeitig blockt - unabhängig vom konfigurierten Umfang
in %. Grund: Apache parst `Require [not] ip`-Direktiven beim Reload (nicht
pro Request), die Reload-Dauer hängt also an der Zahl der Direktiven in der
exportierten `.conf`-Datei, nicht an der Gruppengröße selbst. Bei sehr
großen Gruppen (mehrere tausend Pools) würde ein hoher Umfang-Prozentsatz
sonst bei jedem Zyklus eine entsprechend große, langsam zu ladende Blockliste
erzeugen. `0` oder ein negativer Wert deaktiviert die Grenze. Der
Schwellwert-Modus ist von dieser Grenze **nicht** betroffen - er blockt bei
Auslösung bewusst immer die komplette Gruppe, um den Scraping-Schutz nicht
zu schwächen.

## API
Ein Bearer-Auth-geschütztes JSON-API steht unter `/api/v1` bereit (Token
siehe `envFile` oben; ohne konfigurierten Token antwortet das API mit `503`).
Das API arbeitet auf **Gruppen-Ebene** - einzelne Pool-Einträge (IP-Ebene,
z. B. eine einzelne IP whitelisten) bleiben bewusst der WebUI vorbehalten:
```
GET  /api/v1/groups/:name              Status abfragen (blocked/whitelisted/inaktiv)
POST /api/v1/groups/:name/block        Gruppe blocken (Export + Reload)
POST /api/v1/groups/:name/whitelist    Gruppe whitelisten (Export + Reload)
POST /api/v1/groups/:name/deactivate   Gruppe deaktivieren (weder w noch b, Export + Reload)
POST /api/v1/groups/:name/pools        .conf-Datei als Pool importieren und der Gruppe zuweisen
```
Eine Deaktivierung entfernt die Gruppe aus beiden Dateien (Whitelist und
Blocklist) - taucht danach in keiner der beiden mehr auf.

`block`/`whitelist`/`deactivate` sind idempotent: hat die Gruppe (inkl. aller
enthaltenen Pools) bereits exakt den Zielstatus, wird kein erneuter
Export/Apache-Reload ausgelöst - der Aufruf kehrt sofort zurück. Das macht
wiederholte Aufrufe billig, z. B. aus einem Skript, das eine Gruppe blockt,
solange eine Rate-Schwelle überschritten bleibt. Jede der drei Aktionen
schaltet außerdem ein evtl. für die Gruppe laufendes AutoBlock komplett ab
(nicht nur den aktuellen Block) - AutoBlock und manuelle Aktionen schließen
sich gegenseitig aus, siehe Abschnitt AutoBlock oben.

**`POST /api/v1/groups/:name/pools`** entspricht dem WebUI-Formular "Datei in
diese Gruppe hochladen": `multipart/form-data` mit Feld `file` (die
`.conf`/`.txt`-Datei, Format wie beim WebUI-Upload - `Require [not] ip`-
Zeilen bzw. einfache IP-Listen). Der Poolname ergibt sich aus dem
Dateinamen ohne Endung, lässt sich aber über das optionale Feld `poolName`
überschreiben. Optionales Feld `status` (`w`, `b` oder leer/weggelassen für
inaktiv) setzt den Status aller importierten Einträge. Existiert der
Poolname bereits (auch in einer anderen Gruppe), werden die neuen Einträge
ergänzt und der **gesamte** Pool dieser Gruppe zugewiesen (verschiebt ihn
also, statt ihn zu duplizieren). Löst bewusst **keinen** Export/Apache-Reload
aus - dafür anschließend `block`/`whitelist`/`deactivate` aufrufen.

Beispiele:
```bash
curl -H "Authorization: Bearer <API_TOKEN>" https://.../api/v1/groups/Scraping-Netz
curl -H "Authorization: Bearer <API_TOKEN>" -X POST https://.../api/v1/groups/Scraping-Netz/block

# Pool aus einer .conf-Datei importieren und der Gruppe zuweisen (Status "b" = blocked)
curl -H "Authorization: Bearer <API_TOKEN>" \
     -F "file=@./scraper_netzwerke/NEXT-TV_SHPK.conf" \
     -F "status=b" \
     -X POST https://.../api/v1/groups/Scraping-Netz/pools
```

## start/stop
Grundsätzlich wird die Applikation als Service via systemd gestartet. Sie
lässt sich aber auch lokal starten, z. B. zum Test oder um eine frische,
leere Datenbank anzulegen:
```
fairdb -c ./conf.d/fairdb.yml -init   # legt eine neue, leere DB an
fairdb -c ./conf.d/fairdb.yml         # normaler Start (inkl. Schema-Migration + Apache-Abgleich)
```

### Abgleich mit Apache beim Start
Bei jedem normalen Start (nicht bei `-init`) gleicht fairDB den Status jedes
bereits vorhandenen Pool-Eintrags mit dem tatsächlichen Inhalt von
`whitelistPath`/`blocklistPath` ab, bevor der Webserver startet:

- Berücksichtigt werden ausschließlich `.conf`-Dateien, deren Name (ohne
  Endung) exakt einem **bekannten** Pool- oder Gruppennamen aus der DB
  entspricht. Alle anderen Dateien werden ignoriert und nicht angefasst.
- Für Gruppen-Dateien (mit `#----`-Abschnitten je Pool, siehe
  [Gruppen und Pools](#gruppen-und-pools)) wird jeder Abschnitt seinem im
  Header genannten Pool zugeordnet; für eine flache, nicht segmentierte Datei
  gilt ihr Dateiname als Poolname.
- Für jeden vorhandenen Eintrag gilt die jeweilige Datei in diesem Moment als
  Source of Truth: taucht sein CIDR in einer bekannten Whitelist-Datei auf,
  wird der Status auf `w` gesetzt, in einer Blocklist-Datei auf `b`. Fehlt er
  in beiden (auch wenn die Datei komplett fehlt), wird der Status auf ``
  zurückgesetzt. Der Gruppenstatus wird anschließend aus dem neuen
  Pool-Status neu berechnet.
- Es werden dabei **nie** neue Pools/Einträge angelegt und **nie** in
  `whitelistPath`/`blocklistPath` geschrieben (auch keine Status- oder
  Marker-Datei) - reine Leseoperation.

Start/Stop als systemd-Service:
```
systemctl start fairdb
systemctl stop fairdb
```
