# Guida utente di Dialga

| | |
|---|---|
| **Data** | 2026-10-16 |
| **Autore** | Fabio Pellizzaro (con Claude Code) |
| **Versione documentata** | `0.x`, integrazione `feature/v0.2-wave1` (commit `43804bf`) |
| **Destinatari** | Sviluppatori e amministratori DLOOP che importano o esportano database SQL Server su Amazon RDS |
| **Piattaforma** | Linux x86_64 (app desktop e CLI); solo CLI su Windows e macOS |

> **Sintesi**
> - Dialga importa ed esporta database SQL Server su istanze **Amazon RDS** passando per **S3** (backup e restore nativi `rds_*`). Ha un'**app desktop** e una **CLI** (`dialga`), che usano lo stesso engine.
> - Oggi fa: **controlli preliminari** (`check`), **task nativi** (`status`, `wait`), **tunnel SSM integrato** (`tunnel`), **elenco dei database** (`databases`) e **ispezione dei `.bak`** prima dell'upload (`inspect`). Tutto in **sola lettura** su AWS e su SQL Server.
> - Import ed export veri e propri, configurazione guidata e coda delle attività sono **in arrivo** (segnati con `> In arrivo` in questa guida).
> - La password SQL **non è mai nel file di configurazione**: il file dice solo da dove leggerla.

## 1. Cosa c'è oggi

| Funzione | CLI | App desktop | Stato |
|---|---|---|---|
| Controlli preliminari di un target | `dialga check` | Instances → *Run check* | Disponibile |
| Task nativi di backup e restore | `dialga status`, `dialga wait` | Instances → *Show tasks* | Disponibile |
| Tunnel SSM verso l'istanza | `dialga tunnel`, `connect.mode: tunnel` | Usato da sé in modalità `tunnel` | Disponibile |
| Database dell'istanza | `dialga databases` | Databases | Disponibile |
| Panoramica dell'istanza (stato, spazio, DB, task attivi) | — | Instances, nella scheda | Disponibile |
| Ispezione dei `.bak` | `dialga inspect` | — (passo *Verifica* del wizard Importa) | CLI disponibile, GUI in arrivo |
| Import, export, upload, download | — | Import, Export | In arrivo |
| Configurazione guidata, diagnosi dei prerequisiti | — | — | In arrivo |
| Coda e storico delle attività | — | Activity | In arrivo |

## 2. Installazione

### 2.1 Scaricare e verificare

I pacchetti si scaricano dai **Downloads** del repository Bitbucket `decisyon/dialga`:

| File | Per |
|---|---|
| `Dialga_X.Y.Z_amd64.deb` | Debian, Ubuntu: app desktop + CLI |
| `Dialga-X.Y.Z-1.x86_64.rpm` | Fedora e altre distribuzioni rpm: app desktop + CLI |
| `dialga_X.Y.Z_<os>_<arch>.tar.gz` / `.zip` | Solo CLI (Linux, macOS, Windows) |
| `SHA256SUMS` | Checksum di tutti i file |

Nella cartella dei file scaricati:

```bash
sha256sum -c --ignore-missing SHA256SUMS
```

I pacchetti **non sono firmati** fino alla v2.0: il checksum protegge solo da un download danneggiato.

### 2.2 Installare

| Distribuzione | Installazione o aggiornamento | Rimozione |
|---|---|---|
| Debian, Ubuntu | `sudo apt install ./Dialga_X.Y.Z_amd64.deb` | `sudo apt remove dialga` |
| Fedora | `sudo dnf install ./Dialga-X.Y.Z-1.x86_64.rpm` | `sudo dnf remove dialga` |

- Usa `apt`/`dnf`, non `dpkg -i`/`rpm -i`: installano anche WebKitGTK 4.1 e GTK 3.
- Se `dnf` rifiuta un pacchetto locale senza firma (`localpkg_gpgcheck=1`), aggiungi `--nogpgcheck` per questo file, dopo aver verificato `SHA256SUMS`.
- **Distribuzioni minime** (glibc 2.36 o più recente): Debian 12+, Ubuntu 24.04+, Fedora 37+. Ubuntu 22.04: TODO: confermare. Solo x86_64.
- Non c'è aggiornamento automatico: le nuove versioni si installano allo stesso modo, sopra la precedente.

Cosa viene installato:

| Percorso | Contenuto |
|---|---|
| `/usr/bin/dialga` | La CLI (e l'engine dell'app desktop) |
| `/usr/bin/dialga-desktop` | L'app desktop, voce di menu **Dialga** |
| `/usr/share/doc/dialga/` | `config.example.yaml`, `iam-policy.json`, `CHANGELOG.md` |

**Solo CLI**: estrai l'archivio e metti `dialga` nel `PATH` (per esempio `~/.local/bin`). Controlla quale binario viene usato con `command -v dialga` e `dialga version`.

Completamento della shell (facoltativo):

```bash
mkdir -p ~/.local/share/bash-completion/completions
dialga completion bash > ~/.local/share/bash-completion/completions/dialga
```

### 2.3 Primo avvio

1. Prepara i prerequisiti (§3) e il file di configurazione (§4).
2. Verifica da terminale: `dialga config validate`, poi `dialga check`.
3. Avvia l'app dal menu (**Dialga**) o da terminale con `dialga-desktop`. Per i target con `sql.password: prompt` avviala da terminale con la password nell'ambiente (§4.3).

Dopo la rimozione restano la configurazione (`~/.config/dialga/`) e i dati della finestra (`~/.local/share/com.decisyon.dialga/`).

> In arrivo: configurazione guidata al primo avvio (`dialga config init`, `dialga doctor`, aggiunta di un'istanza dalla GUI).

## 3. Prerequisiti

| Cosa | Serve per | Come verificarlo |
|---|---|---|
| **Profilo AWS** (`~/.aws/config`, chiavi o SSO) nell'account del target | Tutti i comandi | `aws sts get-caller-identity --profile <profilo>` |
| **Permessi IAM** della policy minima | Tutti i comandi | `/usr/share/doc/dialga/iam-policy.json`, spiegata in `docs/iam-policy.md` |
| **`session-manager-plugin`**, versione **successiva a 1.2.497.0** | Tunnel integrato (`connect.mode: tunnel`, `dialga tunnel`) | `session-manager-plugin --version` |
| **Istanza bridge** SSM in esecuzione nel VPC dell'istanza RDS (tag `Name=ssm-bridge`) | Tunnel integrato | La cerca Dialga; se manca lo dice (§7.3) |
| **podman** (consigliato) o **docker** | `dialga inspect` | `podman --version` / `docker info` |
| Immagine **`mcr.microsoft.com/mssql/server:2022-latest`** (circa 1,5 GB) | `dialga inspect` | `podman image exists mcr.microsoft.com/mssql/server:2022-latest` |
| **Login SQL Server** dell'istanza (utente e password) | Login SQL, task, database | Te lo dà l'amministratore dell'istanza |

Installazione del plugin su Fedora/RHEL:

```bash
sudo dnf install https://s3.amazonaws.com/session-manager-downloads/plugin/latest/linux_64bit/session-manager-plugin.rpm
```

Altre distribuzioni: documentazione AWS *Install the Session Manager plugin for the AWS CLI*.

L'immagine SQL Server per `inspect` non viene mai scaricata da Dialga. Scaricala una volta:

```bash
podman pull mcr.microsoft.com/mssql/server:2022-latest
```

## 4. Configurazione

### 4.1 Dove sta

Un solo file YAML, condiviso da CLI e app desktop:

```bash
$ dialga config path
/home/utente/.config/dialga/config.yaml
```

Tutti i comandi accettano `--config <file>` per usarne un altro. Il file non lo crea nessuno: parti dall'esempio.

```bash
cfg=$(dialga config path)
[ -e "$cfg" ] || install -D -m 600 /usr/share/doc/dialga/config.example.yaml "$cfg"
"${EDITOR:-vi}" "$cfg"
```

L'app desktop rilegge il file a ogni aggiornamento della schermata: non serve riavviarla.

### 4.2 Schema

```yaml
apiVersion: v1
targets:
  dev-sql:                          # target name: lower case letters, digits and "-"
    account_id: "123456789012"      # account guard: required
    region: eu-west-1
    aws_profile: default
    instance: my-sqlserver          # RDS DB instance identifier (not the endpoint)
    bucket: my-sqlserver-backups    # bucket of the SQLSERVER_BACKUP_RESTORE option
    prefixes: { import: import, export: export }
    protected: false
    max_tasks: 2
    sql: { user: admin, password: prompt }
    connect: { mode: tunnel, local_port: 0, bridge_tag: ssm-bridge }
```

| Chiave | Obbligatoria | Default | Significato |
|---|---|---|---|
| `apiVersion` | Sì | — | Sempre `v1` |
| `targets.<nome>` | Sì | — | Uno per istanza RDS. Nome: minuscole, cifre e `-`, fino a 63 caratteri |
| `account_id` | Sì | — | Account AWS a 12 cifre. Se il profilo punta altrove, **ogni comando si ferma** |
| `region` | Sì | — | Regione dell'istanza (`eu-west-1`) |
| `aws_profile` | Sì | — | Profilo AWS CLI |
| `instance` | Sì | — | Identificativo dell'istanza RDS |
| `bucket` | Sì | — | Bucket S3 dell'opzione `SQLSERVER_BACKUP_RESTORE` |
| `prefixes.import`, `prefixes.export` | No | `import`, `export` | Prefissi S3 di import ed export |
| `protected` | No | `false` | `true` per la produzione: ogni modifica chiederà di riscrivere il nome del target |
| `max_tasks` | No | `2` | Task nativi in parallelo sull'istanza: 1 o 2 (massimo RDS) |
| `sql.user` | Sì | — | Login SQL Server |
| `sql.password` | No | `prompt` | **Da dove** arriva la password (§4.3), mai la password |
| `connect.mode` | No | `external` | `external`, `direct` o `tunnel` (§4.4) |
| `connect.local_port` | Con `external` | — | Porta locale del tunnel. Con `tunnel`: porta del tunnel condiviso dell'app e di `dialga tunnel` (`0` o assente = una porta libera); i comandi singoli aprono sempre il loro tunnel su una porta libera |
| `connect.bridge_tag` | No | `ssm-bridge` | Tag `Name` dell'istanza bridge (modalità `tunnel`) |

La lettura è **stretta**: una chiave sconosciuta (un refuso) è un errore, mai un default silenzioso. Un solo target non valido rende non valido tutto il file, per ogni comando: `dialga config validate` dice quale e perché.

### 4.3 Password SQL

| `sql.password` | Da dove arriva | CLI | App desktop |
|---|---|---|---|
| `prompt` (default) | Variabile `DIALGA_SQL_PASSWORD`, altrimenti la chiede | La chiede sul terminale se la variabile è vuota | Solo da `DIALGA_SQL_PASSWORD` nell'ambiente dell'app |
| `env:VAR` | Variabile d'ambiente `VAR` | Sì | Sì, se l'app la eredita |
| `keyring` | Portachiavi dell'OS | Previsto, non ancora attivo | Previsto |
| `secretsmanager:<ARN>` | AWS Secrets Manager | Previsto, non ancora attivo | Previsto |

La password non viaggia mai nelle richieste dell'app né nei log. Per l'app desktop con `prompt`:

```bash
read -rs DIALGA_SQL_PASSWORD && export DIALGA_SQL_PASSWORD && dialga-desktop
```

`read -rs` non mostra la password e non la lascia nella history. Senza password i controlli SQL vengono saltati e lo si vede nei risultati (§7.2).

### 4.4 Modalità di connessione

| `connect.mode` | Quando usarla | Cosa fa Dialga |
|---|---|---|
| `external` | Hai già un tunnel aperto (per esempio un altro `dialga tunnel` o `open-dloop-tunnels.sh`) | Si collega a `127.0.0.1:<local_port>` |
| `direct` | La workstation è dentro il VPC | Si collega all'endpoint RDS |
| `tunnel` | Caso normale, workstation fuori dal VPC | Apre da sé il tunnel SSM attraverso il bridge, lo usa e lo chiude alla fine di ogni operazione |

In tutte le modalità il certificato TLS di SQL Server viene **verificato** sul nome dell'endpoint RDS, anche attraverso il tunnel: non serve *Trust server certificate*.

Con `tunnel` ogni operazione apre la sua sessione SSM (qualche secondo in più). Se lavori a lungo con un client SQL o con molti comandi di fila, tieni aperto un tunnel con `dialga tunnel` (§5.5) e usa un target `external` sulla stessa porta.

> In arrivo: indicatore del tunnel condiviso nell'app (un solo tunnel per target, riusato dalle operazioni).

### 4.5 Target protetti

`protected: true` marca un'istanza di produzione: l'app mostra il badge **Protected** sulla scheda. I comandi di oggi sono tutti in sola lettura; con import ed export ogni modifica su un target protetto chiederà di **riscrivere il nome del target**, e ogni azione distruttiva (per esempio la cancellazione di un database) il nome del database.

### 4.6 Verificare la configurazione

```bash
$ dialga config validate
/home/utente/.config/dialga/config.yaml: valid, 1 target(s): dev-sql

$ dialga config validate -o json
{"path":"/home/utente/.config/dialga/config.yaml","valid":true,"targets":["dev-sql"]}
```

`dialga config show` stampa il file con i default applicati:

```yaml
apiVersion: v1
targets:
  dev-sql:
    account_id: "123456789012"
    region: eu-west-1
    aws_profile: default
    instance: my-sqlserver
    bucket: my-sqlserver-backups
    prefixes:
      import: import
      export: export
    protected: false
    max_tasks: 2
    sql:
      user: admin
      password: prompt
    connect:
      mode: tunnel
      local_port: 0
      bridge_tag: ""
```

## 5. CLI

Gli esempi sono output reali, presi il 2026-10-16 su un'istanza SQL Server 2017 attraverso il tunnel integrato, con nomi e identificativi sostituiti dai segnaposto di `docs/config.example.yaml`. Nel terminale gli esiti (`OK`, `WARN`, `FAIL`, `SKIP`) sono colorati.

### 5.1 Opzioni comuni ed exit code

| Opzione | Significato |
|---|---|
| `--config <file>` | File di configurazione (default: `dialga config path`) |
| `-o, --output text\|json` | Testo (default) o JSON per gli script |
| `[target]` o `-t, --target <nome>` | Target della configurazione. Se il file ne ha uno solo si può omettere; con più target va indicato (`choose a target: …`) |

| Exit code | Significato |
|---|---|
| `0` | OK |
| `1` | Errore (configurazione, AWS, connessione, file illeggibile…) |
| `2` | Il comando è andato, ma una verifica non è passata (`check`, `inspect`) |
| `3` | Il task RDS è finito in `ERROR` o `CANCELLED` (`wait`) |

### 5.2 `dialga check`

Controlli preliminari di un target, in sola lettura: credenziali e account AWS, istanza RDS, opzione di backup nativo, bucket, spazio, connessione, login SQL con certificato verificato, database e task nativi.

```bash
dialga check [target] [-t target] [-o json]
```

```
$ dialga check
Checking dev-sql: instance my-sqlserver, profile default, eu-west-1
  OK    AWS credentials                    profile default · account 123456789012
  OK    RDS instance                       available · sqlserver-se 14.00.3540.1.v1 · option group my-sqlserver-options in-sync
  OK    Native backup option               SQLSERVER_BACKUP_RESTORE in my-sqlserver-options · automated backups 04:29-04:59 UTC: avoid long restores across it
  OK    S3 bucket                          s3://my-sqlserver-backups reachable
  OK    Storage                            allocated 100 GiB (autoscaling up to 200 GiB) · free 97.2 GiB · a restored database takes the sum of its files, not the .bak size
  OK    Connection                         tunnel (integrated) 127.0.0.1:41451 → my-sqlserver.abcdefghijkl.eu-west-1.rds.amazonaws.com:1433 via bridge i-0123456789abcdef0
  OK    SQL Server login (TLS verified)    14.0.3540.1 Standard Edition (64-bit) · login admin · certificate verified for my-sqlserver.abcdefghijkl.eu-west-1.rds.amazonaws.com
  OK    Databases and native backup tasks  3 user databases · no native task running
All checks passed.
```

| Esito di un passo | Significato |
|---|---|
| `OK` | Superato |
| `WARN` | Da leggere, non fa fallire il check (per esempio spazio libero non leggibile da CloudWatch, task già in corso) |
| `FAIL` | Non superato: il check finisce con exit `2` |
| `SKIP` | Saltato perché dipende da un passo fallito, o perché manca la password SQL |

Exit code: `0` tutti superati (anche con avvisi), `2` almeno uno non superato o saltato, `1` errore. Con `-o json`: `{"target", "instance", "ok", "steps": [{"step", "label", "status", "detail"}]}`. In modalità `tunnel` il check apre il tunnel e lo chiude alla fine.

### 5.3 `dialga status`

Task nativi di backup e restore dell'istanza (`msdb.dbo.rds_fn_task_status`), dal più recente. RDS ne tiene 36 giorni.

```bash
dialga status [target] [-t target] [--db <database>] [--limit 1-100]   # --limit default 20
```

```
$ dialga status --limit 5
Native backup/restore tasks on dev-sql (instance my-sqlserver), newest first:
  ID  TYPE        DATABASE      LIFECYCLE  %    CREATED           UPDATED           INFO
  2   RESTORE_DB  xfer_e2e      SUCCESS    100  2026-10-15 13:32  2026-10-15 13:33  [2026-10-15 13:32:07.537] Task execution has star…
  1   BACKUP_DB   xfer_e2e_src  SUCCESS    100  2026-10-15 13:29  2026-10-15 13:31  [2026-10-15 13:30:07.270] Task execution has star…
Full detail of a task: dialga wait <task-id>
```

Gli orari sono quelli del server, senza fuso (RDS lavora in UTC salvo diversa impostazione dell'istanza). Exit code: `0`, oppure `1` per un errore.

### 5.4 `dialga wait`

Segue un task fino alla fine e stampa ogni cambio di stato o di percentuale. **Ctrl-C ferma l'attesa, mai il task.**

```bash
dialga wait <task-id> [-t target] [--interval 30s]
```

```
$ dialga wait 2
Following task 2 on dev-sql every 30s (Ctrl-C stops the wait, not the task).
  [10:49:30] task 2: SUCCESS 100%
Task 2 completed: RESTORE_DB of xfer_e2e.
    [2026-10-15 13:32:07.537] Task execution has started.
    [2026-10-15 13:32:07.610] import/xfer_e2e.bak: Completed processing 97.4% of S3 chunks.
    [2026-10-15 13:32:07.613] 99 percent processed.
    [2026-10-15 13:32:07.620] 100 percent processed.
    [2026-10-15 13:32:07.630] Processed 360 pages for database 'xfer_e2e', file 'xfer_e2e_src' on file 1.
    [2026-10-15 13:32:07.633] Processed 2 pages for database 'xfer_e2e', file 'xfer_e2e_src_log' on file 1.
    [2026-10-15 13:32:07.817] RESTORE DATABASE successfully processed 362 pages in 0.036 seconds (78.355 MB/sec).
    [2026-10-15 13:33:07.030] import/xfer_e2e.bak: S3 processing completed successfully
    [2026-10-15 13:33:07.120] Command execution completed successfully.
```

RDS aggiorna lo stato più o meno ogni 2 minuti: un `--interval` sotto i 30 s serve a poco. Exit code: `0` `SUCCESS`, `3` `ERROR` o `CANCELLED`, `1` errore. Su un task già finito risponde subito.

### 5.5 `dialga tunnel`

Apre il tunnel SSM verso SQL Server di un target, **qualunque sia** il suo `connect.mode`, e lo tiene aperto in primo piano finché premi Ctrl-C. Si riconnette da solo se la sessione cade. Serve per usare un client SQL (VS Code, DBeaver, `sqlcmd`, SqlPackage).

```bash
dialga tunnel [target] [-t target] [--port N] [-o json]   # --port: default connect.local_port, 0 = a free port
```

```
$ dialga tunnel
Opening the tunnel to dev-sql: instance my-sqlserver, profile default, eu-west-1 (Ctrl-C closes it).
  [10:49:31] connecting   looking for the bridge (Name=ssm-bridge) in vpc-0123456789abcdef0
  [10:49:33] ready        127.0.0.1:41451 → my-sqlserver.abcdefghijkl.eu-west-1.rds.amazonaws.com:1433 via bridge i-0123456789abcdef0
  SQL clients: server 127.0.0.1,41451; the certificate is issued for my-sqlserver.abcdefghijkl.eu-west-1.rds.amazonaws.com.
^C  [10:50:11] closed
```

- Stati: `connecting`, `ready`, `reconnecting`, `closed`. Con `-o json` ogni cambio di stato è una riga JSON (`target`, `status`, `time`, `localPort`, `endpoint`, `bridge`, `detail`).
- Nel client SQL: server `127.0.0.1,<porta>`, crittografia attiva e, se il client lo chiede, nome del certificato = endpoint RDS indicato nella riga `SQL clients`.
- Ctrl-C o SIGTERM chiudono il tunnel e terminano la sessione SSM. Exit code: `0` alla chiusura, `1` se il tunnel non si apre.

### 5.6 `dialga databases`

Database utente dell'istanza (`sys.databases` senza i database di sistema e `rdsadmin`): stato, dimensione di dati e log, compatibility level, recovery model, owner. Alias: `dbs`.

```bash
dialga databases [target] [-t target] [-o json]
```

```
$ dialga databases
User databases on dev-sql (instance my-sqlserver):
  NAME        STATE   DATA       LOG        COMPAT  RECOVERY  OWNER
  app_db      ONLINE  305.4 MiB  894.9 MiB  140     FULL      rdsa
  app_db_tmp  ONLINE  277.6 MiB  313.6 MiB  140     FULL      rdsa
  keycloak    ONLINE  8.0 MiB    1.0 MiB    140     FULL      rdsa
3 databases, 1.8 GiB in total.
```

Lo stato include le limitazioni d'accesso, per esempio `ONLINE (READ_ONLY, SINGLE_USER)`. Owner `-`: nessun login ha il SID dell'owner (tipico dopo un restore da un altro server). Exit code: `0`, `1` errore (anche senza password SQL).

### 5.7 `dialga inspect`

Legge i `.bak` **sulla workstation, prima dell'upload**, in un SQL Server usa e getta in un container (podman, oppure docker). I file sono montati in sola lettura; il container viene rimosso alla fine, anche su errore o Ctrl-C. Su AWS legge solo versione del motore e spazio libero del target.

```bash
dialga inspect <file.bak>... [-t target] [--image IMG] [-o json]
```

- **Più file** sono più backup, oppure le parti (stripe) di un unico backup: Dialga li raggruppa da sé.
- **Senza `-t`** legge solo gli header e non serve il file di configurazione.
- **Con `-t`** giudica ogni backup rispetto al motore del target e lo spazio necessario rispetto al libero.

```
$ dialga inspect -t dev-sql t2017.bak t2022.bak
Inspecting 2 file(s) against dev-sql
  OK    Target engine          my-sqlserver · sqlserver-se 14.00.3540.1.v1 (SQL Server 2017) · 97.2 GiB free
  OK    Container runtime      podman 5.8.4 · rootless · SELinux
  OK    SQL Server image       mcr.microsoft.com/mssql/server:2022-latest
  OK    Throwaway SQL Server   dialga-inspect-ynezziysazlr on 127.0.0.1:42209, removed at the end
  OK    SQL Server ready       SQL Server 16.0.4305.5 after 4s
  OK    Backup media           2 file(s), 2 backup(s)
  OK    Backup headers         t2017.bak: t2017, SQL Server 2017 (14.0.3560), full · t2022.bak: t2022, SQL Server 2022 (16.0.4305), full
  OK    Database files         4 database file(s), 32.0 MiB needed on restore
  FAIL  Verdict                1 of 2 restorable on dev-sql · not restorable: t2022.bak
  OK    Space on the target    32.0 MiB needed on restore, 97.2 GiB free on my-sqlserver (0.0%)
  OK    Cleanup                dialga-inspect-ynezziysazlr removed

/home/utente/backup/t2017.bak
    Database             t2017 (server 5cd814ff04bc)
    Source               SQL Server 2017 (14.0.3560) · compatibility level 140
    Collation            SQL_Latin1_General_CP1_CI_AS · recovery FULL
    Backup               full · 2026-10-16T08:33:25
    Backup size          2.5 MiB
    Files                D  t2017                             8.0 MiB  PRIMARY
                         L  t2017_log                         8.0 MiB
    Needed on restore    16.0 MiB
  OK    version     SQL Server 2017 (14.0.3560), not newer than SQL Server 2017 on the target
  OK    type        full backup
  OK    encryption  backup not encrypted

/home/utente/backup/t2022.bak
    Database             t2022 (server 3c8ecb037496)
    Source               SQL Server 2022 (16.0.4305) · compatibility level 160
    Collation            SQL_Latin1_General_CP1_CI_AS · recovery FULL
    Backup               full · compressed · checksums · 2026-10-16T08:33:30
    Backup size          2.8 MiB (481.1 KiB compressed)
    Files                D  t2022                             8.0 MiB  PRIMARY
                         L  t2022_log                         8.0 MiB
    Needed on restore    16.0 MiB
  FAIL  version     backup from SQL Server 2022 (16.0.4305): the target runs SQL Server 2017, and RDS cannot restore a backup from a newer version
  OK    type        full backup
  OK    encryption  backup not encrypted
    What to do: Restore it on SQL Server 2022 or newer (a local container), lower its compatibility level to 140, export a .bacpac and import that: import runbook, procedure C (.bak newer than the target)

Not restorable on dev-sql as it is: see FAIL above.
```

| Regola del verdetto | Esito |
|---|---|
| Versione del backup più recente del motore del target (es. 2019/2022 su 2017) | `FAIL`, con la procedura `.bacpac` (§7.5) |
| Backup da SQL Server 2005 | `FAIL` |
| Backup cifrato, database con TDE, file FILESTREAM / In-Memory OLTP (tipo `S`) | `FAIL` |
| Backup differenziale o del log | `WARN`: serve prima il full, ripristinato con NORECOVERY |
| Più backup set nello stesso file | `WARN`: RDS ripristina il primo |
| Spazio: somma dei file del DB oltre l'80% del libero | `WARN`; oltre il 100% `FAIL` |
| Spazio libero non leggibile da CloudWatch | `WARN` |

Lo spazio necessario è la **somma dei file del database** (dati + log), non la dimensione del `.bak`: un backup compresso da 500 MB può richiedere decine di GB.

| Variabile | Effetto |
|---|---|
| `DIALGA_CONTAINER_RUNTIME=podman\|docker` | Forza il runtime (default: podman se c'è, altrimenti docker) |
| `DIALGA_INSPECT_IMAGE=<immagine>` | Sostituisce l'immagine di default (anche `--image`). Per i backup di SQL Server 2025 usa `mcr.microsoft.com/mssql/server:2025-latest` |

Exit code: `0` ripristinabile (o header letti, senza `-t`), `2` non ripristinabile o spazio insufficiente, `1` errore (anche un file che non è un backup leggibile).

### 5.8 `dialga config`

| Comando | Cosa fa |
|---|---|
| `dialga config path` | Stampa il percorso del file di configurazione |
| `dialga config validate` | Controlla il file ed elenca i target (exit `1` se non è valido) |
| `dialga config show` | Stampa il file con i default applicati (YAML) |

Esempi nel §4.6.

> In arrivo: `dialga config init` (creazione guidata del file) e `dialga doctor` (diagnosi dei prerequisiti: runtime container, plugin SSM, profili AWS).

### 5.9 `dialga version` e `dialga completion`

```bash
$ dialga version
dialga X.Y.Z
$ dialga completion bash|zsh|fish|powershell   # shell completion script
```

Il pacchetto stampa la versione del rilascio; una build locale di sviluppo stampa `dialga dev`.

### 5.10 Import ed export

> In arrivo: `upload`, `restore`, `finish`, `post`, `export`, `download`, `cancel`, `drop` e la pipeline completa di import, con anteprima (dry-run) per default e `--apply` per eseguire. Questa sezione va completata quando i comandi sono nel branch di integrazione.

## 6. App desktop

### 6.1 Avvio

- Dal menu: **Dialga**. Da terminale: `dialga-desktop` (serve per passare la password SQL, §4.3, e per vedere i messaggi dell'engine, che compaiono con il prefisso `engine:`).
- L'app avvia l'engine (`dialga serve`) in background e lo chiude quando la finestra si chiude. In basso nella barra laterale c'è il suo stato: `engine <versione>`, *Starting engine…* oppure *Engine not available*.
- Lingua dell'interfaccia: **inglese** di default, **italiano** in *Settings → Language*. Qui sotto le etichette sono in inglese, con l'italiano tra parentesi.

### 6.2 Barra laterale

| Voce | Contenuto |
|---|---|
| **Instances** (Istanze) | Una scheda per target (§6.3) |
| **Databases** (Database) | Database utente di un'istanza (§6.4) |
| **Import**, **Export**, **Activity** (Importa, Esporta, Attività) | Disabilitate, con l'etichetta *Soon* (*Presto*) |
| **Settings** (Impostazioni) | Lingua, tema, percorso della configurazione, versione (§6.5) |

### 6.3 Instances

Una scheda per ogni target della configurazione. Se il file non esiste, la schermata mostra *No instances yet* con il percorso in cui crearlo; se ha errori, *The configuration file has errors* con il dettaglio.

**Intestazione e dati del target**: nome del target, istanza e regione, badge **Protected** se `protected: true`; poi *Account*, *Profile*, *Bucket* e *Connection*:

| Connection | Modalità |
|---|---|
| *Open tunnel on port N* (Tunnel aperto sulla porta N) | `external` |
| *Direct to the endpoint* (Diretta all'endpoint) | `direct` |
| *Integrated tunnel* (Tunnel integrato) | `tunnel` |

**Panoramica dell'istanza** (si carica quando la scheda è visibile; pulsante di aggiornamento a destra):

| Elemento | Significato |
|---|---|
| Badge di stato RDS | *Available* (verde); *Backing up*, *Modifying*, *Rebooting*, *Starting*, *Maintenance* (in corso); *Stopped*, *Stopping* (grigio); *Storage full* e stati `incompatible-*`/`inaccessible-*` (rosso) |
| Motore e classe | Per esempio *SQL Server 2017 Standard* e `db.m6i.large` |
| *Storage* (Spazio) | Barra dello spazio usato: *X free of Y (Z%)*. Gialla sotto il 20% libero, rossa sotto il 10%. *Autoscaling up to N GiB* se lo storage cresce da sé. *Free space not available from CloudWatch* senza i permessi CloudWatch |
| *User databases* (Database utente) | Numero dei database utente; il link apre la schermata Databases su questo target |
| *Active tasks* (Task attivi) | Task nativi in coda, in corso o in annullamento |
| *Databases and tasks need the SQL password* | Manca la password SQL nell'ambiente dell'app (§4.3); un clic mostra il dettaglio |
| *Databases and tasks not read* | Sessione SQL fallita (tunnel, login): un clic mostra l'errore; *Run check* dice quale passo non va |

Se la parte AWS fallisce (credenziali, account) la panoramica mostra l'errore e il pulsante *Retry* (*Riprova*).

**Run check** (*Esegui check*): esegue `dialga check` e mostra i passi mentre avanzano, con un badge per ognuno (§6.6). Alla fine un riquadro dice *All checks passed*, *All checks passed, with warnings* o *Some checks failed*. Se manca la password SQL, un avviso in cima lo dice prima dei passi.

**Show tasks** (*Mostra task*): pannello dei task nativi (gli stessi di `dialga status`): ID, tipo, database, badge dello stato, *Updated* (ora dell'ultimo aggiornamento), barra di avanzamento per i task attivi, *Details* con il log RDS del task. Finché un task è in corso il pannello si aggiorna ogni 30 secondi (*Refreshing every 30 seconds while a task is running*).

> In arrivo: indicatore del tunnel condiviso sulla scheda; pulsanti *Import* ed *Export* che aprono i wizard sul target.

### 6.4 Databases

- In alto il selettore **Instance** (*Istanza*): l'app ricorda l'ultimo target scelto. Pulsante *Refresh* (*Aggiorna*).
- Tabella: *Name*, *State*, *Size* (dati + log; il tooltip li separa), *Compatibility* (livello e versione SQL Server corrispondente), *Recovery*, *Owner*. Clic sull'intestazione per ordinare (ancora per invertire; *Size* parte dal più grande).
- **Filter** (*Filtra*): cerca in nome, owner, stato e recovery model; il contatore dice *N of M databases*.
- Badge accanto allo stato: *Read-only* (*Sola lettura*), *Single user* (*Utente singolo*), *Restricted* (*Accesso limitato*). Owner in grigio: nessun login ha il SID dell'owner.
- Stati vuoti: *No user databases* (solo database di sistema), *No databases match the filter* (con *Clear filter*), *No instances configured* (con *Go to Instances*).
- In caso di errore: *The list is read with a SQL session: the engine needs the SQL password and a reachable instance*. Vedi §7.2 e §7.3.

### 6.5 Settings

| Sezione | Contenuto |
|---|---|
| *Appearance* (Aspetto) | *Language*: English, Italiano. *Theme*: System, Light, Dark (Sistema, Chiaro, Scuro). Le scelte restano su questo computer |
| *Configuration file* (File di configurazione) | Percorso del file in uso; eventuali errori del file |
| *About* (Informazioni) | Versione dell'engine e indirizzo dell'API locale |

### 6.6 Stati e badge

| Dove | Badge | Significato |
|---|---|---|
| Passi del check | *Running* (In corso) | Il passo è in esecuzione |
| | *Passed* (Superato) | OK |
| | *Warning* (Attenzione) | Da leggere, non fa fallire il check |
| | *Failed* (Fallito) | Non superato |
| | *Skipped* (Saltato) | Dipende da un passo fallito, o manca la password SQL |
| Task nativi | *Queued* (In coda) | `CREATED` |
| | *In progress* (In corso) | `IN_PROGRESS`, con la percentuale |
| | *Succeeded* (Riuscito) | `SUCCESS` |
| | *Failed* (Fallito) | `ERROR`: leggi *Details* |
| | *Cancelling* / *Cancelled* (Annullamento / Annullato) | `CANCEL_REQUESTED` / `CANCELLED` |
| Database | *Online*, *Restoring*, *Recovering*, *Recovery pending*, *Suspect*, *Emergency*, *Offline* | Stato di `sys.databases` (*Restoring* è normale durante un restore, o dopo un restore con NORECOVERY) |

### 6.7 Funzioni in arrivo

> In arrivo: wizard **Importa** (File → Verifica, con il verdetto di `inspect` per ogni file e lo spazio → Destinazione → Anteprima → Avanzamento → Esito), wizard **Esporta**, schermata **Activity** (coda e storico dei job, con eventi e log), configurazione al primo avvio e aggiunta di un'istanza dalla GUI.

## 7. Risoluzione dei problemi

I messaggi sono quelli che stampano la CLI e l'app (in inglese).

### 7.1 Configurazione e AWS

| Messaggio | Causa | Cosa fare |
|---|---|---|
| `no configuration at <path> (start from docs/config.example.yaml)` | Il file non esiste | §4.1 |
| `invalid YAML: yaml: unmarshal errors: line N: field <x> not found in type config.Target` | Chiave sconosciuta o con un refuso | Correggi la chiave alla riga indicata: la lettura è stretta (§4.2) |
| `connect.local_port is required with mode external (the port of the open tunnel)` | Modalità `external` senza porta | Indica la porta del tunnel aperto, o passa a `mode: tunnel` |
| `choose a target: a, b` | Più target e nessuno indicato | `dialga <comando> <target>` o `-t <target>` |
| `unknown target "x" (known: …)` | Nome del target sbagliato | Usa uno dei nomi elencati |
| `profile <p> points to account <A>, expected <B>` | Il profilo AWS è di un altro account: Dialga si ferma prima di toccare qualsiasi cosa | Correggi `aws_profile` o `account_id`; verifica con `aws sts get-caller-identity --profile <p>` |
| Errore di `STS: GetCallerIdentity` (token scaduto, credenziali assenti) | Sessione SSO scaduta o chiavi non valide | `aws sso login --profile <p>`, oppure aggiorna le chiavi del profilo |
| `RDS instance <id>: … DBInstanceNotFound` / `instance not found` | Identificativo o regione sbagliati | Controlla `instance` e `region` (identificativo, non endpoint) |
| `RDS instance` in `FAIL` con stato diverso da `available` | Istanza ferma, in modifica, in backup… | Aspetta che torni `available`; se è ferma, chiedi all'amministratore |
| `option group <g> <stato>` in `FAIL` (non `in-sync`) | Una modifica all'option group non è ancora applicata | Aspetta l'applicazione; se resta `pending-reboot` serve un riavvio nella finestra di manutenzione (decisione dell'amministratore) |
| `the instance has no option group: native backup and restore cannot run` / `SQLSERVER_BACKUP_RESTORE missing in option group <g>` | Backup e restore nativi non configurati | L'amministratore aggiunge l'opzione `SQLSERVER_BACKUP_RESTORE` (con il suo ruolo IAM sul bucket) all'option group dell'istanza |
| `s3://<b> not reachable: bucket not found` | Nome del bucket sbagliato | Correggi `bucket` |
| `… access denied: the profile cannot read the bucket` | Mancano i permessi S3 | Statement `BackupBucket` della policy (`docs/iam-policy.md`) |
| `… the bucket is in another region` | Il bucket non è nella regione del target | Usa il bucket della regione dell'istanza |
| `free space not read from CloudWatch (permissions?)` (`WARN`) | Manca `cloudwatch:GetMetricStatistics` | Statement `RdsFreeStorage`; non blocca il check |
| `N native tasks running or queued: a new one may wait` (`WARN`) | Ci sono già task nativi | `dialga status`; RDS ne esegue al massimo 2 alla volta |

### 7.2 Password e login SQL

| Messaggio | Causa | Cosa fare |
|---|---|---|
| `SQL login will be skipped: environment variable <VAR> is empty` e passi SQL `SKIP` | `sql.password: env:VAR` ma la variabile non è impostata nel terminale (o nell'ambiente dell'app) | `read -rs VAR && export VAR`, poi rilancia |
| `no SQL password in the engine environment: sql.password says where it comes from …` | L'engine non ha la password: con `prompt` serve `DIALGA_SQL_PASSWORD`, con `env:VAR` la variabile indicata | Esporta la variabile giusta nell'ambiente dell'app o del terminale, poi riprova |
| `sql.password is prompt: set DIALGA_SQL_PASSWORD, or use env:VAR in the configuration` | App desktop con `prompt`: non può chiedere la password | Avvia l'app da terminale con `DIALGA_SQL_PASSWORD` (§4.3) |
| *Databases and tasks need the SQL password* (app) | Come sopra | Come sopra |
| `SQL password: environment variable <VAR> is empty` (`databases`, `status`, `wait`) | Questi comandi richiedono la password | Imposta la variabile |
| `Login failed for user '<u>'` | Utente o password sbagliati | Controlla `sql.user` e la password |
| Errore di certificato (`x509: certificate is valid for …`) | Il tunnel `external` porta a un'altra istanza | Controlla che `local_port` sia il tunnel di **questa** istanza |

### 7.3 Tunnel e bridge

| Messaggio | Causa | Cosa fare |
|---|---|---|
| `nothing listening on 127.0.0.1:N: open the SSM tunnel` | Modalità `external` senza tunnel aperto | Apri `dialga tunnel <target> --port N` in un altro terminale, oppure passa a `mode: tunnel` |
| `cannot reach <endpoint>:1433: run inside the VPC or use a tunnel` | Modalità `direct` fuori dal VPC | Usa `mode: tunnel` |
| `session-manager-plugin not found on PATH: install it (…)` | Plugin non installato | §3 |
| `session-manager-plugin <v> is too old, Dialga needs a version after 1.2.497.0: update it (…)` | Plugin troppo vecchio: Dialga gli passa la sessione dall'ambiente, mai dalla riga di comando | Aggiorna il plugin (§3) |
| `no instance tagged Name=<tag>: create the bridge, or set connect.bridge_tag` | Il bridge non esiste o ha un altro tag | Chiedi all'amministratore il tag del bridge e impostalo in `connect.bridge_tag` |
| `the bridge tagged Name=<tag> in <vpc> is not running (i-… is stopped): start it` | Bridge spento | L'amministratore avvia l'istanza bridge |
| `N running instances tagged Name=<tag> in <vpc> (…): keep one, or set connect.bridge_tag` | Più bridge con lo stesso tag | Tenerne uno, o dare al bridge giusto un tag univoco |
| `no instance tagged Name=<tag> in <vpc>, the VPC of the RDS instance (only in …)` | Il bridge è in un altro VPC | Serve un bridge nel VPC dell'istanza RDS |
| `127.0.0.1:N is in use (another tunnel?): choose another local port` | La porta locale è occupata | Un'altra porta (`--port`, `connect.local_port`), oppure `0` per una libera |
| Sessione SSM ancora `Active` (es. in `aws ssm describe-sessions --state Active`) dopo un `kill -9` o un crash di `dialga` | Con SIGKILL il processo non può chiudere il tunnel; la sessione resta fino al timeout di inattività | `aws ssm terminate-session --session-id <id>` (solo le tue sessioni). Ctrl-C, SIGTERM, SIGHUP e una pipe chiusa (`| head`) chiudono invece la sessione da soli |
| `ssm StartSession on i-…: … AccessDeniedException` | Mancano i permessi SSM | Statement `TunnelStartSession` e `TunnelTerminateOwnSession` della policy |
| `the tunnel did not answer on 127.0.0.1:N within 30s` | La sessione SSM si apre ma non inoltra (rete del bridge, security group verso l'istanza) | Riprova; se persiste, l'amministratore controlla il bridge e il security group dell'istanza RDS |

### 7.4 `inspect`

| Messaggio | Causa | Cosa fare |
|---|---|---|
| `no container runtime: install podman (…) or docker` | Né podman né docker | Installa podman (§3) |
| `docker info: … (is the daemon running and your user in the docker group? Or install podman)` | Docker installato ma non utilizzabile | Avvia il daemon / aggiungi l'utente al gruppo `docker`, oppure usa podman |
| `<image> is not in the local images: pull it once (about 1.5 GB) with: podman pull <image>` | Immagine non scaricata | Esegui il comando indicato |
| `SQL Server stopped: …` | Il container si è fermato (spesso memoria: SQL Server vuole almeno 2 GB) | Libera memoria e riprova |
| `The media family on device '<file>' is incorrectly formed` | Il file non è un backup, è danneggiato, o è di una versione più recente dell'immagine (2025 letto con 2022) | Riscarica il file; per un backup 2025 usa `--image mcr.microsoft.com/mssql/server:2025-latest` |
| `striped backup: 2 of 3 files given, stripe 3 missing` | Backup su più file, non tutti indicati | Passa tutti i file del backup |
| `the path cannot contain ':' or control characters` | Nome o cartella con `:` | Rinomina o sposta il file |
| `backup from SQL Server 2022 (…): the target runs SQL Server 2017 …` | Backup più recente del motore del target | Procedura `.bacpac` (§7.5) |
| `Cleanup` in `WARN` | Il container non è stato rimosso | `podman rm -f $(podman ps -aq --filter label=io.dialga.inspect=true)` |

### 7.5 `.bak` più recente del target: procedura `.bacpac`

RDS **non ripristina un backup di una versione più recente** del suo motore (un `.bak` 2019, 2022 o 2025 su SQL Server 2017 fallisce con Msg 3169). Si passa da un `.bacpac`, che contiene schema e dati. Dialga non automatizza ancora questo percorso (previsto nella 1.x); i passi:

1. **Restore locale** in un SQL Server della stessa versione del backup o più recente (container podman, immagine 2022 o 2025).
2. Sulla copia locale: **compatibility level** al massimo del target (`140` per SQL Server 2017; in generale major × 10):

   ```sql
   ALTER DATABASE [app_db] SET COMPATIBILITY_LEVEL = 140;
   ```

3. Sulla copia locale: **utente master di RDS in `db_owner`**, altrimenti l'import su RDS fallisce per permessi (prerequisito documentato da AWS): login con lo stesso nome dell'utente master, utente nel database, `ALTER ROLE db_owner ADD MEMBER [<utente master>]`.
4. **Export BACPAC** dalla copia locale: VS Code (estensione `mssql`, *DACPAC/BACPAC Operations → Export BACPAC*) oppure SqlPackage (`/Action:Export`). Se l'export si ferma su oggetti non validi (riferimenti ad altri DB, utenti Windows), correggili nella copia locale.
5. **Import BACPAC** su RDS attraverso il tunnel: `dialga tunnel <target>` in un terminale, poi SqlPackage (`/Action:Import /TargetServerName:"127.0.0.1,<porta>"`, la via indicata da AWS) o VS Code (*Import BACPAC*, da provare su RDS). L'import vuole un database nuovo o vuoto.
6. Al termine: login dell'applicazione e utenti orfani. Un `.bacpac` porta gli utenti, non i login.

Dettagli, limiti e ripieghi: runbook di import, procedura C (`runbook-import-db-mssql-dev.md`). TODO: confermare dove pubblicare il runbook per gli utenti di Dialga.

Se capita spesso, valuta con l'amministratore un aggiornamento di major version del motore RDS.

### 7.6 App desktop

| Sintomo | Causa | Cosa fare |
|---|---|---|
| *Engine not available* nella barra laterale | L'engine non è partito | Avvia `dialga-desktop` da terminale e leggi le righe `engine:` |
| *No instances yet* | Il file di configurazione non esiste | §4.1; il percorso è nella schermata |
| *The configuration file has errors* | File non valido | `dialga config validate` dice quale chiave |
| La panoramica mostra un errore AWS | Credenziali o account (§7.1) | Correggi, poi *Retry* |

## 8. Riferimenti

| Documento | Contenuto |
|---|---|
| `docs/config.example.yaml` | Configurazione di esempio commentata |
| `docs/iam-policy.md`, `docs/iam-policy.json` | Policy IAM minima per gli utenti |
| `docs/release.md` | Come si rilascia e si installa una versione |
| `CHANGELOG.md` | Modifiche per versione |
| `dialga <comando> --help` | Opzioni di ogni comando |
| Documentazione AWS | *Importing and exporting SQL Server databases using native backup and restore* (RDS for SQL Server); *Install the Session Manager plugin for the AWS CLI* |
