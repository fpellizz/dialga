# Guida utente di Dialga

| | |
|---|---|
| **Data** | 2026-10-16 |
| **Autore** | Fabio Pellizzaro (con Claude Code) |
| **Versione documentata** | `0.7.2` |
| **Destinatari** | Sviluppatori e amministratori che importano o esportano database SQL Server su Amazon RDS |
| **Piattaforma** | Linux x86_64 (app desktop e CLI); solo CLI su Windows e macOS |

> **Sintesi**
> - Dialga importa ed esporta database SQL Server su istanze **Amazon RDS** passando per **S3** (backup e restore nativi `rds_*`). Ha un'**app desktop** e una **CLI** (`dialga`), che usano lo stesso engine.
> - Oggi fa: **controlli preliminari** (`check`, `doctor`), **configurazione guidata** (`config init`), **upload e download su S3** (`upload`, `download`), **operazioni native** (`restore`, `export`, `finish`, `cancel`, `drop`, `post`), **task nativi** (`status`, `wait`), **tunnel SSM integrato** (`tunnel`), **elenco dei database** (`databases`) e **ispezione dei `.bak`** (`inspect`).
> - Ogni comando che modifica qualcosa mostra prima un'**anteprima** (dry-run) e cambia lo stato solo con `--apply`; i target protetti e le azioni distruttive chiedono di riscrivere un testo di conferma.
> - Nell'app desktop ci sono le azioni sui database (Export, Report, Drop, Finish, Restore from S3), l'aggiunta guidata di un'istanza e il tunnel condiviso. Ci sono anche i **wizard Import ed Export**, la schermata **Activity** (coda dei job) e la **ripresa dei job**.
> - La password SQL **non è mai nel file di configurazione**: il file dice solo da dove leggerla.

## 1. Cosa c'è oggi

| Funzione | CLI | App desktop | Stato |
|---|---|---|---|
| Controlli preliminari di un target | `dialga check` | Instances → *Run check* | Disponibile |
| Diagnosi del computer (plugin SSM, runtime container, profili AWS) | `dialga doctor` | Settings → *Prerequisites* | Disponibile |
| Configurazione guidata, aggiunta e rimozione dei target | `dialga config init`, `add-target`, `remove-target` | Instances → *Add instance*; Settings → *Targets* | Disponibile |
| Task nativi di backup e restore | `dialga status`, `dialga wait` | Instances → *Show tasks* | Disponibile |
| Annullare un task nativo | `dialga cancel` | *Cancel* nel pannello dei task | Disponibile |
| Tunnel SSM verso l'istanza | `dialga tunnel`, `connect.mode: tunnel` | Tunnel condiviso, indicatore sulla scheda e nella barra laterale | Disponibile |
| Database dell'istanza | `dialga databases` | Databases | Disponibile |
| Panoramica dell'istanza (stato, spazio, DB, task attivi) | — | Instances, nella scheda | Disponibile |
| Ispezione dei `.bak` | `dialga inspect` | Passo *Files → Verify* del wizard Import | Disponibile |
| Upload e download su S3 | `dialga upload`, `dialga download` | Dentro i wizard Import ed Export | Disponibile |
| Restore, export, finish, drop di un database | `dialga restore`, `export`, `finish`, `drop` | Databases: *Restore from S3*, *Export*, *Finish restore*, *Drop* | Disponibile |
| Report dopo il restore | `dialga post` | Databases → *Report* | Disponibile |
| Import di più database come un solo job | `dialga import`, `dialga run` (piano YAML) | Wizard Import | Disponibile |
| Export di più database come un solo job | `dialga run` (piano YAML) | Wizard Export, *Export selected* in Databases | Disponibile |
| Elenco, dettaglio e ripresa dei job | `dialga jobs`, `dialga jobs show`, `dialga jobs resume` | Activity | Disponibile |

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

1. Controlla il computer: `dialga doctor` (§5.11) dice cosa manca tra plugin SSM, runtime container e profili AWS.
2. Crea la configurazione: `dialga config init` (§4.1) da terminale, oppure *Add instance* nell'app (§6.3).
3. Verifica da terminale: `dialga config validate`, poi `dialga check`.
4. Avvia l'app dal menu (**Dialga**) o da terminale con `dialga-desktop`. Per i target con `sql.password: prompt` avviala da terminale con la password nell'ambiente (§4.3).

Dopo la rimozione restano la configurazione (`~/.config/dialga/`) e i dati della finestra (`~/.local/share/com.decisyon.dialga/`).

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

`dialga doctor` (§5.11) e, nell'app, *Settings → Prerequisites* dicono se plugin e runtime container ci sono e, se mancano, il comando di installazione per la tua distribuzione.

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

Tutti i comandi accettano `--config <file>` per usarne un altro. Il file non esiste dopo l'installazione: lo crea il primo target che aggiungi (§4.1.1), oppure lo scrivi a mano partendo dall'esempio.

```bash
cfg=$(dialga config path)
[ -e "$cfg" ] || install -D -m 600 /usr/share/doc/dialga/config.example.yaml "$cfg"
"${EDITOR:-vi}" "$cfg"
```

L'app desktop rilegge il file a ogni aggiornamento della schermata: non serve riavviarla.

#### 4.1.1 Primo avvio: `dialga config init`

Aggiunge un target alla configurazione, in modo guidato: profilo AWS, regione, istanza SQL Server trovata nell'account, bucket S3 (scelto dall'elenco dei bucket dell'account nella regione, oppure digitato; verificato con `HeadBucket`), login SQL, **da dove** arriva la password (mai la password), modalità di connessione e nome del target.

```bash
dialga config init [--profile P] [--region R] [--instance I] [--bucket B] [--sql-user U] [--password prompt|env:VAR] \
  [--mode tunnel|external|direct] [--local-port N] [--name N] [--protected] [--replace] [--no-bucket-check] [-y]
```

- **Su un terminale** chiede ogni valore che le opzioni non danno e, alla fine, una conferma (`-y` salta la conferma).
- **Senza terminale** i valori arrivano dalle opzioni, almeno `--instance` e `--bucket` (altrimenti: `not a terminal: give at least --instance and --bucket`).
- Chiamate AWS, tutte in sola lettura: `sts:GetCallerIdentity` (la guardia sull'account), `rds:DescribeDBInstances`, `rds:DescribeOptionGroups`, `s3:HeadBucket` e, facoltativa, `s3:ListBuckets` (permesso IAM `s3:ListAllMyBuckets`) per l'elenco dei bucket. Con `--bucket` l'elenco non viene chiesto. Senza il permesso `config init` lo dice e chiede di digitare il nome; l'elenco serve solo a comodità, vedi `docs/iam-policy.md` (`OptionalListBuckets`).
- Un target con lo stesso nome non viene toccato senza `--replace`.
- Default: regione del profilo (altrimenti `us-east-1`), profilo `$AWS_PROFILE` (altrimenti `default`), modalità `tunnel` (tunnel SSM integrato; con `--mode external` la porta `--local-port` vale `11433`), nome ricavato dall'identificativo dell'istanza.

> Novità della 0.3.0: nella 0.2.0 `config init` e `config add-target` avevano `external` come modalità predefinita; ora è `tunnel`, come in *Add instance* dell'app. Un target **già nel file** senza `connect.mode` resta `external` (§4.2): il default di `tunnel` vale solo per i target che questi comandi scrivono.

#### 4.1.2 `dialga config add-target` e `remove-target`

```bash
dialga config add-target <nome> --instance I --account-id 123456789012 --bucket B --sql-user U [--region R] [--profile P] \
  [--password prompt|env:VAR] [--mode tunnel|external|direct] [--local-port N] [--protected] [--replace]
dialga config remove-target <nome> [--confirm <nome>]
```

- `add-target` **non chiama AWS**: per un target nuovo servono `--instance`, `--account-id`, `--bucket` e `--sql-user`. Con `--replace` un target esistente cambia solo nei campi indicati.
- `remove-target` su un terminale chiede di riscrivere il nome; senza terminale serve `--confirm <nome>` (`the confirmation does not match the target name: nothing removed` se non coincide). Rimuovere l'ultimo target rimuove il file.
- Tutte e tre le scritture sono **atomiche** (file temporaneo e rinomina) con permessi **`0600`**. I **commenti del file non si conservano**; la versione precedente resta accanto al file come `config.yaml.bak`. Nel file non finisce mai una password, solo il riferimento (`prompt`, `env:VAR`).

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

| `sql.password` | Da dove arriva | Memorizzabile |
|---|---|---|
| `prompt` (default) | La memoria dell'engine (digitata nell'app), poi `DIALGA_SQL_PASSWORD`, poi il portachiavi se l'hai ricordata; altrimenti la chiede (terminale o app) | Sì, nel portachiavi |
| `env:VAR` | Variabile d'ambiente `VAR` | No: viene dall'ambiente |
| `keyring` | Voce del portachiavi di sistema (servizio `dialga`, account = nome del target) | Sì: `dialga password set <target>` |
| `secretsmanager:<ARN>` | AWS Secrets Manager, con profilo e regione del target (stesso controllo dell'account) | No: viene da AWS |

Per `secretsmanager` il segreto è un JSON con il campo `password` (come i segreti gestiti da RDS) oppure una stringa semplice. Serve il permesso `secretsmanager:GetSecretValue` sul segreto, e `kms:Decrypt` se usa una chiave gestita dal cliente (`docs/iam-policy.md`).

**CLI.** Con `prompt` la password viene chiesta sul terminale se nessuna fonte la fornisce; prima Dialga prova `DIALGA_SQL_PASSWORD` e il portachiavi. Per memorizzarla:

```bash
dialga password set dev        # chiede senza eco (due volte), oppure legge una riga da stdin
dialga password status         # per ogni target: fonte e voce del portachiavi (stored, empty, unavailable)
dialga password forget dev     # rimuove la voce
```

`dialga password status` non mostra mai la password. Il portachiavi è quello dell'OS: Secret Service su Linux (GNOME Keyring, KWallet), Keychain su macOS, Credential Manager su Windows. Senza un servizio attivo (sessione remota, headless) `set` fallisce e `status` dice `unavailable`: usa `env:VAR` o `secretsmanager:`.

**App desktop.** Con `prompt` o `keyring` e nessuna password disponibile l'app la chiede e può ricordarla nel portachiavi di sistema; altrimenti la tiene solo in memoria fino alla chiusura. Con `env:VAR` e `secretsmanager:` l'app non chiede nulla (la password non si può digitare). L'app può anche dimenticarla (voce del portachiavi compresa). In alternativa, avviala con la variabile:

```bash
read -rs DIALGA_SQL_PASSWORD && export DIALGA_SQL_PASSWORD && dialga-desktop
```

`read -rs` non mostra la password e non la lascia nella history. La password digitata nell'app viene dimenticata se cambiano istanza, account, regione o `sql.user` del target, o se lo rimuovi. Non viene mai scritta su file, nel journal dei job, negli eventi o nei log. Senza password i controlli SQL vengono saltati e lo si vede nei risultati (§7.2).

### 4.4 Modalità di connessione

| `connect.mode` | Quando usarla | Cosa fa Dialga |
|---|---|---|
| `external` | Hai già un tunnel aperto (per esempio un altro `dialga tunnel` o uno script di port forwarding SSM) | Si collega a `127.0.0.1:<local_port>` |
| `direct` | La workstation è dentro il VPC | Si collega all'endpoint RDS |
| `tunnel` | Caso normale, workstation fuori dal VPC | Apre da sé il tunnel SSM attraverso il bridge. Un comando della CLI lo apre, lo usa e lo chiude alla fine; l'app lo tiene condiviso (sotto) |

In tutte le modalità il certificato TLS di SQL Server viene **verificato** sul nome dell'endpoint RDS, anche attraverso il tunnel: non serve *Trust server certificate*.

Chi usa la porta locale, in modalità `tunnel`:

| Chi | Porta locale | Quando si chiude |
|---|---|---|
| Un comando singolo della CLI (`check`, `status`, `restore`, …) | **Sempre una porta libera**, anche se `connect.local_port` è impostato | Alla fine del comando (anche con Ctrl-C, SIGTERM, SIGHUP o una pipe chiusa) |
| `dialga tunnel` | `--port`, altrimenti `connect.local_port`, altrimenti una libera | Ctrl-C o SIGTERM |
| App desktop | `connect.local_port` (`0` o assente = una libera): **un solo tunnel per target**, condiviso da panoramica, check, database e operazioni | Si chiude da solo dopo **10 minuti** senza utilizzatori se è stato aperto dall'uso; resta aperto finché non lo disconnetti se l'hai aperto con *Connect*; si chiude comunque all'uscita dall'app (§6.1) |

> Novità della 0.3.0: un **job** apre **un solo tunnel per target**, non uno per database. Nella CLI (`dialga import`, `dialga run`, `dialga jobs resume`) il tunnel è del job: lo usano il controllo preliminare e tutti i database, e si chiude alla fine del job (anche con Ctrl-C). Nell'app i job prendono in prestito il tunnel condiviso del target (quello dell'indicatore): uno solo per target, anche con più job insieme.

Ogni comando singolo apre la sua sessione SSM (qualche secondo in più). Se lavori a lungo con un client SQL o con molti comandi di fila, tieni aperto un tunnel con `dialga tunnel` (§5.5) e usa un target `external` sulla stessa porta. L'indicatore del tunnel dell'app è nel §6.3.

### 4.5 Target protetti

`protected: true` marca un'istanza di produzione: l'app mostra il badge **Protected** sulla scheda e *Protected target* nelle finestre di conferma. Ogni modifica su un target protetto (`restore`, `export`, `finish`, `cancel`, `drop`, `upload`, `download`) chiede di **riscrivere il nome del target**; su un target non protetto la conferma serve solo per le azioni distruttive: `drop` chiede il nome del database, un upload che sostituisce un oggetto o un download che sostituisce un file chiedono il testo indicato dal piano. I dettagli della conferma sono nel §5.10.

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
| `2` | Il comando è andato, ma una verifica non è passata (`check`, `inspect`, `doctor`; i controlli preliminari di `restore`, `export`, `finish`, `cancel`, `drop`; `post`; qualche file rifiutato da `upload`/`download`) |
| `3` | Il task RDS è finito in `ERROR` o `CANCELLED` (`wait`, `restore`, `export`, `finish`, `cancel`, `drop`) |

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
| `dialga config init` | Aggiunge un target in modo guidato (§4.1.1) |
| `dialga config add-target <nome>` | Aggiunge un target dalle opzioni, senza chiamate AWS (§4.1.2) |
| `dialga config remove-target <nome>` | Rimuove un target, chiede di riscriverne il nome (§4.1.2) |

Esempi di `validate` e `show` nel §4.6.

### 5.9 `dialga version` e `dialga completion`

```bash
$ dialga version
dialga X.Y.Z
$ dialga completion bash|zsh|fish|powershell   # shell completion script
```

Il pacchetto stampa la versione del rilascio; una build locale di sviluppo stampa `dialga dev`.

**Avviso di nuova versione.** Se sul mirror pubblico GitHub (`fpellizz/dialga`) c'è una release più recente, `dialga version` aggiunge una riga:

```bash
$ dialga version
dialga 0.6.5
Dialga 0.7.0 is available: https://github.com/fpellizz/dialga/releases/tag/v0.7.0
```

- La risposta è in cache per 12 ore (cartella cache dell'utente, `dialga/update-check.json`); il controllo non fa mai fallire il comando e lo rallenta al massimo di 3 secondi; se è offline o GitHub non risponde, la riga non compare.
- Una build `dev` (o con una versione che non è `X.Y.Z`) non risulta mai indietro. Le prerelease e le bozze sono ignorate.
- `dialga version --check` ignora la cache e stampa sempre il risultato (anche "aggiornato" o "nessuna release pubblicata"); offline esce comunque con 0 e l'errore va su stderr.
- Con `-o json` l'output è `{"version": …, "update": {…}}` (oggetto `update` come `GET /api/v1/update`).
- `DIALGA_NO_UPDATE_CHECK=1` spegne il controllo automatico (`version` e `serve`); `version --check`, chiesto esplicitamente, funziona comunque.
- L'unica richiesta è una GET non autenticata a `api.github.com`: non viene inviato altro che lo `User-Agent` `dialga/<versione>`.

### 5.10 Import ed export

I comandi che modificano qualcosa (`upload`, `download`, `restore`, `export`, `finish`, `cancel`, `drop`) seguono tutti la stessa regola.

| Regola | Come funziona |
|---|---|
| **Anteprima per default** | Senza `--apply` stampano i controlli preliminari e il piano (ogni chiamata S3 o ogni istruzione T-SQL, identica a quella che girerà) e non cambiano nulla. Chiudono con `Rerun with --apply to run it.` |
| **`--apply`** | Esegue il piano. Se un controllo preliminare è `FAIL` non parte nulla (`Some pre-flight checks failed: nothing was changed.`, exit `2`) |
| **Conferma da riscrivere** | Su un target `protected` il piano chiede il **nome del target**; `drop` chiede il **nome del database**; un upload o un download che **sostituisce** qualcosa chiede il testo indicato dal piano (`Running it asks to retype "…"`). Su un terminale lo chiede; senza terminale va passato con `--confirm <testo>`. Un testo diverso ferma tutto: `not confirmed: the text does not match (expected "…"): nothing was changed` |
| **Impronta del piano** | Il piano ha un'impronta (SHA-256) che l'app confronta con quella dell'anteprima: se nel frattempo il piano è cambiato, non parte (§6.4, §7.7). Con `-o json` l'anteprima include `digest` e `confirmation` |
| **`-o json`** | Un solo documento JSON alla fine (`target`, `instance`, `operation`, `ready`, `checks`, `plan`, `digest`, `applied`, `steps`, `task`); per `upload`/`download` gli eventi `step` e `progress` escono su stderr come righe JSON |

#### 5.10.1 `dialga upload`

Carica uno o più `.bak` nel bucket del target, per default sotto il prefisso `import` e con il nome del file come chiave.

```bash
dialga upload <file.bak>... [-t target] [--prefix P] [--key K] [--overwrite] [--concurrency 1-32] [--apply] [--confirm testo]
```

| Opzione | Significato |
|---|---|
| `--prefix P` | Prefisso della chiave al posto di quello `import` del target (`""` = radice del bucket) |
| `--key K` | Chiave dell'oggetto (solo con un file) |
| `--overwrite` | Sostituisce gli oggetti già presenti in S3. Senza, un file che esiste già viene **rifiutato** e il piano lo dice (`already in s3://… : choose overwrite to replace it`); un oggetto non viene mai sostituito di nascosto (S3 riceve `If-None-Match: *`) |
| `--concurrency N` | Parti in volo insieme (default `4`, da 1 a 32) |

- **Multipart**: le parti sono dimensionate sul file (da 5 MiB, massimo 10 000 parti), ogni parte è ritentata da sola e ha un timeout.
- **Checksum**: CRC64NVME, che S3 verifica; alla fine l'oggetto viene riletto (dimensione e checksum) e, se non coincidono, l'upload risulta fallito.
- **Ctrl-C** (come SIGTERM, SIGHUP e una pipe chiusa) **annulla** l'upload: l'upload multipart viene abortito e **in S3 non resta nulla**. Se anche l'abort fallisce, l'errore stampa il comando `aws s3api abort-multipart-upload …` da lanciare.
- **Pausa e ripresa**: l'engine sa mettere in pausa un upload tenendo le parti già in S3 e completarlo poi (`ListParts` su S3 è la verità). Nella v0.2.0 questa funzione non ha un comando né un pulsante: la usa la coda dei job, in arrivo (§6.9): un upload di un job interrotto si riprende da *Resume* in Activity o da `dialga jobs resume` (§5.14). Da `dialga upload` un upload interrotto si rifà da capo.
- File che non possono partire (illeggibile, non regolare, nome con caratteri non ammessi, stessa chiave di un altro file, oggetto già presente senza `--overwrite`) sono elencati con il motivo e **gli altri vengono caricati**: exit `2`.

#### 5.10.2 `dialga download`

Scarica un oggetto del bucket del target.

```bash
dialga download <chiave|s3://bucket/chiave|nome> [dest] [-t target] [--overwrite] [--concurrency 1-32] [--apply] [--confirm testo]
```

- Il primo argomento è una chiave, un URL `s3://` sul bucket del target, oppure un nome semplice, cercato sotto il prefisso `export`. `dest` è un file o una cartella esistente (default: la cartella corrente).
- GET parallele per intervalli (16 MiB) in un file temporaneo che diventa `dest` solo a download completo; il checksum CRC dell'oggetto è verificato quando esiste (`checksum mismatch: … the file was not kept` se non coincide). Prima si controlla lo spazio libero.
- Un file locale che esiste già non viene sostituito senza `--overwrite`. Ctrl-C ferma il download e non lascia nulla.
- Exit: `0` fatto, `2` il file è stato rifiutato (non esiste in S3, `dest` esiste già…), `1` errore.

#### 5.10.3 `dialga restore`

Ripristina un `.bak` del bucket in un **nuovo** database (o, con `--replace`, sopra uno esistente) con `rds_restore_database`, poi segue il task fino alla fine.

```bash
dialga restore <db> <chiave> [-t target] [--norecovery] [--differential] [--no-wait] [--interval 30s] [--apply] [--confirm testo]
```

- `<chiave>`: un nome semplice è cercato sotto il prefisso `import` e poi nella radice del bucket; una chiave con `/` è presa com'è; `s3://<bucket>/<chiave>` deve indicare il bucket del target. Un `*` ripristina un backup spezzato su più file (al massimo 10).
- `--norecovery` lascia il database in `RESTORING` per un successivo restore differenziale; `--differential` ripristina un differenziale su un database lasciato `RESTORING`; `dialga finish` conclude.
- `--no-wait` avvia il task nativo e torna subito (`Follow it with: dialga wait <id> -t <target>`).
- **Avviso spazio libero** (nei controlli preliminari, `WARN`): se il backup occupa più dell'80% dello spazio libero dell'istanza, o più di tutto lo spazio libero, il restore parte ugualmente ma l'avviso ricorda che il database ripristinato occupa **la somma dei suoi file**, non la dimensione del `.bak` (`dialga inspect` la dà). Con lo spazio non leggibile da CloudWatch l'avviso lo dice.
- Alla fine di un restore riuscito stampa `Next: dialga post <db> -t <target>` (con `--norecovery`, come proseguire).
- `--replace auto|swap|drop` e `--no-backup-first`: restore su un database che esiste già, vedi **Restore su un database esistente** qui sotto.

L'anteprima stampa `Pre-flight checks of restore on <target> (instance <istanza>):` con un esito per controllo, poi `Dry run: restore on <target> (instance <istanza>). Nothing is changed.` e le azioni numerate, ognuna con la sua istruzione T-SQL.

**Restore su un database esistente**

RDS rifiuta un restore nativo sopra un database che esiste già, e per questo `dialga restore` (e `dialga import`) senza opzioni lo rifiuta con "choose another name or drop it first". Con `--replace` lo si fa esplicitamente, in due modi:

| Strategia | Che cosa fa | Che cosa ti resta |
|---|---|---|
| `swap` | Ripristina il backup in un database temporaneo (`<db>_dialga_new_<stamp>`), poi scambia i nomi: il database corrente diventa `<db>_old_<stamp>` e quello nuovo prende il nome `<db>`. Il database corrente non viene mai toccato finché il restore non è finito. | Il database vecchio, intatto: lo elimini tu con `dialga drop <db>_old_<stamp>` quando non serve più |
| `drop` | Salva il database corrente su S3 (`<prefisso export>/<db>_before_replace_<stamp>.bak`), lo elimina e ripristina il backup al suo posto. Se il restore fallisce, il database non c'è più: l'errore indica il backup. | Il backup di sicurezza su S3 |
| `auto` | Sceglie da solo (vedi sotto) e dice perché | Come la strategia scelta |

**Regola dello spazio di `auto`**: `swap` richiede due copie sull'istanza insieme. Con `auto`, se `2 × dimensione` sta nello spazio libero dell'istanza sceglie `swap`; altrimenti `drop`, **sempre con il backup su S3**. La dimensione è quella del backup (o, se non è nota, dei file dati del database corrente). Se lo spazio libero o la dimensione non si leggono, sceglie `swap`, la via sicura: se lo spazio finisce, il restore fallisce e nulla è cambiato. L'anteprima mostra la scelta, ad esempio `swap: two copies (2 × 42.0 GiB) fit in 97.2 GiB free` oppure `drop: not enough free storage for two copies (needs 84.0 GiB, 61.3 GiB free); the current database is backed up to S3 first`.

**Backup prima di `drop`**: di default `drop` salva prima il database su S3 (`backupFirst`). `--no-backup-first` lo salta: il database corrente è perso per sempre, e l'anteprima lo segnala con un `WARN`. Con `auto` il backup c'è sempre quando sceglie `drop`.

**Sicurezza**: è un'operazione distruttiva, quindi vale tutto il modello di sempre: dry run di default, controlli in sola lettura, T-SQL esatto nell'anteprima, e `--apply` chiede di riscrivere il **nome del database** (il nome del target, se il target è protetto). I controlli avvisano delle sessioni attive sul database (saranno disconnesse) e verificano che i nomi temporanei e il backup di sicurezza siano liberi. Un replace segue i task fino alla fine: `--no-wait` non è ammesso. Le rinomine usano `rdsadmin.dbo.rds_modify_db_name`, verificata su `dev-sql` il 2026-10-19 (`@old_db_name`, `@new_db_name`).

```bash
dialga restore sales sales.bak -t dev-sql --replace auto            # anteprima
dialga restore sales sales.bak -t dev-sql --replace drop --apply    # con backup di sicurezza
```

#### 5.10.4 `dialga export`

Esegue il backup di un database nel bucket con `rds_backup_database`, poi segue il task.

```bash
dialga export <db> [chiave] [-t target] [--no-wait] [--interval 30s] [--apply] [--confirm testo]
```

La chiave di default è `<prefisso export>/<db>_<AAAAMMGG-HHMMSS>.bak` (UTC); un nome semplice va sotto il prefisso `export`. **Non sovrascrive mai un oggetto** esistente. Per portare il file sulla workstation: `dialga download`.

#### 5.10.5 `dialga finish`, `cancel`, `drop`

| Comando | Cosa fa | Note |
|---|---|---|
| `dialga finish <db>` | Porta online un database lasciato `RESTORING` da `restore --norecovery` (`rds_finish_restore`) | Dopo l'ultimo differenziale |
| `dialga cancel <task-id>` | Chiede a RDS di annullare un task nativo non finito (`rds_cancel_task`) | RDS lo ferma al passo successivo: `cancel already requested` (`WARN`) se già richiesto |
| `dialga drop <db>` | Elimina un database (`rds_drop_database`): un restore fallito, una copia di prova | **Non si annulla.** `--apply` chiede di riscrivere il nome del database, anche su un target non protetto. Avvisa se ci sono task nativi in corso su quel database |

Tutti accettano `-t`, `--apply` e `--confirm`.

#### 5.10.6 `dialga post`

Report di un database ripristinato, **in sola lettura**.

```bash
dialga post <db> [-t target] [-o json]
```

Mostra proprietà, file, **se il login SQL può entrare** nel database, gli **utenti SQL orfani** (senza login sull'istanza), gli utenti Windows e gli oggetti per tipo. Per ogni utente orfano suggerisce di creare il login e di associargli l'utente:

```sql
CREATE LOGIN [utente] WITH PASSWORD = '...';
ALTER USER [utente] WITH LOGIN = [utente];
```

| Verdetto | Esito |
|---|---|
| Database `ONLINE`, login che entra, nessun orfano | `OK` |
| Utenti orfani; utenti o gruppi Windows (inutilizzabili su RDS senza Active Directory) | `WARN` |
| Database mancante o non online (`RESTORING`: serve `dialga finish`); il login non entra (i database ripristinati sono di `rdsa`) | `FAIL`, e utenti e oggetti non vengono letti |

Exit: `0` OK (anche con avvisi), `2` database mancante o non online, o login che non entra, `1` errore.

#### 5.10.7 Cosa continua a girare su RDS dopo Ctrl-C

Un task nativo (`restore`, `export`, `finish`, `drop`) è eseguito da RDS, non da Dialga. Dopo l'`--apply`:

- **Ctrl-C** (o SIGTERM, SIGHUP, una pipe chiusa) ferma solo l'**attesa**: `wait interrupted: task N keeps running on the instance`. Il task **continua sull'istanza** fino alla fine.
- L'istruzione `rds_*` già inviata non viene interrotta: Dialga la lascia finire. Quello che non parte più sono i passi successivi (per esempio l'attesa).
- Per riprendere a seguirlo: `dialga wait <task-id> -t <target>`. Per fermarlo davvero: `dialga cancel <task-id>`.
- Un upload o un download, invece, **si fermano** con Ctrl-C e non lasciano nulla (§5.10.1, §5.10.2).

### 5.11 `dialga doctor`

Controlla **questo computer**, un verdetto per voce, **senza chiamate AWS** (`dialga check` verifica un target contro AWS).

```bash
dialga doctor [-o json]
```

| Voce | Cosa guarda |
|---|---|
| *Session Manager plugin* | Presenza e versione (serve una versione successiva alla 1.2.497.0) |
| *Container runtime* | podman o docker (serve a `inspect`) |
| *AWS profiles* | Profili in `~/.aws/config` e `~/.aws/credentials` |
| *Configuration* | Il file di configurazione: esiste, quanti target ha (se manca: `add a target with dialga config init`) |
| *Target <nome>* | Profilo AWS presente sul computer e origine della password SQL (`WARN` se la variabile indicata è vuota) |

Per le voci mancanti stampa il comando di installazione per la tua distribuzione (Fedora, Debian/Ubuntu). Chiude con `Ready.`, `Ready, with warnings.` oppure `Not ready: fix the items marked FAIL.` Exit: `0` pronto (anche con avvisi), `2` qualcosa da correggere, `1` errore.

### 5.12 `dialga import`

Importa uno o più `.bak` come **un solo job**: per ogni database `upload` su S3 (multipart, checksum verificato) → `restore` → attesa del task nativo → report `post` in sola lettura (non con `--norecovery`). I database avanzano **in parallelo**, nei limiti di `max_tasks` del target; un database che fallisce non ferma gli altri.

```bash
dialga import <file.bak>... [-t target] [--db nome,...] [--key K | --prefix P] [--differential] [--norecovery] \
  [--overwrite] [--skip-failing] [--interval 30s] [--apply] [--confirm testo]
```

| Opzione | Significato |
|---|---|
| `--db` | Nomi dei database, **uno per file** nell'ordine (default: il nome del file senza estensione, `sales.bak` → `sales`). Se il nome non si ricava dal file: `cannot name the database after …: pass --db` |
| `--key K` | Chiave S3 dell'upload, solo con **un** file (con più file: `--key names the object of one file: use --prefix for several`) |
| `--prefix P` | Prefisso al posto di quello `import` del target (`""` = radice del bucket) |
| `--differential` | Backup differenziali, su database lasciati `RESTORING` |
| `--norecovery` | Lascia i database `RESTORING` (segue un differenziale); niente report `post` |
| `--overwrite` | Sostituisce gli oggetti già presenti in S3 (richiede conferma) |
| `--skip-failing` | Lascia fuori i database il cui controllo preliminare fallisce, invece di rifiutare tutto il job |
| `--interval` | Intervallo di lettura dei task nativi (default 30 s) |

- **Backup striped** (più file di un solo backup): `dialga import` tratta ogni file come un database; per le stripe serve un piano YAML con `files` (§5.13).
- **Anteprima per default**: controlli preliminari di **tutti** i database e piano; senza `--skip-failing` basta un controllo fallito perché non parta niente. `--apply` avvia il job e lo segue nel terminale.
- **Conferma una sola volta per job**: il nome del target se è `protected`, altrimenti il testo di ciò che viene sostituito (la chiave, oppure `replace N objects`). Su un terminale lo chiede; senza terminale serve `--confirm`.
- **Ctrl-C annulla il job** (anche SIGTERM, SIGHUP, pipe chiusa): gli upload in corso vengono interrotti con abort (in S3 non resta nulla), i database in coda non partono, ma i **task nativi già avviati continuano sull'istanza**. Per fermarli: `dialga cancel <task-id>`.
- Il job gira nel processo di `dialga import` ed **entra nel journal** con origine `cli` (§5.14): compare in `dialga jobs` e nella pagina Activity dell'app, anche mentre l'app è aperta, e se viene interrotto (processo ucciso) si riprende con `dialga jobs resume`. Un `--apply` rifiutato (controlli falliti, conferma sbagliata) vi lascia un job `failed` con il motivo. Un job tra più database è limitato a 50 database; i trasferimenti S3 contemporanei sono al massimo 3.
- Con `-o json`: un documento alla fine (`target`, `applied`, `ready`, `plan`, `job`); con `--apply` gli eventi del job vanno anche su stderr, una riga JSON ciascuno (`{"event": <tipo>, "data": …}`).

| Exit code | Significato |
|---|---|
| `0` | Anteprima pronta, oppure job completato |
| `2` | Controllo preliminare fallito: non è partito niente |
| `3` | Un task nativo è finito in `ERROR` o `CANCELLED` |
| `1` | Errore, job fallito o annullato |

### 5.13 `dialga run`

Esegue un job di import **o di export** di più database descritto da un **piano YAML**.

```bash
dialga run <piano.yaml> [-t target] [--skip-failing] [--interval 30s] [--apply] [--confirm testo]
```

Schema del piano (`apiVersion: v1`, `kind: Import` o `Export`, `target`, `items`), controlli preliminari e API: `docs/piani-job.md`. Punti da sapere:

- La lettura è **stretta**: una chiave sconosciuta è un errore. I percorsi relativi partono dalla cartella del piano.
- `-t` serve solo se il piano non ha `target`; se il piano lo ha e `-t` dice un altro target, è un errore.
- Opzioni e comportamento dopo l'avvio (anteprima, conferma unica, Ctrl-C, exit code `0`/`2`/`3`/`1`, `-o json`) sono quelli di `dialga import` (§5.12).
- Un `kind: Export` con `destination` scarica il backup dopo l'export; senza, il backup resta in S3.

### 5.14 `dialga jobs`

Ogni operazione è un **job** e finisce in un **journal** su disco: `$XDG_STATE_HOME/dialga/jobs`, oppure `~/.local/state/dialga/jobs` (Linux). Ci scrivono sia l'**app desktop** (origine `app`) sia i comandi che cambiano qualcosa con `--apply` (origine `cli`): `import`, `run`, `restore`, `export`, `drop`, `finish`, `cancel`. Ogni processo scrive **solo i propri job**, un file per job, quindi app e CLI possono lavorare insieme. `dialga jobs` legge il journal **in sola lettura**, anche con l'app aperta.

| Comando | Cosa fa |
|---|---|
| `dialga jobs` | Elenco dei job, i più recenti per primi: in corso e storico (gli ultimi 200, al massimo 30 giorni, di tutte le origini). Colonne: tipo (`import`, `export` o l'operazione: `restore`, `drop`, `finish`, `cancel`, `post`), origine (`app`/`cli`), target, database |
| `dialga jobs show <job-id> [--events N]` | Database del job con i loro passi, avanzamento, task nativi, ciò che serve a una ripresa, poi gli ultimi `N` eventi (default 20, `0` = tutti); `-o json` stampa il job intero |
| `dialga jobs resume <job-id> [--confirm <target>]` | Riprende un job **interrotto** in questo terminale |

Tutti accettano `--dir <cartella>` per un altro journal. Non esiste `dialga jobs cancel`: un job lanciato da CLI si annulla con Ctrl-C nel suo terminale (resta `cancelled`); uno dell'app con *Cancel job* in Activity (§6.9). Un job che gira in un **altro processo** si vede e si segue, ma non lo si ferma da qui: Activity risponde 409 (`the job runs in another Dialga process: stop it there`).

**Chi possiede un job**: finché un job gira, il processo che lo esegue tiene un lock (`.<id>.lock` nella cartella del journal; il sistema lo rilascia se il processo muore). Un job non finito il cui lock è libero è **orfano**: il primo processo che lo trova lo segna `interrupted`. Un job non finito con il lock preso gira altrove e **non si tocca**, quindi aprire l'app mentre un job CLI è in corso non lo rovina e nessun job va perso. I job rifiutati sono già finiti quando vengono scritti. Il report di un database (§6.4) è una sola lettura e non lascia job.

**`dialga jobs resume`**:

- Solo un job `interrupted` (l'engine si è fermato mentre girava, per esempio l'app chiusa o uccisa). Gli altri stati: `job <id> is <stato>: only an interrupted job can be resumed`.
- I database già finiti e i passi riusciti restano com'erano; gli altri ripartono da ciò che il journal ha tenuto: un upload **continua il suo multipart** (le parti già in S3 non si rimandano), un task nativo già in coda **viene seguito, non rimesso in coda**. Il job tiene lo stesso id e stampa i suoi eventi finché finisce.
- **Rifiutato se un altro processo lo esegue o lo sta riprendendo** (`the job is in use by another Dialga process: …`): ogni job ha un solo proprietario alla volta, quindi se l'app (o un altro `dialga jobs resume`) lo ha già ripreso, il job va avanti lì: seguilo da Activity. Se nessuno lo tiene, la ripresa dalla CLI funziona anche con l'app aperta.
- Il target deve essere ancora nella configurazione e puntare alla **stessa istanza** del job.
- Su un target `protected` chiede di riscrivere il nome del target (`--confirm <target>` senza terminale).
- Ctrl-C **interrompe di nuovo** il job: si potrà riprendere ancora. Exit: `0` job riuscito, `3` finito `failed` o `cancelled`, `1` errore.

### 5.15 `dialga access`

Dà a un login SQL l'accesso a un database, glielo toglie e lega un utente orfano al suo login. Tutto è una **dry run** finché non c'è `--apply`; le scritture girano come job (§5.14, Activity).

| Comando | Cosa fa |
|---|---|
| `dialga access list <db>` | Utenti del database: login, livello, backup, ruoli, nota (orfano, non gestito da Dialga). Sola lettura |
| `dialga access logins` | Login SQL dell'istanza, senza `sa`, `rdsa` e `##…##`. Il master user del target è marcato: non viene mai toccato. Sola lettura |
| `dialga access grant <db> <login> --level …` | Dà accesso. Se il database non ha l'utente del login, lo crea con lo stesso nome; se c'è un utente orfano con quel nome, lo lega al login e ne tiene i ruoli |
| `dialga access revoke <db> <login> --remove …` | Toglie livello, backup o utente |
| `dialga access fix-orphan <db> <user> [--login <login>]` | Lega un utente orfano (login assente, tipico dopo un restore da un'altra istanza) al login con lo stesso nome, o a `--login` |

**Livelli** (`--level`):

| Livello | Ruoli nel database |
|---|---|
| `read` | `db_datareader` |
| `readwrite` | `db_datareader` + `db_datawriter` |
| `owner` | `db_owner` |

**`--backup`** (con `grant`) aggiunge `db_backupoperator` nel database e, in msdb, i permessi di `rds_backup_database` e `rds_task_status` e `SELECT` su `rds_fn_task_status`, così chi usa quel login può lanciare `exec msdb.dbo.rds_backup_database` con i suoi strumenti (SSMS, `sqlcmd`) e seguirne il task. Dialga invece si collega sempre con l'utente SQL del target. Un `db_owner` può già fare il backup del suo database: gli basta la parte in msdb, e `list` lo segna *partial* finché non c'è. `rds_cancel_task` non viene concesso di proposito. Il backup scrive nei bucket a cui può accedere il **ruolo IAM dell'istanza**, non l'utente SQL.

**`--remove`** (con `revoke`):

| Valore | Effetto |
|---|---|
| `level` | Toglie `db_datareader`, `db_datawriter` e `db_owner` |
| `backup` | Toglie `db_backupoperator`. I permessi in msdb si tolgono solo se nessun altro database lascia fare il backup a quel login |
| `user` | Elimina l'utente dal database. **Distruttivo**: `--apply` chiede di riscrivere il nome del login. Fallisce se l'utente possiede schemi |

Il login non viene mai eliminato. Non vengono mai toccati: il master user del target, `sa`, `rdsa`, i `##…##`, `dbo`, `guest`.

**Nuovo login** (`grant --create-login`): il login viene creato con `CHECK_POLICY ON` e senza scadenza. Con `--apply` serve la password, in uno di due modi:

- `--generate-password`: Dialga ne genera una di 20 caratteri e la stampa **una sola volta** su stderr, dopo che il login esiste (nel JSON, con `-o json`, nel campo `generatedPassword`). Non compare mai in dry run.
- `--password-stdin`: legge **una riga** da stdin. Non passare mai la password come argomento del comando.

La password deve avere almeno 12 caratteri e tre classi su quattro (maiuscole, minuscole, cifre, simboli). Non viene salvata nel journal né mostrata nell'anteprima, dove compare come `****`.

**Esempi**

```bash
# Chi ha accesso al database (sola lettura)
dialga access list <db> -t <target>

# Dry run: anteprima con il T-SQL esatto, nessuna modifica
dialga access grant <db> <login> --level readwrite -t <target>

# Esecuzione
dialga access grant <db> <login> --level readwrite -t <target> --apply

# Login nuovo, password generata e stampata una volta
dialga access grant <db> <nuovo-login> --level read --create-login --apply --generate-password -t <target>

# Login nuovo, password da una riga di stdin
read -rs PW && printf '%s\n' "$PW" | dialga access grant <db> <nuovo-login> --level read --create-login --apply --password-stdin -t <target>
unset PW

# Accesso con backup, poi revoca del solo backup
dialga access grant <db> <login> --level read --backup --apply -t <target>
dialga access revoke <db> <login> --remove backup --apply -t <target>

# Utente orfano dopo un restore: legarlo al login con lo stesso nome
dialga access fix-orphan <db> <user> --apply -t <target>
```

Exit code come in §5.1. `access list` esce con `2` se il database non esiste.

## 6. App desktop

### 6.1 Avvio

- Dal menu: **Dialga**. Da terminale: `dialga-desktop` (serve per passare la password SQL, §4.3, e per vedere i messaggi dell'engine, che compaiono con il prefisso `engine:`).
- L'app avvia l'engine (`dialga serve`) in background. All'uscita lo ferma **con garbo**: gli chiede di chiudersi, così chiude i suoi tunnel SSM e termina le sessioni, e aspetta fino a 10 secondi prima di forzarlo. Un engine forzato (o un'app uccisa con `kill -9`) lascia le sessioni SSM aperte fino al timeout di inattività (§7.3). In basso nella barra laterale c'è il suo stato: `engine <versione>`, *Starting engine…* oppure *Engine not available*.
- Lingua dell'interfaccia: **inglese** di default, **italiano** in *Settings → Language*. Qui sotto le etichette sono in inglese, con l'italiano tra parentesi.

### 6.2 Barra laterale

| Voce | Contenuto |
|---|---|
| **Instances** (Istanze) | Una scheda per target (§6.3) |
| **Databases** (Database) | Database utente di un'istanza (§6.4) |
| **Import**, **Export**, **Activity** (Importa, Esporta, Attività) | Disabilitate, con l'etichetta *Soon* (*Presto*) |
| **Settings** (Impostazioni) | Lingua, tema, prerequisiti, target, percorso della configurazione, versione (§6.5) |

In fondo alla barra laterale, sotto lo stato dell'engine, c'è il riepilogo dei tunnel: *1 tunnel open* / *N tunnels open* (*1 tunnel aperto* / *N tunnel aperti*), con *N connecting* (*N in connessione*) mentre se ne apre qualcuno. Il dettaglio di ogni tunnel è sulla scheda dell'istanza (§6.3).

### 6.3 Instances

Una scheda per ogni target della configurazione. Se il file non esiste, la schermata mostra *No instances yet* con il percorso in cui verrà creato e il pulsante *Add instance*; se ha errori, *The configuration file has errors* con il dettaglio.

**Add instance** (*Aggiungi istanza*): finestra che trova un'istanza SQL Server e la salva come target. Non cambia nulla su AWS.

1. *AWS profile* (*Profilo AWS*, da `~/.aws/config` e `~/.aws/credentials`; senza profili la finestra lo dice) e *Region* (*Regione*), poi *Discover instances* (*Cerca le istanze*): mostra l'account e le istanze SQL Server trovate.
2. Scegli l'*Instance* (*Istanza*): un'etichetta dice se ha l'opzione di backup nativo (*Native backup option*) o se manca (*No native backup option*: l'import e l'export falliranno finché non viene aggiunta).
3. Compila *S3 bucket* (un campo con elenco: si carica dall'account con il profilo e la regione scelti, si filtra scrivendo e si sceglie con il mouse o con le frecce e Invio; si può sempre digitare un nome che non è nell'elenco, e vale la stessa validazione; se l'utente AWS non ha `s3:ListAllMyBuckets` la finestra lo dice e il campo resta libero, vedi `docs/iam-policy.md`), *SQL login*, la password (*Ask when needed* oppure *Environment variable*: la password non viene mai salvata, solo dove cercarla), *Connection* (*Open tunnel*, *Direct*, *Integrated tunnel*; con *Open tunnel* anche *Local port*), *Target name* e, per la produzione, *Protected*. *Replace the existing target* sostituisce un target con lo stesso nome.
4. *Save* (*Salva*) scrive il file con le stesse regole di `dialga config init` (§4.1.2) e mostra *Target … saved. Run check to verify it.*

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

**Tunnel** (solo target con `connect.mode: tunnel`): indicatore sulla scheda, sopra la panoramica, con lo stato (*Connecting…*, *Connected*, *Reconnecting…*, *Not connected*, *Connection failed*), il bridge (*via i-…*) e quanti utilizzatori lo usano (*N in use*). È il tunnel **condiviso** del target: uno solo, usato da panoramica, check, database e operazioni (§4.4).

| Pulsante | Effetto |
|---|---|
| *Connect* (*Connetti*) | Apre il tunnel su richiesta: resta aperto finché non lo disconnetti |
| *Disconnect* (*Disconnetti*) | Lo chiude |
| (nessuno) | Un tunnel aperto dall'uso si chiude da solo dopo 10 minuti senza utilizzatori |

**Panoramica** (*overview strip*): si carica solo quando la scheda diventa visibile nella finestra, così le schede fuori schermo non costano chiamate AWS; il resto è descritto sopra.

> In arrivo: pulsanti *Import* ed *Export* sulla scheda, che aprono i wizard sul target.

### 6.4 Databases

- In alto il selettore **Instance** (*Istanza*): l'app ricorda l'ultimo target scelto. Pulsante *Refresh* (*Aggiorna*).
- Tabella: *Name*, *State*, *Size* (dati + log; il tooltip li separa), *Compatibility* (livello e versione SQL Server corrispondente), *Recovery*, *Owner*. Clic sull'intestazione per ordinare (ancora per invertire; *Size* parte dal più grande).
- **Filter** (*Filtra*): cerca in nome, owner, stato e recovery model; il contatore dice *N of M databases*.
- Badge accanto allo stato: *Read-only* (*Sola lettura*), *Single user* (*Utente singolo*), *Restricted* (*Accesso limitato*). Owner in grigio: nessun login ha il SID dell'owner.
- Stati vuoti: *No user databases* (solo database di sistema), *No databases match the filter* (con *Clear filter*), *No instances configured* (con *Go to Instances*).
- **Azioni** (colonna *Actions*, *Azioni*), una per riga, dipendono dallo stato:

  | Azione | Quando | Cosa apre |
  |---|---|---|
  | *Report* | Sempre | Il report di `dialga post` (§5.10.6): *Properties*, *Files*, *Checks* (*Login access*, *Orphaned users*, *Windows users*), l'elenco degli utenti SQL senza login con il comando per ricreare il login, *Objects* per tipo. Se il login non entra, lo dice e non legge utenti e oggetti |
  | *Export* (*Esporta*) | Database `ONLINE` | Il piano di `dialga export` |
  | *Finish restore* (*Concludi ripristino*) | Database `RESTORING` | Il piano di `dialga finish` |
  | *Access…* (*Accessi…*) | Database `ONLINE` | Il pannello degli accessi: concedere, revocare, *Fix orphan* (sotto) |
  | *Drop* (*Elimina*) | Sempre | Il piano di `dialga drop`, distruttivo |

  In alto, il pulsante *Restore from S3* (*Ripristina da S3*) apre il piano di `dialga restore`: nome del database, chiave S3 del backup (con il suggerimento sulla ricerca sotto il prefisso `import`), *Differential backup* e *Leave the database restoring*.
- Quando un piano ha avviato un task, un avviso dice *Task #N keeps running on <target>* con il link *Follow it in Instances*; *Dismiss* lo chiude.
- In caso di errore: *The list is read with a SQL session: the engine needs the SQL password and a reachable instance*. Vedi §7.2 e §7.3.

#### Accessi

Il pulsante *Access…* (*Accessi…*) di ogni riga è disponibile solo per i database `ONLINE` e apre il pannello *Access to <database>*. Mostra gli utenti SQL del database (*Login*, *Level*, *Can back up*, *Roles*):

- Gli **orfani** (login assente) sono evidenziati e offrono *Fix orphan*: lega l'utente al login con lo stesso nome.
- Gli utenti che Dialga non gestisce (`dbo`, il master user, …) sono in sola lettura.
- *Grant access* apre la finestra del piano per dare accesso a un login esistente, oppure a un *New login* con nome e password. *Generate* crea una password di 20 caratteri; *Show password* e *Copy* la mostrano. Dialga non la conserva: va copiata prima di chiudere il dialogo.
- *Revoke…* chiede prima cosa togliere (livello, backup o utente) e poi apre la finestra del piano. Togliere l'utente chiede di riscrivere il nome del login.

Il piano segue le stesse quattro fasi degli altri (§6.4, *La finestra del piano*). Alla chiusura del pannello gli accessi vengono riletti.

#### La finestra del piano

Ogni azione che modifica qualcosa (Restore, Export, Finish, Drop, Cancel) apre la stessa finestra, in quattro momenti.

1. **Anteprima.** Compili i valori e premi *Preview* (*Anteprima*): l'engine esegue i controlli preliminari (*Pre-flight checks*, *Controlli preliminari*) e mostra *What runs* (*Cosa viene eseguito*): le azioni numerate, con il badge *Destructive* (*Distruttiva*) per quelle che non si annullano. Sotto *Details* (*Dettagli*) di ogni azione c'è il **T-SQL esatto** che girerà, lo stesso di `dialga <comando>` senza `--apply`. Se un controllo è fallito la finestra dice *Some pre-flight checks failed: the plan cannot run* e offre *Preview again*.
2. **Conferma.** Se il piano la richiede compare *Type … to confirm* (*Scrivi … per confermare*): il nome del target se è *Protected target*, il nome del database per *Drop* (*This drops … for good: it cannot be undone*). Il pulsante di esecuzione (*Restore*, *Export*, *Finish restore*, *Cancel task*, *Drop database*) si abilita solo con il testo esatto. Se cambi un valore dopo l'anteprima, compare *The values changed: preview again before running* e occorre rifare l'anteprima.
3. **Esecuzione dal vivo.** I passi compaiono mentre avanzano (stessi badge del §6.6); per un restore o un export c'è la barra *Progress* (*Avanzamento*) del task nativo con la percentuale. Prima di partire l'app invia l'**impronta del piano** vista nell'anteprima: se la situazione è cambiata nel frattempo, non parte e dice *The situation changed since the preview: review the new preview before running*.
4. **Esito.** Messaggio finale per operazione (restore completato con rimando al report, backup completato con l'URI `s3://…`, database eliminato, …). Se il task è finito in errore: *The task ended as … Read its log*.

**Chiudere la finestra durante un task smette di seguirlo, non lo ferma**: il task continua sull'istanza (§5.10.7). Si segue dal pannello dei task della scheda (§6.3) o con `dialga wait <id> -t <target>`.

**Cancel su un task in corso**: nel pannello *Show tasks* (§6.3) ogni task in coda (*Queued*) o in corso (*In progress*) ha il pulsante *Cancel*; apre il piano *Cancel task #N* (`rds_cancel_task`), con la stessa finestra. L'annullamento è una richiesta: RDS ferma il task al passo successivo (badge *Cancelling*, poi *Cancelled*).

### 6.5 Settings

| Sezione | Contenuto |
|---|---|
| *Appearance* (Aspetto) | *Language*: English, Italiano. *Theme*: System, Light, Dark (Sistema, Chiaro, Scuro). Le scelte restano su questo computer |
| *Prerequisites* (Prerequisiti) | Una riga per *Session Manager plugin* e *Container runtime* (come `dialga doctor`, §5.11): *Found* (*Trovato*), *Missing* (*Mancante*) o *Update needed* (*Da aggiornare*, plugin troppo vecchio per il tunnel integrato). Per ciò che manca c'è *Install on <distribuzione>* con il comando da copiare; *Check again* (*Controlla di nuovo*) rilegge |
| *Targets* (Target) | Le istanze del file di configurazione. *Remove* (*Rimuovi*) chiede di scrivere il nome del target (*Type … to confirm*) e lo toglie dal file: su AWS non cambia nulla |
| *Updates* (Aggiornamenti) | *Check for updates* (*Controlla gli aggiornamenti*), **attivo di default**: all'avvio e ogni 12 ore l'app chiede all'engine se esiste una release più recente (una GET a `api.github.com`, nient'altro). Sotto: *Last checked …* (*Ultimo controllo …*) e *Check now* (*Controlla ora*, ignora la cache e dice l'esito). Se c'è una versione nuova compare in fondo alla barra laterale, sopra lo stato dell'engine, *Version X.Y.Z available* con *View release* (apre la pagina della release nel browser di sistema) e *Dismiss* (nasconde l'avviso per quella versione; una versione ancora più nuova lo riporta). A barra compressa resta un'icona con nome accessibile. La stessa informazione è in *About*. La scelta resta su questo computer; per la CLI vedi `DIALGA_NO_UPDATE_CHECK` (§5.9) |
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

### 6.7 Wizard Import

Voce **Import** (*Importa*) della barra laterale, oppure `#import/<target>`. In alto, la scelta dell'istanza (*Instance*). Sei passi in ordine, con l'indicatore *Step N of 6*; dai passi già fatti si può tornare indietro (*Go back to …*). Non cambia nulla su AWS finché non premi *Start import*.

| Passo | Cosa si fa |
|---|---|
| 1. *Files* (File) | Si trascinano i `.bak` nella zona di rilascio (nell'app desktop; in un browser non ci sono i percorsi) oppure si scrivono i **percorsi assoluti**, uno per riga (*Add files*). Le stripe di un backup vanno insieme. Ogni file si può togliere |
| 2. *Verify* (Verifica) | L'app esegue `dialga inspect` (§5.7) su un SQL Server usa e getta (podman o docker): per ogni backup *Restorable*, *Not restorable*, *Read* (letto, senza verdetto), o *Unreadable*, i dati del backup, lo spazio necessario rispetto al libero e *What to do* per i non ripristinabili. Si toglie un backup con *Remove from the import*; finché ce ne sono di non ripristinabili non si prosegue. *Verify again* rilegge |
| 3. *Destination* (Destinazione) | Per ogni file il *Database name* (default dal nome del file; regole di §5.12), l'*S3 folder* (default il prefisso `import` del target), *Replace the objects that already exist* e *Leave the database restoring*; per un backup differenziale (riconosciuto in *Verify*) il passo dice su quale database verrà ripristinato, che deve essere già lasciato `RESTORING` da un restore completo |
| 4. *Preview* (Anteprima) | Il piano del job **senza eseguire nulla**: per ogni database l'upload (bucket, numero di parti, *Replaces an object*) e il restore con il T-SQL e i controlli preliminari. Se un controllo è fallito: *This restore cannot run: fix what the checks say, then preview again* (*Preview again*) |
| 5. *Progress* (Avanzamento) | Dopo *Start import*: avanzamento dal vivo di ogni database (upload con byte, velocità e tempo residuo; restore con la percentuale del task) |
| 6. *Result* (Esito) | Per ogni database l'esito e il report post-restore (*The database is online and the login can enter it*, oppure cosa richiede attenzione e gli *SQL users without a login* con i comandi per ricreare il login). Riepilogo *Restored / failed / not completed* e link *Open Databases*, *Follow it in Activity*, *Import more files* |

**Conferme**: una **sola** per l'intero job, nel passo 4: il nome del target se è *Protected*, oppure il testo di ciò che l'upload sostituisce (*retype what it replaces to confirm*). Il pulsante *Start import* si abilita solo con il testo esatto. Se la situazione cambia tra anteprima ed esecuzione (impronta diversa) il job non parte e il wizard rifà l'anteprima.

**Cosa continua se esci o annulli** (§5.10.7):

| Azione | Effetto |
|---|---|
| *Stop following* / lasciare la pagina | Smette solo di seguire: **il job va avanti nell'engine** e resta in Activity |
| *Cancel import* | Annulla il job: gli upload si fermano (in S3 non resta nulla), i database in coda non partono; i **task nativi già avviati continuano su RDS** (*Native task #N was still running on the instance when the job stopped*) |
| Chiudere l'app durante il job | Il job diventa *Interrupted* e si riprende da Activity (§6.9) |

Con `Leave the database restoring` il risultato dice che il database resta in restoring: il differenziale si ripristina con un secondo import (*Differential backup*) o con `dialga finish` / *Finish restore* in Databases.

### 6.8 Wizard Export

Voce **Export** (*Esporta*), oppure `#export/<target>?db=<nome>&db=<nome>`. Fa il backup nativo RDS di uno o più database su S3 e, a richiesta, li scarica. Sei passi, nulla cambia prima di *Run export*.

| Passo | Cosa si fa |
|---|---|
| 1. *Databases* (Database) | Si scelgono i database da esportare (*Select all*, *Clear selection*; *Selected: N · dimensione*). Compaiono solo quelli **online**: gli altri sono elencati come *Not online, not listed* |
| 2. *Options* (Opzioni) | *S3 key of …* per ogni database (vuoto = nome di default: prefisso `export` del target, database, data e ora UTC; **un export non sovrascrive mai**, e `*` non è ammesso). *Download each backup to this computer*: la *Folder* (percorso assoluto di una cartella esistente; ogni file prende il nome della sua chiave) e *Replace local files with the same name* |
| 3. *Windows* (Finestre) | La **finestra dei backup automatici** e quella di **manutenzione** dell'istanza (con la prossima occorrenza), i task nativi in corso o in coda e una **durata stimata** (grossolana: un tasso in MB/s per database più alcuni minuti perché RDS avvii ogni task). Se i backup attraversano una finestra, avvisa (*Start them later, or go on knowing that they may take longer*): **non blocca**. Se le finestre non si leggono, l'export può comunque partire |
| 4. *Preview* (Anteprima) | Per ogni database *Ready* o *Not ready*, dove andrà il backup (*Backup to*), dove sarà scaricato (*Download to*) e il T-SQL. Se qualcosa non va: *Some checks failed: change the options or the instance, then preview again* |
| 5. *Progress* (Avanzamento) | I backup girano **uno dopo l'altro**; ogni download parte quando il suo backup è su S3 |
| 6. *Result* (Esito) | Per ogni database: chiave in S3, file locale, dimensione e checksum (*Verified*, *Not verified*, *No checksum from S3*); riepilogo *Exported / failed / not completed* e link *Open Databases*, *Follow it in Activity*, *New export* |

- **Conferma**: una sola, nel passo 4 e solo se serve: il nome del target se è *Protected*. L'export non ha altre conferme (non sostituisce nulla in S3; il file locale solo con *Replace local files*).
- **Come gira**: se l'engine ha i job, l'export è **un job** (*The engine runs the export as one job: it goes on if you leave this page, and shows in Activity*); altrimenti la pagina li esegue uno dopo l'altro e va tenuta aperta fino alla fine (*a running backup goes on in RDS anyway*).
- **Cosa continua se esci o annulli**: *Stop following* non annulla un backup in corso, il task RDS va avanti (*Instances → Show tasks*); *Cancel export* ferma l'attesa e i download, ma un **backup già partito su RDS continua** (*Stopped while task N was running: it goes on on …*). I database non ancora partiti non partono.
- **Export selected**: in Databases (§6.4) si spuntano i database (*Select …*, *Select all shown databases*) e *Export selected (N)* (*Esporta i selezionati*) apre il wizard già sui database scelti.

### 6.9 Activity

Voce **Activity** (*Attività*), con il numero dei job in corso accanto al nome. Mostra **ogni operazione**: gli import e gli export, le singole operazioni dalla pagina Databases (*Restore from S3*, *Export*, *Drop*, *Finish*, *Cancel task*, che sono job di tipo `operation`), i tentativi **rifiutati** al momento di avviarli e i job lanciati dalla **CLI**. Prima quelli in corso (*Running*), poi lo storico (*History*): gli ultimi 200 job, al massimo 30 giorni, di tutte le origini.

- **Elenco**: per ogni job tipo e target (*Import on …*, *Export on …*), stato (*Queued*, *Running*, *Succeeded*, *Failed*, *Cancelled*, *Interrupted*), *N of M databases done*, quanti falliti e quando è stato creato o è finito. Senza job: *No jobs yet*.
- **Dettaglio** (clic su un job; torna con *All jobs*): istanza, data di creazione e di fine, id del job; i **database** con i loro passi (*Pending*, *Waiting for a slot*, *Running*, *Done*, *Failed*, *Skipped*, *Cancelled*, *Interrupted*), il progresso dei trasferimenti (byte, velocità, tempo residuo) e i task nativi (*Native task #N*); l'**Event log**, in diretta (*Live*; se cade la connessione: *Connection lost, reconnecting…*), con *Copy the log*.
- **Cancel job** (*Annulla il job*): chiede conferma (*Cancel this job?*): i database in coda o in attesa non partono e gli upload in corso si fermano; un task nativo RDS già in esecuzione **non viene fermato** (*Native task #N was still running on the instance when the job stopped: Dialga does not stop it*): si ferma con *Cancel* nel pannello dei task o `dialga cancel`. *Keep running* rinuncia.
- **Job interrotto** (*Interrupted: the engine stopped while this job was running*): compare *Resume* (*Riprendi*), che elenca cosa ha tenuto il journal (database, ultimo passo, passi non fatti, upload multipart con parti già in S3, task nativi, *still running on the instance*) e chiede conferma (*Resume this job?*): i database finiti restano, gli upload **continuano dalle parti già in S3**, un task già in coda **viene seguito, non rimesso in coda**. Su un target protetto si riscrive il nome del target. *Not now* rinuncia.
- Dai risultati dei wizard Import ed Export il link *Follow it in Activity* porta al dettaglio di quel job.
- **Tentativi rifiutati**: se *Run* viene respinto (controlli preliminari falliti o piano cambiato dopo l'anteprima, 412; conferma o password sbagliate o mancanti, 422/428; AWS o SQL Server non raggiungibili, 502) resta un job `Failed` senza passi, con il motivo e i controlli falliti. Le anteprime e i *dry run* non creano job.
- **Job della CLI** (`dialga import`, `run`, `restore`…): compaiono con origine `cli`, anche mentre l'app è aperta, e si seguono in diretta; per fermarli serve Ctrl-C nel loro terminale (*Cancel job* risponde che il job gira in un altro processo). `dialga jobs` e Activity leggono lo stesso journal (§5.14).
- **Operazioni singole**: il job ha i passi del piano (`restore`/`export` + `wait`, oppure `drop`, `finish`, `cancel`) e, se l'app si chiude, si riprende come gli altri. Chiudere la pagina durante l'operazione non la ferma.

### 6.10 Funzioni in arrivo

> In arrivo: una pausa su richiesta degli upload non ha ancora un comando o un pulsante (la ripresa riguarda i job interrotti). TODO: confermare con il team cos'altro è previsto per la prossima versione.

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
| `SQL password needed for target <nome>` (HTTP 428, passo SQL `SKIP`) | `prompt` o `keyring` e nessuna fonte ha la password | App: digitala nella finestra e ricordala se vuoi. CLI: `dialga password set <target>` o `DIALGA_SQL_PASSWORD` |
| `sql.password is keyring and no password is stored for <target>: run dialga password set <target>` | Target `keyring` senza voce nel portachiavi | `dialga password set <target>` |
| `no system keyring is usable on this machine` (501 dall'app, o da `password set`) | Nessun servizio portachiavi (Secret Service assente, sessione remota) | Avvia/sblocca GNOME Keyring o KWallet, oppure usa `env:VAR`/`secretsmanager:`. L'app può comunque tenere la password in memoria senza ricordarla |
| `the password comes from the environment variable <VAR>` / `… from AWS Secrets Manager` (409) | Si prova a digitare la password di un target `env:` o `secretsmanager:` | Cambia la variabile o il segreto, non la password nell'app |
| `secret <ARN>: …AccessDenied…` | Il profilo non può leggere il segreto (o la chiave KMS) | Aggiungi `secretsmanager:GetSecretValue` (e `kms:Decrypt`) all'utente (`docs/iam-policy.md`) |
| `secret <ARN> is JSON without a password field` | Il segreto è un JSON senza `password` | Aggiungi il campo `password`, o salva la sola password come testo |
| Verifica della password rifiutata (422, `Login failed for user '<u>'`) | La password digitata nell'app non è valida: non viene memorizzata | Riprova con quella giusta |
| `SQL password: environment variable <VAR> is empty` (`databases`, `status`, `wait`) | Questi comandi richiedono la password | Imposta la variabile |
| `Login failed for user '<u>'` | Utente o password sbagliati | Controlla `sql.user` e la password |
| Nuovo login rifiutato: password `422` o errore di SQL Server sulla policy | `--create-login` richiede almeno 12 caratteri da tre classi su quattro, e SQL Server applica anche la sua policy (`CHECK_POLICY ON`). L'errore di SQL Server viene ripulito: la password non compare | Usa `--generate-password`, oppure una password più forte con `--password-stdin`. Nell'app, *Generate* |
| Utenti orfani dopo un restore da un'altra istanza (`orphan: no login`, *Orphan* nel pannello) | Il database porta gli utenti dell'istanza di origine, ma i login non esistono qui | Se il login esiste: `dialga access fix-orphan <db> <user> --login <login> --apply`, o *Fix orphan* nel pannello. Se manca: `dialga access grant <db> <user> --level … --create-login --apply --generate-password` crea il login, ci lega l'utente orfano (che tiene i suoi ruoli) e aggiunge quelli del livello. Un login già legato a un altro utente del database non si lega (`fix-orphan` lo dice) |
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
| Sessione SSM ancora `Active` (es. in `aws ssm describe-sessions --state Active`) dopo un `kill -9` o un crash di `dialga` (o dell'engine dell'app) | Con SIGKILL il processo non può chiudere il tunnel; la sessione resta fino al timeout di inattività | `aws ssm terminate-session --session-id <id>` (solo le tue sessioni). Ctrl-C, SIGTERM, SIGHUP e una pipe chiusa (`| head`) chiudono invece la sessione da soli |
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

Dettagli, limiti e ripieghi: runbook di import, procedura C (runbook di import). TODO: confermare dove pubblicare il runbook per gli utenti di Dialga.

Se capita spesso, valuta con l'amministratore un aggiornamento di major version del motore RDS.

### 7.6 App desktop

| Sintomo | Causa | Cosa fare |
|---|---|---|
| *Engine not available* nella barra laterale | L'engine non è partito | Avvia `dialga-desktop` da terminale e leggi le righe `engine:` |
| *No instances yet* | Il file di configurazione non esiste | §4.1; il percorso è nella schermata |
| *The configuration file has errors* | File non valido | `dialga config validate` dice quale chiave |
| La panoramica mostra un errore AWS | Credenziali o account (§7.1) | Correggi, poi *Retry* |

### 7.7 Operazioni native, upload e download

| Messaggio | Causa | Cosa fare |
|---|---|---|
| `not confirmed: the text does not match (expected "<testo>"): nothing was changed` | Il testo riscritto non coincide con quello chiesto dal piano (nome del target, o del database per `drop`) | Riscrivi esattamente il testo; non è stato cambiato nulla |
| `this plan asks to retype "<testo>": run it on a terminal or pass --confirm` (per `upload`/`download`: `this <operazione> asks to retype "<testo>": pass --confirm <testo>`) | Conferma richiesta ma il comando non ha un terminale | Passa `--confirm <testo>` |
| *The plan changed since the preview: preview it again* (app); *The situation changed since the preview* | Tra anteprima ed esecuzione il piano è cambiato: l'impronta non coincide | Rileggi la nuova anteprima e riesegui |
| `Some pre-flight checks failed: nothing was changed.` (exit `2`) | Un controllo preliminare è `FAIL` | Leggi il controllo in `FAIL` e correggi: nulla è partito |
| `<db> already exists (<stato>): choose another name or drop it first` | Il restore vuole un database nuovo | Altro nome, oppure `dialga drop <db>` (distruttivo) |
| `<db> does not exist: a differential restore needs it RESTORING (full restore with --norecovery first)` | `--differential` su un database assente o non `RESTORING` | Prima un restore completo con `--norecovery` |
| `object not found: s3://… (upload it first)` / `no object matches … (upload the files first)` | La chiave non esiste nel bucket | `dialga upload`, o controlla la chiave: un nome semplice è cercato sotto `import` poi nella radice |
| `the bucket must be <bucket>: RDS reaches only the bucket of the target` | `s3://` su un altro bucket | Usa il bucket del target |
| `RDS restores at most 10 files per backup: … matches N` | Il `*` trova più di 10 file | Restringi lo schema |
| `… already in s3://<bucket>/<chiave> (…): choose overwrite to replace it` (upload) | **Oggetto già in S3** | `--overwrite` se vuoi sostituirlo, oppure `--key`/`--prefix` per un'altra chiave |
| `… appeared during the upload and was not replaced` | Un oggetto con quella chiave è comparso durante l'upload | Controlla chi l'ha scritto; poi rifai con `--overwrite` se serve |
| `<dest> already exists (…): choose overwrite to replace it` (download) | Il file locale esiste già | `--overwrite`, oppure un altro `dest` |
| `not enough free space in <cartella>: X needed, Y free` | Il download non entra nel disco | Libera spazio o scegli un'altra cartella |
| `export … already exists: an export never overwrites, choose another key` | La chiave dell'export esiste già in S3 | Un'altra chiave (o lascia quella di default, che contiene l'ora) |
| `the backup alone is X and Y is free: the restored database takes at least that much` (`WARN` del restore) | **Spazio probabilmente insufficiente** sull'istanza | Il restore parte comunque: `dialga inspect` dà la somma dei file, `dialga status` mostra se fallisce; libera spazio o aumenta lo storage |
| `s3://… changed since the plan` / `<file> changed since the plan: plan the upload again` / `… changed during the upload` | L'oggetto o il file è cambiato tra piano ed esecuzione | Rifai l'anteprima |
| `checksum mismatch: S3 has …, the download …; the file was not kept` | Download danneggiato | Riprova; il file parziale non resta |
| `uploaded, but S3 has CRC64NVME … and the file …` / `uploaded, but S3 has N bytes instead of M` | La rilettura finale non coincide | Non usare l'oggetto: rifai l'upload con `--overwrite` |
| `upload cancelled (…): the multipart upload was aborted, nothing is left in S3` | Ctrl-C, SIGTERM, SIGHUP o pipe chiusa | Normale: rifai l'upload quando vuoi |
| `…aborting the multipart upload failed too (…): remove it with aws s3api abort-multipart-upload …` | L'abort non è riuscito | Lancia il comando indicato per non lasciare parti (a pagamento) in S3 |
| `…may exist all the same: check it before using it` | L'esito del completamento non è certo | Controlla l'oggetto in S3 (dimensione e checksum) |
| `wait interrupted: task N keeps running on the instance` | Ctrl-C durante l'attesa | Il task continua: `dialga wait N -t <target>` (§5.10.7) |
| `task N not found (RDS keeps 36 days of history)` / `task N already ended in <stato>` (`cancel`) | Task inesistente o già finito | `dialga status` per gli id validi |
| `<db> is RESTORING: dialga finish <db> brings it online` (`post`) | Restore con `--norecovery` non concluso | `dialga finish <db>` |
| `<utente> cannot enter <db>: restored databases are owned by rdsa …` (`post`) | Il login non può entrare nel database ripristinato | Il messaggio rimanda al runbook di import (*troubleshooting: master user access to the restored database*); lo stesso messaggio compare nel report dell'app. TODO: confermare dove pubblicare il runbook (vedi §7.5) |
| `N SQL user(s) without a login on the instance: …` (`post`, `WARN`) | Utenti orfani dopo un restore da un altro server | Per ognuno `CREATE LOGIN` e `ALTER USER … WITH LOGIN` (§5.10.6) |
| Sessione SSM ancora `Active` dopo un `kill -9` di `dialga` o dell'app | Con SIGKILL il tunnel non si chiude | Vedi §7.3 |

### 7.8 Configurazione guidata

| Messaggio | Causa | Cosa fare |
|---|---|---|
| `not a terminal: give at least --instance and --bucket` | `config init` senza terminale | Passa le opzioni |
| `target already exists: <nome> (use --replace to change it)` | Il nome è già in uso | `--replace`, o un altro nome |
| `a new target needs --instance, --account-id, --bucket, --sql-user` (elenco di quelli mancanti) | `add-target` incompleto | Aggiungi le opzioni |
| `AWS profile "<p>" is not in the AWS shared files (known: …)` / `no AWS profiles on this computer: create one with aws configure (or aws configure sso)` | Profilo assente | `aws configure` (o `aws configure sso`) |
| `bucket <b>: … (--no-bucket-check keeps it anyway)` | `HeadBucket` fallito | Controlla nome, regione e permessi; `--no-bucket-check` salva comunque |
| `no RDS SQL Server instance in account <A>, <regione>` / `instance "<i>" not found (found: …)` | Profilo o regione sbagliati | Controlla profilo e regione |
| `the confirmation does not match the target name: nothing removed` / `not a terminal: confirm with --confirm <nome>` | `remove-target` | Riscrivi il nome, o `--confirm <nome>` |

### 7.9 Job, wizard e Activity

| Messaggio | Causa | Cosa fare |
|---|---|---|
| **412**: `pre-flight failed, nothing was started: …` (app) / `Nothing was started.` (CLI, exit `2`) | Un controllo preliminare di almeno un database è fallito: il job non è partito | Nel wizard leggi i controlli in *Not ready* e correggi; con `--skip-failing` (*skipFailing*) si lasciano fuori i database che falliscono |
| **412**: `the plan changed since the preview: preview it again` | Tra anteprima e avvio il piano è cambiato (impronta diversa) | Il wizard rifà da solo l'anteprima: leggila e riavvia. Da CLI rilancia il comando |
| **422**: *not confirmed* / `the text does not match` | Il testo riscritto non è quello chiesto (nome del target se protetto, o ciò che viene sostituito) | Riscrivi il testo esatto indicato dal wizard (*Type … to confirm*) o passa `--confirm` |
| **422**: `SQL password: …` / `no SQL password in the engine environment` | L'engine dell'app non ha la password del target (`prompt` o `env:VAR`) | Avvia l'app da terminale con la variabile impostata (§4.3) |
| `job <id> is <stato>: only an interrupted job can be resumed` | `jobs resume` o *Resume* su un job che non è `interrupted` | Solo i job interrotti si riprendono; `dialga jobs show <id>` dice lo stato |
| `the job is in use by another Dialga process: …` | L'app desktop (o un altro `dialga jobs resume`) esegue o sta riprendendo quel job | Seguilo da Activity; se serve fermarlo, dal processo che lo esegue |
| `the job runs in another Dialga process: stop it there` (409 su *Cancel job*) | Il job è di una run CLI | Ctrl-C nel terminale che lo esegue |
| `the target <t> of job <id> is not in the configuration any more` / `target <t> now points at instance <A>, the job ran on <B>: not resumed` | Il target è stato rimosso o ora punta a un'altra istanza | Riporta il target com'era (stessa istanza) e riprova |
| `job <id> not found in <cartella> (removed after 30 days or 200 jobs)` / *No job with this id* | Il journal tiene gli ultimi 200 job, al massimo 30 giorni; | `dialga jobs` elenca quelli disponibili |
| `the job cannot be resumed` | Un passo da rifare non si può ricostruire dal journal | Il job resta `interrupted` e non cambia nulla: rilancia l'operazione dal wizard o con `dialga import` / `dialga run` |
| `invalid job id "<x>": dialga jobs lists them` | Id non valido | Copia l'id da `dialga jobs` |
| *Interrupted: the engine stopped while this job was running* | L'app è stata chiusa o uccisa durante il job | *Resume* in Activity (§6.9). Un task nativo già avviato è andato avanti da solo su RDS: controlla *Show tasks* |
| `a prefix is for imports` / `database <db> is in the job twice` / `<n> databases: a job takes 50 at most` | Piano non valido | Correggi il piano (`docs/piani-job.md`) |
| `apiVersion is "<x>", this build reads "v1"` / `invalid YAML: …` | Piano YAML con versione o chiave sbagliata (lettura stretta) | Correggi il file |
| `the plan is for target <A>, -t says <B>` | `-t` in contrasto con il `target` del piano | Togli `-t` o usa il target giusto |

## 8. Riferimenti

| Documento | Contenuto |
|---|---|
| `docs/config.example.yaml` | Configurazione di esempio commentata |
| `docs/iam-policy.md`, `docs/iam-policy.json` | Policy IAM minima per gli utenti |
| `docs/piani-job.md` | Piani YAML di `dialga run`, controlli preliminari dei job, API dei job |
| `docs/release.md` | Come si rilascia e si installa una versione |
| `engine/internal/api/openapi.yaml` | Contratto OpenAPI dell'API locale dell'app (`GET /api/v1/openapi.yaml` sull'engine in esecuzione) |
| `CHANGELOG.md` | Modifiche per versione |
| `dialga <comando> --help` | Opzioni di ogni comando |
| Documentazione AWS | *Importing and exporting SQL Server databases using native backup and restore* (RDS for SQL Server); *Install the Session Manager plugin for the AWS CLI* |
