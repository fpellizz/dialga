# Job di import ed export: `dialga import` e piani YAML (`dialga run`)

| | |
|---|---|
| **Data** | 2026-10-16 |
| **Autore** | Fabio Pellizzaro (con Claude Code) |
| **Versione documentata** | `0.3.0` |
| **Oggetto** | Import ed export di più database come un solo job: CLI `import`, `run <piano.yaml>`, API `POST /api/v1/jobs` |

> **Sintesi**
> - Un **job** importa o esporta **uno o più database** di un target. Ogni database fa i suoi passi:
>   - **import**: `upload` → `restore` → `wait` → `post`;
>   - **export**: `export` → `wait` → `download`.
> - Prima di partire, Dialga esegue i **controlli preliminari di tutti i database** (sola lettura). Se uno fallisce non parte niente, a meno di `--skip-failing` (`skipFailing` nel piano), che lascia fuori i database che falliscono.
> - **Conferma una volta per job**: si riscrive il nome del target se è `protected`, altrimenti quello che viene sostituito (la chiave, il file, oppure `replace N files`).
> - Il **dry-run è il default**: `--apply` avvia il job e lo segue nel terminale. **Ctrl-C annulla il job**: gli upload vengono interrotti con abort (in S3 non resta niente), mentre i task nativi già partiti continuano sull'istanza.

## 1. Cosa fa un job

| Tipo | Passo | Risorsa | Cosa fa |
|---|---|---|---|
| Import | `upload` | trasferimento S3 | Upload multipart con checksum CRC64NVME verificato (come `dialga upload`); mai sopra un oggetto esistente senza `overwrite` |
| Import | `restore` | task RDS | `rds_restore_database` (anche `differential`, `noRecovery`) |
| Import | `wait` | task RDS (lo stesso slot di `restore`) | Segue il task fino alla fine; `ERROR`/`CANCELLED` = database fallito |
| Import | `post` | nessuna | Report post-restore in sola lettura (come `dialga post`); con `noRecovery` non c'è |
| Import con `replace` | `backup`, `wait-backup`, `drop`, `single-user`, `rename-old`, `rename-new`, `multi-user`, `multi-user-old` | task RDS o nessuna | Restore su un database che esiste già, vedi §3.3 |
| Export | `export` | task RDS | `rds_backup_database`, mai sopra un oggetto esistente |
| Export | `wait` | task RDS | Come per l'import |
| Export | `download` | trasferimento S3 | Solo se c'è `destination`: GET a range paralleli, file temporaneo, checksum verificato |

- **Più database insieme**: i database avanzano in parallelo. Mentre uno fa il restore, il successivo fa l'upload.
- **Limiti**: al massimo `max_tasks` task nativi per istanza (dal target; RDS ne esegue 2) e 3 trasferimenti S3 alla volta, contando tutti i job insieme.
- **Fallimenti**: un database che fallisce non ferma gli altri.
- **Massimo per job**: 50 database.
- **Il job vive nell'engine**:
  - con la CLI, nel processo di `dialga import`/`dialga run`, che resta in primo piano finché il job finisce;
  - con l'app, nell'engine dell'app: la pagina Activity lo segue.

## 2. `dialga import`

```bash
dialga import <file.bak>... [-t target] [--db NOME,...] [--key KEY | --prefix P]
              [--differential] [--norecovery] [--overwrite] [--skip-failing]
              [--interval 30s] [--apply] [--confirm TESTO]
```

| Opzione | Significato |
|---|---|
| `--db` | Nomi dei database, **uno per file** nell'ordine (default: il nome del file senza estensione, `sales.bak` → `sales`) |
| `--key` | Chiave S3 dell'upload, solo con un file |
| `--prefix` | Prefisso al posto di `prefixes.import` del target (`""` = radice del bucket) |
| `--differential` | Backup differenziali, sopra database lasciati `RESTORING` |
| `--norecovery` | Lascia i database `RESTORING` (segue un differenziale); niente report `post` |
| `--overwrite` | Sostituisce gli oggetti già presenti in S3 (chiede conferma) |
| `--replace auto\|swap\|drop` | Ripristina anche sopra database che esistono già (§3.3); chiede di riscrivere il nome |
| `--no-backup-first` | Con `drop`: non salva prima il database corrente su S3 |
| `--skip-failing` | Lascia fuori i database il cui pre-flight fallisce |
| `--interval` | Intervallo di lettura dei task nativi (default 30 s) |

Un backup **striped**, cioè diviso in più file, si importa con un piano YAML (`files`, §3).

```bash
# dry-run: controlli e piano di ogni database, niente cambia
dialga import ~/backup/sales.bak ~/backup/hr.bak -t dev-sql

# job vero, con nomi espliciti
dialga import ~/backup/a.bak ~/backup/b.bak --db sales,hr -t dev-sql --apply
```

## 3. Piano YAML per `dialga run`

```bash
dialga run piano.yaml [-t target] [--skip-failing] [--interval 30s] [--apply] [--confirm TESTO]
```

Lo schema è `apiVersion: v1`. La lettura è stretta: una chiave sconosciuta è un errore. I percorsi relativi partono dalla cartella del piano. `-t` serve solo se il piano non ha `target`; se lo ha e `-t` è diverso, è un errore.

### 3.1 Import

```yaml
apiVersion: v1
kind: Import
target: dev-sql
prefix: import/2026-10        # facoltativo: al posto del prefisso import del target ("" = radice)
skipFailing: false            # facoltativo
replace: ""                   # facoltativo: auto | swap | drop, vale per gli item che non lo indicano (anche backupFirst)
items:
  - file: backup/sales.bak
    database: sales
    key: import/sales.bak     # facoltativo, solo con un file
    overwrite: false          # facoltativo: sostituisce l'oggetto in S3
  - files: [backup/crm_1.bak, backup/crm_2.bak]   # backup striped (fino a 10 file)
    database: crm
  - file: backup/hr_full.bak
    database: hr
    noRecovery: true          # resta RESTORING per il differenziale
```

| Chiave dell'item | Obbligatoria | Note |
|---|---|---|
| `file` oppure `files` | Sì | Un `.bak`, oppure le stripe di un solo backup. Ogni file va in `<prefix>/<nome file>`; il restore usa `<inizio comune>*<fine comune>` |
| `database` | Sì | Regole di `mssql-import.sh`: lettere, cifre, `_` e `-`, niente DB di sistema. Una volta sola per job |
| `key` | No | Solo con un file |
| `differential` | No | Il DB deve esistere `RESTORING` |
| `noRecovery` | No | Niente passo `post` |
| `overwrite` | No | Sostituisce gli oggetti in S3 (conferma) |
| `replace` | No | `auto`, `swap` o `drop`: ripristina sopra un database esistente (§3.3). Vuoto = lo rifiuta. Non si combina con `differential` né `noRecovery` |
| `backupFirst` | No | Solo `drop`: salva prima il database su S3 (default `true`; con `auto` è sempre `true` se sceglie `drop`) |

**Backup striped**: se il pattern `*` corrisponde anche ad altri oggetti già nel bucket, il pre-flight fallisce, perché RDS li ripristinerebbe insieme al backup. In quel caso si usa un altro `prefix`.

**Full più differenziale dello stesso DB**: non stanno nello stesso job, perché i database avanzano in parallelo e un DB compare una volta sola. Si lancia prima il job con `noRecovery: true`, poi un secondo job con `differential: true`.

### 3.3 Restore su un database esistente (`replace`)

RDS rifiuta un restore nativo sopra un database che esiste. Con `replace` Dialga lo fa in uno di due modi, e l'anteprima mostra quale e **perché** (controllo `replace`) con il T-SQL esatto di ogni passo.

| Strategia | Passi | Cosa resta |
|---|---|---|
| `swap` | restore in `<db>_dialga_new_<aaaammggoommss>`, attesa, `ALTER DATABASE … SET SINGLE_USER WITH ROLLBACK IMMEDIATE`, rinomina `<db>` in `<db>_old_<stamp>`, rinomina il nuovo in `<db>`, `SET MULTI_USER` su entrambi, report post | il database vecchio, `<db>_old_<stamp>`: si elimina poi con `dialga drop` |
| `drop` | backup di `<db>` in `<prefisso export>/<db>_before_replace_<stamp>.bak` (se `backupFirst`), attesa, `rds_drop_database`, restore, attesa, report post | il backup di sicurezza su S3 |
| `auto` | `swap` se due copie stanno nello spazio libero, altrimenti `drop` (con il backup) | come la strategia scelta |

Le rinomine usano `EXEC rdsadmin.dbo.rds_modify_db_name`, verificata su un'istanza di sviluppo il 2026-10-19 (`@old_db_name`, `@new_db_name`). Se la seconda rinomina fallisce, il database vecchio riprende il suo nome e il nuovo resta col nome temporaneo; un job interrotto tra le due rinomine riprende leggendo i nomi attuali prima di agire.

**Conferma e controlli**: serve riscrivere il nome del database (o del target se è protetto). Nuovi controlli: sessioni attive sul database (avviso: saranno disconnesse), nomi temporaneo e vecchio liberi e di al massimo 128 caratteri, spazio libero per strategia, chiave del backup di sicurezza libera.

**Stamp**: l'anteprima sceglie un `stamp` (UTC) che data i nomi; il digest lo ignora, ma rimandandolo (`stamp` in `POST /api/v1/jobs`) l'esecuzione usa gli stessi nomi mostrati.

### 3.2 Export

```yaml
apiVersion: v1
kind: Export
target: dev-sql
items:
  - database: sales
    destination: ./export          # facoltativo: cartella esistente o file
  - database: hr
    key: export/hr_mensile.bak     # facoltativo
    destination: ./export/hr.bak
    overwrite: true                # sostituisce il file locale (conferma)
  - database: crm                  # senza destination: il backup resta in S3
```

| Chiave dell'item | Obbligatoria | Note |
|---|---|---|
| `database` | Sì | Deve essere `ONLINE` |
| `key` | No | Default `<prefisso export>/<database>_<AAAAMMGG-HHMMSS>.bak` (UTC); mai sopra un oggetto esistente |
| `destination` | No | Cartella esistente (il file prende il nome dalla chiave) o percorso di file. Senza, niente download |
| `overwrite` | No | Sostituisce il file locale (conferma) |

## 4. Controlli preliminari

| Controllo | Import | Export |
|---|---|---|
| File leggibile, non vuoto, chiave valida per RDS | Sì (`Upload to S3`) | — |
| Oggetto già in S3 (senza `overwrite` fallisce) | Sì | Sì (`Destination in S3`, sempre) |
| Stesse chiavi o stessi file locali tra database del job | Sì | Sì |
| Stripe: il pattern `*` tocca solo i file del backup | Sì (`Backup files`) | — |
| Stato del DB: assente (o `RESTORING` per il differenziale) / `ONLINE` | Sì | Sì |
| Task nativi in corso (avviso: RDS ne esegue 2) | Sì | Sì |
| File locale libero (senza `overwrite` fallisce) | — | Sì (`Local file`); lo spazio libero si controlla al download |
| Cifratura KMS del bucket (avviso, se leggibile) | Sì | — |

Il controllo `Backup in S3` del restore non c'è, perché il backup lo mette in S3 l'upload dello stesso job.

## 5. Annullare, chiudere l'app, exit code

| Evento | Upload | Task nativo | Stato del job |
|---|---|---|---|
| Ctrl-C (CLI), *Cancel* (Activity, `POST /api/v1/jobs/{id}/cancel`) | Abort: in S3 non resta niente | Continua sull'istanza (`taskLeftRunning`); si annulla con `dialga cancel <task-id>` | `cancelled` |
| L'app si chiude (engine fermato) | Le parti **restano** in S3 per la ripresa (upload id nel journal) | Continua sull'istanza | `interrupted`: si riprende da Activity o con `dialga jobs resume` |
| Un database fallisce | — | — | Gli altri continuano; il job è `failed` |

| Exit code | Significato |
|---|---|
| `0` | Dry-run pronto, oppure job completato |
| `1` | Errore, job fallito o annullato |
| `2` | Pre-flight fallito: non è partito niente |
| `3` | Un task nativo è finito in `ERROR` o `CANCELLED` |

Con `-o json`: un documento su stdout alla fine (`target`, `applied`, `ready`, `plan`, `job`). Con `--apply` gli eventi del job vanno anche su stderr, una riga JSON ciascuno (`{"event": <tipo>, "data": …}`).

## 6. API (`POST /api/v1/jobs`)

Lo usa la GUI (wizard Importa ed Esporta). Corpo:

```json
{ "kind": "import", "target": "dev-sql", "prefix": "import",
  "items": [ { "files": ["/abs/crm_1.bak", "/abs/crm_2.bak"], "database": "crm" } ],
  "dryRun": true, "skipFailing": false, "confirm": "", "digest": "" }
```

| Risposta | Quando |
|---|---|
| `200` + piano (`JobPlan`) | `dryRun: true`. Rimandare `items[].key` e `digest` del piano: la chiave di default dell'export contiene l'ora |
| `201` + job (`Location: /api/v1/jobs/{id}`) | Job partito: lo si segue su `/api/v1/jobs/{id}/events` |
| `412` `{reason, plan}` | Pre-flight fallito, oppure `digest` diverso |
| `422` | `confirm` errato, o password SQL assente nell'ambiente dell'engine |
| `400` / `404` / `409` / `502` / `503` | Richiesta non valida / target sconosciuto / config / AWS o SQL / engine in chiusura |

Contratto completo: `engine/internal/api/openapi.yaml`, schemi `JobRequest`, `JobPlan` e `JobRefused`.

## 7. Limiti

- La **ripresa** riguarda i job dell'app: un job `interrupted` si riprende da Activity (`POST /api/v1/jobs/{id}/resume`) o con `dialga jobs resume <id>` (non mentre l'app tiene il journal). Gli upload continuano dalle parti già in S3, un task nativo già in coda viene seguito.
- La CLI non scrive i suoi job nel journal dell'app: non compaiono in Activity né in `dialga jobs`, e non si riprendono.
- `dialga import` non legge il nome del database dal `.bak`: lo prende dal nome del file. Il wizard della GUI usa invece l'ispezione.
- Il progresso dei trasferimenti è **per database**: con le stripe è la somma dei file.
