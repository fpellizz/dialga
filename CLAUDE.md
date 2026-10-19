# CLAUDE.md — Dialga

**Dialga** importa ed esporta uno o più database SQL Server su istanze AWS RDS, passando per S3 (`rds_restore_database` / `rds_backup_database`). Ha due interfacce:
- un'**app desktop** con GUI;
- una **CLI** per l'automazione.

È fatta così: un engine Go con API locale; sopra, una shell Tauri v2 e un frontend React sul Decisyon Design System.

| Versione | Piattaforme e contenuto |
|---|---|
| v1 | **Linux** |
| v2.0 | Anche Windows e macOS, pacchetti firmati, deploy su Kubernetes con la stessa UI via web |

- **Piano** (fuori repo, nome di lavoro precedente `mssql-rds-xfer`): `~/Claudio/AWS/Sales/mssql-import/piano-app-mssql-rds-xfer.md`.
- **Riferimento funzionale:** gli script bash collaudati e il runbook in `~/Claudio/AWS/Sales/mssql-import/` (`mssql-import.sh`, `mssql-bak-inspect.sh`, `runbook-import-db-mssql-dev.md`, mock `test/mock-rds.sql`).

**Stato al 2026-10-16:**

| Branch | Contenuto | Commit |
|---|---|---|
| `feature/bootstrap` | CI, scheletro dell'engine, asset del brand | Unito in `main` (PR #1) |
| `feature/spike-tauri-sidecar` | Spike dell'architettura: engine `serve` + API + check, frontend `web/`, shell `desktop/` | Unito in `main` (PR #2, fix gitleaks PR #3) |
| `feature/v0.1-foundations` | v0.1, prima parte: configurazione dei target, CLI `check`/`config` (testo/JSON), controllo dell'account, connessione `external`/`direct`. Provato dal vivo su `mssql-dev` | Unito in `main` (PR #4) |
| `feature/v0.1-foundations` | v0.1, seconda parte: API dei target (`GET /api/v1/targets`, check per target), GUI con sidebar e schermata Instances, EN/IT. Provata dal vivo nel browser (Vite + engine) | Unito in `main` (PR #5) |
| `feature/v0.1-foundations` | v0.1, terza parte: task nativi RDS. CLI `status`/`wait` (exit 3 se il task finisce in ERROR/CANCELLED), API `GET /api/v1/targets/{name}/tasks`, pannello Tasks nella scheda dell'istanza (si aggiorna ogni 30 s finché un task è in corso). Pacchetti condivisi `awsx`/`mssql`/`session`. Provata dal vivo su `mssql-dev` (sola lettura) | Unito in `main` (PR #6) |
| `feature/v0.1-foundations` | v0.1, quarta parte: piano di azioni con anteprima (`internal/core`: dry-run di default, conferma per target protetti e azioni distruttive, esecuzione con eventi) e T-SQL delle procedure `rds_*` identico a `mssql-import.sh` (golden file). Nessun comando nuovo: li usano restore/export della v0.2 | Unito in `main` (PR #7) |
| `feature/v0.1-foundations` | v0.1, quinta parte: contratto OpenAPI 3.1 dell'API locale (`engine/internal/api/openapi.yaml`, servito su `GET /api/v1/openapi.yaml`) con test che lo confronta con le rotte e con i tipi Go | Unito in `main` (PR #8) |
| `feature/v0.1-foundations` | v0.1, sesta parte: pagina Impostazioni (lingua, tema sistema/chiaro/scuro, percorso della config, versione dell'engine), navigazione con hash (`#instances`, `#settings`) | Unito in `main` (PR #8) |
| `feature/v0.1-foundations` | v0.1, settima parte: `check` con gli stessi verdetti di `mssql-import.sh check` (opzione `SQLSERVER_BACKUP_RESTORE`, bucket raggiungibile, spazio libero da CloudWatch, option group non `in-sync` = errore, task in corso = avviso) e stato `warning` che non fa fallire. Confrontati dal vivo su `mssql-dev` il 2026-10-16: stesso esito e stessi valori | Unito in `main` (PR #8): **v0.1 completa** |
| `feature/v0.2-wave1` | Integrazione del lavoro in parallelo (sezione sotto): trasferimenti S3, operazioni native con anteprima e GUI di conferma, ispezione dei `.bak`, tunnel SSM integrato e condiviso, schermata Database, primo avvio e scrittura della config, job multi-DB (scheduler, journal, API), accessibilità AA, test web (componenti + e2e), pacchetti Linux, documentazione in italiano (`docs/guida-utente.md`) | Unito in `main` (PR #9) |
| `feature/v0.2-wave2` | Integrazione della seconda ondata (sezione «Aggiunte della wave 2»): wizard Importa/Esporta sull'API dei job, pagina Attività con ripresa dei job interrotti, pipeline `import`/`export` (`POST /api/v1/jobs`, `dialga import`/`run`), un solo tunnel per target e job, checksum nei job, correzioni e test più stabili | PR verso `main` |

Lo **spike** è validato dal vivo su `mssql-dev`:
- l'engine fa da sidecar con token su stdin;
- l'API su 127.0.0.1 respinge le richieste senza token (401), da origini estranee (403) e con host falsi (403);
- il check in streaming (SSE) passa 5/5 in circa 4 s;
- il **login SQL avviene con il certificato RDS verificato** attraverso il tunnel. Le prove contrarie falliscono come devono: hostname sbagliato e radici di sistema;
- la UI nel browser (Vite dev + engine con `DIALGA_DEV=1`) mostra il flusso completo.

## Lavoro in parallelo (dal 2026-10-16)

Su richiesta dell'utente ("che sto attrezzo veda la luce il prima possibile", meno build possibili) il lavoro procede con **più agenti in parallelo**:

- ogni agente lavora in un **git worktree** `~/GIT/dialga-wt/<nome>` sul branch locale `feature/wave1-<nome>` (`wave2-<nome>` nella seconda ondata), senza push;
- il lead integra i branch in `feature/v0.2-wave1` / `feature/v0.2-wave2` (merge `--no-ff`, conflitti risolti a mano, test completi in locale) e porta su `main` **poche PR grandi**;
- per ridurre i conflitti: i comandi CLI nuovi si registrano da soli dal loro file (`commands` in `cmd/dialga/main.go`), le rotte API idem (`extraRoutes` in `internal/api/server.go`);
- file caldi: `openapi.yaml` (in caso di conflitto si ricostruisce dalla versione di integrazione più i path/schemi nuovi del branch, confrontando le chiavi con la base comune), `openapi_test.go`, `api.ts`, `i18n.tsx`, `app.css` (aggiunte in coda), `main.go`/`server.go` (campi di `app`/`Options`), `go.mod` (`go mod tidy` dopo il merge);
- i worktree si rimuovono (`git worktree remove`) quando il loro branch è integrato e unito in `main`.

## Decisioni prese

| Tema | Decisione (2026-10-15) |
|---|---|
| Stack | Engine Go + API locale (HTTP + SSE), shell Tauri v2 con l'engine come sidecar, frontend React 18.3 + TS |
| UI | Decisyon Design System (`DecisyonDesignSystem_06d538`): shadcn stile Nova su **Base UI** (non Radix), Geist, Lucide, primario `#5b8210` |
| Lingua | **Inglese di default**, italiano selezionabile dalla sidebar (scelta in `localStorage` `dialga.lang`). Dizionari tipizzati in `web/src/i18n.tsx`, senza librerie. Copy secondo le regole del DS: sentence case, imperativi, tono neutro |
| Piattaforme v1 | **Linux**: pacchetti deb e rpm; CLI cross-compilata per tutti gli OS. Installer Windows/macOS → v2.0 |
| React | **18.3, non 19**: il bundle del Decisyon Design System si aggancia a React 18 come globale |
| Password SQL | Mai in una risposta API, su file (tranne il portachiavi di sistema), nel journal dei job, nei parametri dei passi, negli eventi, nei log o negli errori. Arriva all'engine solo con `PUT /api/v1/targets/{name}/password` (dialogo dell'app) o dalla fonte configurata in `sql.password`: `prompt`, `env:VAR`, `keyring`, `secretsmanager:<ARN>`. Dettagli nella sezione "Password SQL" sotto |
| Firma dei pacchetti | v2.0 |
| GitHub | Mirror e workflow pronti ma **spenti** fino alla v2.0 |
| CI (2026-10-16) | Meno build: nessuna pipeline sui push dei branch (i controlli girano sulla PR); nella PR ogni step solo se la sua parte è cambiata (`changesets`; la pipeline o `ci/` fanno girare tutto); su `main` solo secrets + mirror; cross-build della CLI solo sui tag e in `release-dry-run`; tag sempre completi; pipeline custom `checks` per lanciare tutto a mano |
| Dipendenze | Solo versioni pubblicate da **almeno 14 giorni**, anche le transitive (supply chain). Go: `go list -m -json <mod>@<ver>` (campo Time). npm: `npm view <pkg> time --json`; le transitive troppo giovani si fissano con `overrides` in `pnpm-workspace.yaml` |

## Contenuto

| Percorso | Cosa fa |
|---|---|
| `bitbucket-pipelines.yml` | PR: check (engine, web, desktop, secrets) con condizioni `changesets` + integrazione (`go test -tags integration -p 1`). `main`: secrets + mirror. Tag: tutto, con verifica della versione (`ci/bump-version.sh --check`) già nel primo gruppo, CLI per 6 piattaforme, pacchetti Linux deb/rpm (immagine `node:24-bookworm`, glibc 2.36) e upload **manuale** nei Downloads. Custom: `checks`, `integration`, `release-dry-run`, `mirror-to-github` |
| `.github/workflows/ci.yml` | Check su GitHub: engine su Linux, Windows e macOS, lint, integrazione, web, desktop, gitleaks. Gira solo col mirror attivo |
| `.github/workflows/release.yml` | Percorso di rilascio **v2.0**: bundle per i tre OS → release GitHub in bozza |
| `ci/build-engine.sh` | Cross-build della CLI (`dist`) o del sidecar Tauri (`sidecar`), condiviso dalle due CI |
| `ci/bump-version.sh` | `X.Y.Z` aggiorna la versione in `tauri.conf.json`, `Cargo.toml`, `Cargo.lock` (voce `dialga-desktop`), `desktop/package.json`, `web/package.json`; `--check [v]X.Y.Z` confronta soltanto. Tutto o niente, solo `MAJOR.MINOR.PATCH` |
| `ci/tauri-placeholders.sh` | File finti richiesti da `tauri-build` nei job di check (mai nei rilasci) |
| `engine/` | Modulo `github.com/decisyon/dialga/engine` (Go 1.26), dettaglio sotto |
| `engine/cmd/dialga` | CLI (cobra), comandi della v0.1 (quelli della wave 1 nella sezione sotto): `check [target]`, `status [target]` (`--db`, `--limit`: task nativi, dal più recente), `wait <task-id>` (`-t target`, `--interval`, default 30s: segue il task fino alla fine; Ctrl-C ferma l'attesa, non il task), `config path\|validate\|show`, `serve` (API locale per la shell), `version`. Opzioni globali `--config`, `--output text\|json`. Exit code: 0 OK, 1 errore, 2 controllo non superato, 3 task RDS in `ERROR`/`CANCELLED` |
| `engine/internal/config` | Configurazione dei target (`apiVersion: v1`), lettura stretta (chiavi sconosciute = errore), default e validazione. Percorso `os.UserConfigDir()/dialga/config.yaml`. La password è solo un riferimento: `prompt`, `env:VAR`, `keyring`, `secretsmanager:<ARN>` (tutte attive dal 2026-10-16) |
| `docs/config.example.yaml` | Esempio commentato, con soli segnaposto |
| `engine/internal/api` | HTTP su 127.0.0.1, porta casuale, token Bearer a tempo costante, controllo di Host e Origin (CORS). La config si rilegge a ogni richiesta. `GET /api/v1/health`; `GET /api/v1/targets` (elenco senza riferimenti alla password; file assente = elenco vuoto, file non valido = campo `error`); `GET /api/v1/openapi.yaml` (il contratto); `POST /api/v1/targets/{name}/checks` (SSE `notice`/`step`/`done`; 409 senza config, 404 target sconosciuto); `GET /api/v1/targets/{name}/tasks?db=&limit=` (task nativi; 400 parametri non validi, 422 password non disponibile, 502 se AWS o SQL non rispondono; ogni chiamata apre la sua sessione SQL, e in modalità `tunnel` il suo tunnel). Gli endpoint aggiunti dalla wave 1 sono nella sezione sotto. **Contratto:** `openapi.yaml` (OpenAPI 3.1, incorporato con `go:embed`). `openapi_test.go` lo confronta con `routes()` (unica lista delle rotte) e con i tag JSON dei tipi Go (proprietà = campi, `required` = campi senza `omitempty`): una rotta o un campo nuovo senza la specifica aggiornata fa fallire i test |
| `engine/internal/check` | I passi del check, con gli stessi verdetti di `mssql-import.sh check`: `aws` (account), `instance` (`available` e option group `in-sync`), `option` (`SQLSERVER_BACKUP_RESTORE`, finestra dei backup), `bucket` (`HeadBucket`), `storage` (allocato, autoscaling, libero da CloudWatch: senza CloudWatch è solo un avviso), `tunnel`, `sql` (login con TLS verificato), `tasks` (DB utente; task in corso = avviso). Stati `ok`/`warning`/`failed`/`skipped`: `warning` non fa fallire. Il limite di DB per classe d'istanza dello script (30 per `db.m6i.large` Single-AZ) non è portato: `TODO: confermare` la fonte dei limiti per classe. `target.go`: da target configurato a richiesta di check (`RequestFor`) |
| `engine/internal/awsx` | Credenziali AWS con controllo dell'account (`Load`, `*WrongAccountError`), istanza RDS (`DescribeInstance`: endpoint, option group, storage, finestra dei backup), `HasOption`, `HeadBucket` (404/403/301 tradotti in messaggi leggibili), `FreeStorageBytes` (CloudWatch `FreeStorageSpace`, minimo degli ultimi 20 minuti). SDK S3 `v1.114.0` e CloudWatch `v1.73.0`, scelti con almeno due settimane di età |
| `engine/internal/mssql` | Connessione con certificato verificato sul bundle RDS (`Open`, `Addr`); task nativi da `msdb.dbo.rds_fn_task_status` (`Tasks.List`/`Get`, interfaccia `TaskReader`, `Wait` con tentativi sugli errori temporanei). Orari = ora del server senza fuso (`2006-01-02T15:04:05`) |
| `engine/internal/core` | Piano di azioni (§3.4 del piano): `Plan` con `Action{ID, Title, Kind sql\|aws, Risk change\|destructive, Statement, Run}`. `Preview` (testo del dry-run), `Confirmation` (target protetto → nome del target; azione distruttiva → `Subject`, es. nome del DB), `Execute` (verifica la conferma, esegue in ordine, al primo errore salta il resto; eventi con gli stessi campi di `check.Event`) |
| `engine/internal/mssql/tsql.go` | T-SQL delle procedure native: `RestoreDatabase` (opzioni `Differential`, `NoRecovery`), `BackupDatabase`, `FinishRestore`, `DropDatabase`, `CancelTask`, più `Str` (letterale `N'…'` con apici raddoppiati) e `S3ARN`. Solo formattazione: nomi e chiavi si validano prima. **Golden file** in `testdata/golden/`, generati da `mssql-import.sh` stesso con `MSSQL_IMPORT=<path> testdata/gen-golden.sh testdata/golden`: se lo script cambia, si rigenerano e il test dice cosa diverge |
| `engine/internal/session` | Sessione SQL su un target configurato (AWS + account → endpoint → TDS + ping). Password: resolver unico `session.Passwords` (`passwords.go`) e portachiavi (`keyring.go`) |
| `engine/internal/rdsca` | Bundle CA RDS incorporato (`global-bundle.pem`) |
| `engine/internal/validate` | `DBName`, `S3Key`: le regole di `valid_db`/`valid_key` di `mssql-import.sh` |
| `package.json`, `pnpm-workspace.yaml` | Workspace pnpm (`pnpm@12.11.2`) con `web` e `desktop`. pnpm 12 applica un'età minima di rilascio: le eccezioni per Tauri 2.12.x le ha aggiunte pnpm stesso (`minimumReleaseAgeExclude`) |
| `web/` | React 18 + TS 7 + Vite 8 + Biome + Vitest. `src/engine.ts` (URL e token dalla shell, o `VITE_ENGINE_*` in dev), `src/api.ts` + `src/sse.ts` (fetch e SSE con header Bearer: `listTargets`, `checkTarget`), `src/i18n.tsx` (EN/IT), `src/theme.ts` (tema `system|light|dark`, `localStorage` `dialga.theme`, applicato prima del primo paint), `src/App.tsx` (layout sidebar + pagina, routing con hash `#instances`/`#settings`), `src/Sidebar.tsx` (navigazione: Instances, Databases, Import, Export, Activity, Settings; stato dell'engine e dei tunnel), `src/SettingsPage.tsx` (lingua e tema come segmented control su radio native, percorso della config, versione dell'engine e URL dell'API locale), `src/InstancesPage.tsx` (schede dei target, skeleton, empty state con il percorso della config, errori della config, check con `src/StepList.tsx`, task con `src/TasksPanel.tsx`), `src/styles/ds-tokens.css` (copia dei token del DS, da non modificare) + `src/styles/app.css` |
| `desktop/` | Shell Tauri v2. `src-tauri/src/main.rs`: avvia il sidecar, passa il token su stdin, comando `engine_info`, chiude l'engine all'uscita. CSP `connect-src` limitato a `127.0.0.1`; al frontend solo `core:default`. Bundle deb/rpm |
| `docs/brand/` | `decisyon-symbol.svg` (dal DS), `decisyon-mark-1024.png` (sorgente per `tauri icon`), `decisyon-logo.jpg` (dal sito, solo riferimento) |

### Aggiunte della wave 1 (branch `feature/v0.2-wave1`, 2026-10-16)

**Tunnel SSM integrato** (agente `tunnel`)
- `engine/internal/tunnel`: tunnel SSM integrato, porting di `open-dloop-tunnels.sh`. Bridge: istanza `Name=<connect.bridge_tag>` (default `ssm-bridge`) nella VPC dell'istanza RDS (`DBSubnetGroup.VpcId`), `running`; errori chiari se nessuna, più d'una, ferma o solo in altre VPC. Sessione: `StartSession` con `AWS-StartPortForwardingSessionToRemoteHost`, poi `session-manager-plugin` come fa l'AWS CLI; la risposta di StartSession passa solo in `AWS_SSM_START_SESSION_RESPONSE` (mai negli argomenti: il token non compare in `ps`); serve un plugin più recente di 1.2.497.0.
- Tunnel su `127.0.0.1` (porta configurata o libera, stabile tra le riconnessioni); riconnessione se il plugin esce o la porta non risponde (sonda ogni 30 s, backoff 1-30 s, azzerato dopo 60 s stabili); `Close()`/fine del context: SIGTERM al plugin poi kill, `TerminateSession`; plugin in un process group suo (Ctrl-C solo all'engine; su Linux SIGTERM se l'engine muore); stati `connecting`/`ready`/`reconnecting`/`closed` (`OnState`).
- `connect.mode: tunnel` attivo in `check`, `status`, `wait`, `databases` e negli endpoint dell'API: ogni operazione apre il suo tunnel e lo chiude alla fine (il pool SQL possiede il tunnel, `mssql.OpenWith`). `config.ErrTunnelNotYet` rimosso; `connect.bridge_tag` validato.
- CLI `dialga tunnel [target] [--port N]`: tunnel in primo piano qualunque sia `connect.mode`; porta `--port` / `connect.local_port` / libera; `-o json` = NDJSON; Ctrl-C/SIGTERM chiudono e terminano la sessione. Sostituisce `open-dloop-tunnels.sh dev-mssql` per la modalità `external`.
- SDK EC2 `v1.338.1` e SSM `v1.79.0` (≥ 2 settimane, nessun bump dei moduli core).
- Prova dal vivo su `mssql-dev` (2026-10-16): `check` 8/8 con TLS verificato attraverso il tunnel integrato, `status`, API `checks`/`tasks`; plugin ucciso con `kill -9` → riconnesso in ~2 s sulla stessa porta; SIGTERM/SIGINT → `closed`, sessioni `Terminated`, nessun plugin rimasto.

**Ispezione dei `.bak`** (agente `inspect`)
- `engine/internal/inspect`: ispezione dei `.bak` in locale (porting di `mssql-bak-inspect.sh`). SQL Server usa e getta in container: podman preferito, docker in alternativa, `DIALGA_CONTAINER_RUNTIME=podman|docker` per forzarlo. Argomenti: `-u 0`; `--security-opt label=disable` solo con SELinux attivo; porta casuale su `127.0.0.1`; label `io.dialga.inspect=true`; password SA casuale solo nell'ambiente. `RESTORE LABELONLY` raggruppa i file per media set (backup striped), poi HEADERONLY e FILELISTONLY via go-mssqldb. Container rimosso sempre, anche su annullamento. Immagine `mcr.microsoft.com/mssql/server:2022-latest` (`DIALGA_INSPECT_IMAGE` o `--image`), mai scaricata in automatico.
- Verdetto: versione del backup oltre la major di `EngineVersion` = non ripristinabile, con rimedio `.bacpac` (runbook, procedura C); cifratura, TDE e FILESTREAM = KO; differenziale, log, più backup set, full-text 2005 = avviso; spazio: avviso oltre l'80% del libero (CloudWatch), KO oltre il 100%, avviso se il libero non è noto. Il target richiede solo AWS in lettura (qualsiasi `connect.mode`).
- CLI `dialga inspect <file.bak>... [-t target] [--image IMG]`: senza `-t` legge solo gli header e non serve la config. Exit: 0 ripristinabile o header letti, 2 non ripristinabile o spazio insufficiente, 1 errore.
- API `POST /api/v1/inspect` `{paths (assoluti), target?, image?}`: SSE `step` → `result` (o `notice`) → `done`; 400/404/409 prima dello stream. Schemi `Inspect*`.
- `inspect.DetectRuntime(ctx)` pubblica: stessa scelta del runtime per `inspect` e per i prerequisiti (`doctor`).
- Test: fixture `testdata/*.json` da backup veri, rigenerate con `go test -tags integration -run TestInspectRealBackups ./internal/inspect -update` (container 2017 e 2022, uno alla volta). `DIALGA_TEST_BACKUP_DIR=<dir>` conserva i `.bak`.
- Provato dal vivo il 2026-10-16 su `mssql-dev` (14.00.3540.1.v1, AWS in sola lettura): `.bak` 2017 ripristinabile, 2022 no (exit 2); anche via docker. Container rimasto dopo un kill: `podman rm -f $(podman ps -aq --filter label=io.dialga.inspect=true)`.
- Limiti: In-Memory OLTP (file `S`) non distinguibile da FILESTREAM in FILELISTONLY → possibile falso "non ripristinabile" (TODO: confermare); backup 2025 non leggibili con l'immagine 2022 (`--image …:2025-latest`); `--restore-as`/`--verify` non portati.

**Operazioni native** (agente `ops`)
- `engine/internal/ops`: operazioni native `restore`/`export`/`finish`/`cancel`/`drop` come `core.Plan` dopo pre-flight in sola lettura (stato del DB, backup su S3 con `HeadObject` o elenco per `*` fino a 10 file, task in corso = avviso). `Execute` legge il `task_id` dalla riga restituita dalla procedura `rds_*` (fallback `MAX(task_id)` come `mssql-import.sh`) e segue il task. `Post` = report post-restore (proprietà, file, accesso del login, utenti orfani, utenti Windows, oggetti). `Digest` identifica il piano.
- Differenze da `mssql-import.sh`: export con nome nudo sotto il prefisso export; differenziale solo su DB `RESTORING`; pre-flight anche per export/finish/cancel/drop; export mai sopra un oggetto esistente (pre-flight + `@overwrite_s3_backup_file = 0`); chiave di default in UTC.
- CLI: `restore <db> <key>` (`--differential`, `--norecovery`), `export <db> [key]`, `finish <db>`, `cancel <task-id>`, `drop <db>`: dry-run di default, `--apply` esegue, conferma riscritta su terminale o `--confirm TEXT` (nome del DB per drop, nome del target se `protected`); `--no-wait`/`--interval` per restore/export. `post <db>` in sola lettura. Exit: 2 pre-flight fallito (anche `post` con DB assente/non online/non accessibile), 3 task `ERROR`/`CANCELLED`.
- API: `POST /api/v1/targets/{name}/plans/{operation}` (anteprima: piano, check, `ready`, `confirmation`, `digest`, `preview`, `request` con i default risolti), `.../execute` (esegue come job `operation`; SSE `job` (`{"id"}`, primo), `notice`/`step`/`task`/`done` (con `jobId`); il job continua se il client chiude lo stream; 412 pre-flight fallito o `digest` cambiato, 422 conferma errata: ogni rifiuto lascia un job `failed` con `refusal`), `GET /api/v1/targets/{name}/databases/{db}/report`. La GUI rimanda `request` + `confirmation` + `digest` dell'anteprima.
- `internal/mssql`: `Session` (`DatabaseState`, `ActiveTasks`, `StartTask`, `Exec`, `DatabaseReport`); `internal/awsx/objects.go`: `Bucket.Size` (`ErrObjectNotFound`), `Bucket.Keys`; `session.OpenWithAWS` (con tunnel integrato).
- Test di integrazione: SQL Server 2022 + `internal/testdata/mock-rds.sql` (copia condivisa del mock), password generate in memoria, `rds_finish_restore` mockato nel test, bucket finto in memoria; il T-SQL eseguito coincide con i golden file. Immagine `mcr.microsoft.com/mssql/server:2022-latest` (override `DIALGA_MSSQL_IMAGE`).
- Da verificare dal vivo su `mssql-dev` (sola lettura): dry-run di `restore`/`export` e `post`; forma reale della riga restituita da `rds_restore_database`/`rds_backup_database`.
- `go build ./cmd/...` scrive il binario `engine/dialga` nel repo: usare `go build ./...` o `-o` fuori dal repo.

**Database e overview** (agente `databases`)
- `engine/internal/mssql`: `Catalog` (`Databases`: DB utente da `sys.databases` senza i DB di sistema e `rdsadmin`, con dimensioni dati/log in MiB da `sys.master_files`, servono `VIEW ANY DEFINITION`; `Usage`: numero di DB utente e task nativi non finiti), interfaccia `DatabaseReader`.
- `engine/internal/session`: `OpenDatabases` (sessione per leggere i DB) e `DescribeTarget` (solo AWS: account, istanza, spazio libero da CloudWatch; niente password SQL).
- `engine/internal/awsx`: `Instance.Class` (classe dell'istanza).
- API: `GET /api/v1/targets/{name}/databases` (stessi codici di `tasks`: 409/404/422/502) e `GET /api/v1/targets/{name}/overview` (parte AWS e parte SQL in parallelo; solo un errore AWS dà 502; senza password o con `tunnel` `sql: skipped`, sessione o query fallita `sql: failed`, entrambi con `sqlDetail`; i campi ignoti mancano, non valgono 0).
- CLI: `dialga databases [target]` (alias `dbs`), tabella testo o JSON.
- GUI: pagina **Database** (`#databases`, `#databases/<target>`): selettore del target (ultimo in `localStorage` `dialga.databases.target`), tabella ordinabile da tastiera (`aria-sort`), filtro, skeleton/empty/errore. Nelle schede Istanze la striscia di riepilogo (stato, edizione e classe, barra dello spazio libero, DB utente con link alla pagina Database, task attivi) si carica quando la scheda è visibile.
- Test di integrazione: `TESTCONTAINERS_RYUK_DISABLED=true go test -tags integration ./...` (testcontainers-go v0.44.0).
- Limiti: overview con password ripete STS e DescribeDBInstances (serve una variante di `session.Open` con istanza già descritta); dimensioni 0 senza `VIEW ANY DEFINITION`; nessuna azione nella pagina Database (export, drop: v0.2 seconda parte).

**Primo avvio e configurazione** (agente `firstrun`)
- `engine/internal/config/write.go`: scrittura della config. `Update` (lettura → modifica → `Save`), `SetTarget`/`RemoveTarget`, `LoadOrEmpty`, `Check`, `SupportedPasswordSource`, `Valid*`. Scrittura atomica (temporaneo nella stessa cartella + rename), file 0600, cartella 0700 se la crea, copia precedente in `config.yaml.bak`, link simbolico conservato. **I commenti non si conservano** (intestazione nei file scritti). Un file illeggibile non viene mai sovrascritto (`*InvalidFileError`); togliere l'ultimo target cancella il file (primo avvio). Target nuovi: password solo `prompt` o `env:VAR`, l'errore non ripete il valore rifiutato. Scritture concorrenti serializzate solo nello stesso processo.
- `engine/internal/discover`: profili AWS dai file condivisi (delle credenziali **solo le intestazioni delle sezioni**, dal config anche la regione); istanze `sqlserver-*` (esclusa RDS Custom) con `SQLSERVER_BACKUP_RESTORE` per option group, master user, account da `GetCallerIdentity`; prerequisiti `session-manager-plugin` (`$PATH`, `/usr/local/bin`, `/usr/local/sessionmanagerplugin/bin`; ≤ 1.2.497.0 = `outdated`) e podman/docker (`DIALGA_CONTAINER_RUNTIME`), con comandi di installazione Fedora e Debian/Ubuntu. Interfaccia `discover.Service` (`System` reale).
- CLI: `config init` (guidato su terminale: profilo → regione → istanza → bucket con HeadBucket → login, default master user → password → connessione → nome → protetta; senza terminale almeno `--instance` e `--bucket`; `--no-bucket-check`, `--replace`, `-y`); `config add-target <nome>` (solo flag, nessuna chiamata AWS); `config remove-target <nome>` (riscrivere il nome o `--confirm`); `doctor` (plugin, runtime, profili, config, per target profilo e variabile della password; nessuna chiamata AWS; exit 0 o 2; `-o json`).
- API: `GET /api/v1/prerequisites`, `GET /api/v1/discovery/profiles`, `GET /api/v1/discovery/instances?profile=&region=` (400/502), `POST /api/v1/config/targets` (201 aggiunto, 200 sostituito con `replace`, 400 validazione o campi sconosciuti come `password`, 409 nome usato o file non valido), `DELETE /api/v1/config/targets/{name}?confirm=<name>` (204/400/404/409); CORS ammette DELETE.
- GUI: "Add instance" (stato vuoto e accanto ad Aggiorna) apre un `<dialog>` modale nativo (profilo → regione → ricerca → bucket, login, password `DIALGA_SQL_PASSWORD` o variabile, connessione e porta, nome, protetta, sostituisci). In Impostazioni le schede Target (Rimuovi riscrivendo il nome) e Prerequisiti (Trovato / Mancante / Da aggiornare). Aiuti puri in `web/src/targetForm.ts`. Tema scuro: pulsante distruttivo più scuro (il rosso del DS con testo bianco non raggiunge AA).
- Provato dal vivo il 2026-10-16 (sola lettura, config temporanee): `config init` su `sales`/`mssql-dev`, `validate`, `doctor`; nella UI aggiunta, sostituzione e rimozione di un target.

**Test web** (agente `webtests`)
- Test web: `pnpm -C web run test` esegue due progetti Vitest: `unit` (`src/**/*.test.ts`, Node) e `dom` (`src/**/*.test.tsx`, jsdom + Testing Library + jest-dom). Un test di componente nuovo è un `*.test.tsx` con gli helper di `web/src/test/` (`mockFetch`/`requestsTo`/`sseStream`/`deferred` in `http.ts`, `renderI18n` e fixture in `fixtures.ts`, `installMatchMedia` in `media.ts`). Polling: `vi.useFakeTimers({ shouldAdvanceTime: true })` + `act(() => vi.advanceTimersByTimeAsync(ms))`.
- E2E: `pnpm -C web run e2e` (Playwright, solo Chromium; in locale una volta `pnpm -C web exec playwright install --only-shell chromium`, mai `--with-deps`). Il global setup compila l'engine (`go build`, oppure `DIALGA_BIN`), avvia `dialga serve` con `DIALGA_DEV=1`, config temporanea, AWS vuoto, nessuna password SQL (nessuna chiamata di rete), fa la build della web app con `VITE_ENGINE_*` e la serve con `vite preview` su una porta libera; una fixture fa caricare le pagine da `http://localhost:1420` riscrivendo le richieste (Origin ammessa dall'engine anche con la 1420 occupata). Log e trace in `web/test-results/` (ignorata da git).
- Durate 2026-10-16: Vitest 87 test ~1 s; e2e 6 test ~3 s con cache Go calda.
- `e2e/` e `playwright.config.ts` non passano da `tsc` (servirebbe `@types/node`); restano coperti da Biome.
- Dipendenze: età minima 14 giorni anche per le transitive; le versioni troppo giovani si fissano con `overrides` in `pnpm-workspace.yaml` (quelle del 2026-10-16 per jsdom/Testing Library si possono togliere dopo il 2026-10-28). Controllo: pacchetti nuovi in `pnpm-lock.yaml` + `npm view <pkg> time --json`.

**Pacchetti Linux e rilascio** (agente `packaging`)
- `ci/bump-version.sh X.Y.Z` aggiorna la versione in `tauri.conf.json`, `Cargo.toml`, `Cargo.lock` (voce `dialga-desktop`), `desktop/package.json`, `web/package.json`; `--check [v]X.Y.Z` confronta soltanto. Tutto o niente, solo `MAJOR.MINOR.PATCH`; fallisce se un manifest con versione non è nella lista. Rilascio v1, passo 1: usare lo script, non modificare i file a mano.
- Rilascio documentato in `docs/release.md` (CHANGELOG, bump, tag, upload manuale, `SHA256SUMS`, installazione, primo avvio, rollback); `CHANGELOG.md` in formato Keep a Changelog, v0.1 sotto `[Unreleased]`.
- Policy IAM minima in `docs/iam-policy.json` (segnaposto `[account-id]`, `[region]`, `[bucket]`, `[import-prefix]`, `[export-prefix]`), spiegata in `docs/iam-policy.md`.
- Pacchetti deb/rpm (nome `dialga`): `/usr/bin/dialga` è il sidecar ed è anche la CLI nel `PATH`; `/usr/bin/dialga-desktop` è l'app; `Dialga.desktop` generato da `desktop/src-tauri/linux/desktop-entry.hbs`; icone hicolor 16-512 px; in `/usr/share/doc/dialga/` `config.example.yaml`, `iam-policy.json`, `CHANGELOG.md`.
- Dipendenze: bastano i default di Tauri (deb `libwebkit2gtk-4.1-0, libgtk-3-0`; rpm i soname corrispondenti); l'engine è Go statico.
- Tauri usa la prima PNG di `bundle.icon` come icona della finestra: tenere `icons/128x128.png` in testa.
- glibc: la build locale su Fedora 43 richiede glibc 2.39; la pipeline (`node:24-bookworm` = Debian 12, glibc 2.36) no. Pubblicare solo i pacchetti della pipeline.
- TODO: confermare licenza, email del maintainer deb, compatibilità con Ubuntu 22.04, comportamento dei Downloads quando si ricarica un file con lo stesso nome.

**Tunnel condiviso** (agente `tunnel`, seconda parte)
- `engine/internal/tunnels`: un tunnel SSM condiviso per ogni target in modalità tunnel, finché `dialga serve` è attivo. Chiave: account, regione, profilo, istanza, `local_port`, `bridge_tag` (un target modificato apre un tunnel nuovo). Apertura al primo uso o su richiesta (Connect); chiamate concorrenti si accodano a un solo tentativo e ne condividono l'errore. Chiusura dopo 10 minuti senza utenti se aperto dall'uso; se aperto su richiesta resta fino a Disconnect. Durante connessione/riconnessione chi lo chiede aspetta al massimo 1 minuto. Allo stop dell'engine (`Close()`) tutti chiusi con `TerminateSession`.
- L'API usa il tunnel condiviso per tasks, databases, overview e check (`session.Sessions{Tunnels}`, `check.Request.Lender`); `Sessions.OpenWithAWS` presta il tunnel anche a chi serve la config AWS. La CLI resta com'era: un tunnel per comando, salvo i job (`import`/`run`/`jobs resume`: un tunnel per job, vedi la wave 2). I tunnel che una sessione o un check della CLI apre per sé prendono sempre una porta libera (`session.PrivateTunnelTarget`): `connect.local_port` vale solo per il tunnel condiviso dell'app e per `dialga tunnel`.
- Rotte: `GET /api/v1/tunnels`; `GET/POST/DELETE /api/v1/targets/{name}/tunnel`; `GET /api/v1/targets/{name}/tunnel/events` (SSE `state`). Stato: connecting/ready/reconnecting/closed con localPort, bridge, detail, since, users, keepOpen, failed. 422 `NotTunnelMode` per i target non in modalità tunnel.
- GUI: indicatore nella scheda di ogni istanza in modalità tunnel (pallino, stato, porta, bridge, motivo in riconnessione/errore, Connetti/Disconnetti) e riepilogo nella sidebar (`TunnelsProvider`, `GET /api/v1/tunnels` all'avvio e ogni 30 s), in tempo reale via SSE; live region solo per connesso/in riconnessione/chiuso/non riuscito.
- Prova dal vivo del 2026-10-16 su `mssql-dev`: 5 chiamate parallele = un solo plugin (~4,5 s a freddo, poi ~1,6 s per chiamata); `kill -9` del plugin → GUI da "Riconnessione…" a "Connesso" in ~5 s; check dalla GUI tramite tunnel condiviso 8/8; chiusura dello stdin dell'engine = nessun plugin né sessione rimasti.
- Shell desktop: all'uscita (`RunEvent::Exit`) scrive "shutdown" sullo stdin dell'engine, aspetta fino a 10 s che termini (l'engine chiude i tunnel con TerminateSession) e lo uccide solo se è ancora vivo. Prima lo uccideva subito (SIGKILL) e la sessione SSM restava aperta fino al timeout. Protocollo engine ↔ shell: l'engine si ferma su EOF dello stdin, sulla riga "shutdown" o su SIGINT/SIGTERM; in ogni caso prima chiude i tunnel condivisi. Provato il 2026-10-16 con il binario release: chiusura della finestra → app chiusa in 0,5 s, sessione SSM Terminated, nessun plugin rimasto.

**Trasferimenti S3** (agente `transfer`)
- `engine/internal/transfer`: trasferimenti S3 (§3.9 del piano). Upload multipart con parti dimensionate sul file (16 MiB fino a 1 GiB, 64 MiB fino a 625 GiB, poi ≤ 10.000 parti; max 5 TiB), 4 in parallelo, retry per parte con backoff e jitter, timeout adattivo con minimo 1 min, CRC64NVME FULL_OBJECT verificato da S3 e riletto con HeadObject (anche dopo un Complete ambiguo). Annulla = AbortMultipartUpload; pausa (causa `ErrPaused`) = parti tenute per `Upload.Resume` da ListParts; hook `Journal` per la v0.5 (per i job: `ErrInterrupted` deve fare match con `transfer.ErrPaused`). Mai sostituire senza overwrite (If-None-Match: *). Download a range paralleli su file temporaneo, If-Match, spazio libero, CRC64NVME/CRC32/CRC32C verificati, file 0644 (o il modo del file sostituito). `PlanUploads`/`PlanDownload` = `core.Plan` + file rifiutati con motivo. Avanzamento su una goroutine propria (un client lento non rallenta l'upload).
- CLI `upload`/`download` (`cmd_transfer.go`): dry-run di default, `--apply`, `--confirm` (target protetto o sostituzione), `-o json` = documento su stdout + eventi JSON lines su stderr. Exit 2 = file rifiutati (gli altri si trasferiscono). SIGTERM/SIGHUP fanno abort pulito.
- API `POST /api/v1/targets/{name}/uploads|downloads`: `apply:false` → piano, `apply:true` → SSE `plan`/`step`/`progress` (`TransferProgressEvent`)/`done`; client che chiude = annulla.
- `api.Serve`: i contesti delle richieste seguono quello di Serve; allo shutdown prima si chiudono i tunnel, poi si aspetta fino a 40 s (`ShutdownTimeout`) perché gli upload facciano abort.
- Test: `internal/transfer/transfertest` = S3 in memoria; integrazione MinIO in `internal/transfer` e `cmd/dialga` tramite `transfertest.RunMinIO` (build tag `integration`): immagine `docker.io/bitnamilegacy/minio:2025.4.22`, stessa release 2025-04-22T22-12-26Z, avviata con il binario `minio` su `/bitnami/minio/data` (utente non root). MinIO non pubblica più le immagini community: quay.io e Docker Hub rispondono unauthorized e in CI il pull fallisce (pipeline #23). Se sparisce anche Bitnami legacy, le alternative scaricabili sono `pgsty/minio` (fork) o un altro S3 compatibile, da riverificare su CRC64NVME e If-None-Match. Mai provato su S3 reale: alla prima prova usare una chiave di test e verificare CRC64NVME FULL_OBJECT, If-None-Match e MpuObjectSize su CompleteMultipartUpload.

**Job multi-DB** (agente `packaging`, seconda parte)
- `engine/internal/jobs`: job multi-DB (v0.5). Si avviano con `Manager.Start(Spec)`: `ItemSpec` per DB, `Step` (ID, Title, Resource, Run con `Reporter`), adattatori `NewStep` e `FromAction`.
- Scheduler condiviso da tutti i job: `transfer` con limite globale (default 3, `Options.Limits`), `rds-task` al massimo `Spec.MaxTasks` per istanza (chiave `InstanceKey`). Lo slot resta preso fra step consecutivi della stessa risorsa (restore + wait). Priorità: il job più vecchio, poi l'ordine dei DB: mentre il DB n fa il restore, il n+1 carica.
- Un DB fallito non ferma gli altri. Annulla = causa `ErrCancelled` (l'upload multipart si annulla); spegnimento dell'engine = causa `ErrInterrupted` (l'upload resta per la ripresa). Un task RDS già in corso non viene mai ucciso: il DB ha `taskLeftRunning` e un avviso nel log.
- Come si collegano gli step: transfer = `ResourceTransfer`, `r.Transfer(...)` e `r.Upload(UploadState{...})`; ops = due step `ResourceRDSTask` consecutivi ("restore" invia, "wait" segue con `mssql.Wait`); inspect = risorsa `container`; post = `NoResource`. La conferma (target protetto, azione distruttiva) si verifica prima di `Manager.Start`.
- Eventi: `job.*`, `step.*`, `upload.progress` (velocità ed ETA calcolate dall'engine, max 4/s, solo live), `task.status` (nel log solo i cambi), `log`; `seq` progressivo per job.
- Journal: `$XDG_STATE_HOME/dialga/jobs` (o `~/.local/state/dialga/jobs`; fuori Linux `os.UserConfigDir()/dialga/jobs`). Un JSON per job, scrittura atomica (temp, fsync, rename, 0600), progressi al massimo ogni 2 s. **Condiviso fra processi** (engine dell'app e run CLI, `Options.Origin` = `app`|`cli`): ogni processo scrive solo i propri job; id unico (`claimID`: lock dell'id + il file non esiste); nessun lock globale. **Lock per job** `flock` su `.<id>.lock`, preso da `Start`/`Resume`/`Record` e tenuto fino alla fine del job (il kernel lo rilascia se il processo muore; no-op fuori Unix, TODO). Un job non finito con lock libero è orfano → `interrupted` (lo marca chi lo trova, tenendo il lock); con lock preso gira altrove → `run.foreign`: si elenca e si segue (rilettura del file, `Manager.rescan`/`sync`, `List`/`Get`/`Subscribe` rileggono, throttle 300 ms; `follow` ogni 500 ms), non si tocca; `Cancel` → `ErrForeign` (409). `Resume` prende il lock del job (`ErrLocked`, 409). I `.tmp` si rimuovono solo se vecchi di un minuto (un altro processo può star scrivendo). Retention (200 job o 30 giorni) applicata sull'insieme di tutte le origini, e rimuove anche i `.lock`.
- Tipi di job: `import`, `export`, `operation` (`Job.Operation` = `restore|export|drop|finish|cancel|post`). Le singole operazioni (`pipeline.OperationSpec`) hanno un item (database, o `task N` per cancel) e gli step del piano riusando i kind `pipeline.task|wait|sql` (`SQLParams` ha `finish`/`cancel`; `Env.OnTask` passa il task completo al chiamante). `POST .../plans/{op}/execute` e `dialga restore|export|drop|finish|cancel --apply` le eseguono nel `Manager`; `post` è un job solo come passo di un piano (il GET del report è una vista e non lascia job). I rifiuti (412/422/428/502) si registrano con `Manager.Record` e `Job.Refusal` (`api/refusals.go`: `refusalWriter` + `recordRefusal` in defer; la CLI fa lo stesso per 412/422); anteprime e `dryRun` mai.
- API: `GET /api/v1/jobs`, `GET /api/v1/jobs/{id}`, `GET /api/v1/jobs/{id}/events` (SSE: replay da `Last-Event-ID` o `?after=`, poi live, fino al `job.*` finale), `POST /api/v1/jobs/{id}/cancel` (202, 409 se finito o di un altro processo). `api.Options.Jobs` = manager in memoria di default; `serve` apre quello con il journal.
- Creazione dei job (`POST /api/v1/jobs`, pipeline) e ripresa dei job interrotti: vedi la wave 2. Da fare: controllo dei task avviati fuori da Dialga prima dell'invio.

**Accessibilità** (agente `databases`, seconda e terza parte)
- `web/src/hints.ts`: per gli errori che non dicono cosa fare, suggerimento (EN/IT) ricavato da una parte nota del messaggio dell'engine (profilo, account, login SSO, credenziali, variabile della password, tunnel, login SQL, TLS, istanza, bucket, permessi) o dallo stato HTTP (401/404/409). Mostrato da `Feedback.tsx` (`ErrorBanner`, `HintText`) sotto il messaggio, nei passi del check falliti, nella striscia di riepilogo e in Impostazioni. `api.ts`: `ApiError` porta lo stato HTTP. I suggerimenti dipendono dalla formulazione inglese dei messaggi dell'engine: se cambia, il suggerimento sparisce (i test coprono la formulazione attuale).
- Accessibilità (WCAG 2.2 AA) verificata il 2026-10-16 su Istanze, Database e Impostazioni, tema chiaro e scuro, 1280/640/420/320 px. Lighthouse accessibilità 100 su tutte e sei le combinazioni (prima 95-96).
- `app.css`: toni di testo AA (`--tone-success/info/warning/destructive`), riempimento dei pulsanti `--primary-fill` (in scuro resta #5b8210: il bianco su #6f9c14 è 3,3:1), `--control-border` a 3:1, focus ad anello pieno di 2px. I token del DS (`ds-tokens.css`) non si toccano.
- Pulsanti occupati: `aria-disabled` invece di `disabled` (`disabled` faceva perdere il focus). Link "Vai al contenuto", titolo della finestra per pagina, focus sul titolo `h1` a ogni cambio pagina, Impostazioni dentro il landmark di navigazione (`.nav-bottom`), una sola live region "polite" per scheda per avanzamento ed esito del check, nomi distinti per i pulsanti di aggiornamento a icona.
- Pulsanti Aggiorna con nomi accessibili distinti che contengono il target ("Refresh the overview of dev-sql") e iniziano con il testo visibile; aria-busy vero dal primo render finché i dati non arrivano.
- Interfaccia del primo avvio verificata il 2026-10-16 (Lighthouse accessibilità 100): dialogo "Aggiungi istanza" con focus restituito a chi lo apre anche in strict mode, niente header/footer nel dialog, campi `aria-required`, Salva occupato con `aria-disabled`, errori con suggerimento; schede Target e Prerequisiti con pulsanti occupati in `aria-disabled` e conferma della rimozione in una live region sempre presente.
- `app.css`: le regole dei campi (`.input`/`.select`) stanno in un solo posto; i nuovi blocchi CSS non devono ridefinirle (perderebbero bordo a 3:1 e anello di focus). `ErrorBanner` ha l'opzione `preformatted` per i messaggi su più righe.

**GUI delle operazioni** (agente `ops`, seconda parte)
- `web/src/PlanDialog.tsx`: dialogo riusabile per qualsiasi piano: anteprima con i controlli pre-flight, le azioni con il T-SQL esatto (Dettagli), badge distruttivo, campo di conferma ("Digita `<testo>` per confermare", Esegui attivo solo sul testo esatto; target protetto = nome del target). Esecuzione via SSE: passi, avanzamento del task (`TaskProgress.tsx`, condiviso con `TasksPanel`), esito con cosa fare (restore → "Apri il report", export → URI `s3://`). 412 = situazione cambiata, nuova anteprima; 422 = errore con suggerimento. Modificare un campo dopo l'anteprima disattiva Esegui. Chiudere mentre un task corre ferma solo l'attesa (il dialogo lo dice; la pagina Database mostra "Il task #N continua").
- Pagina Database: colonna Azioni (Export, Report, Drop, Finish per i DB `RESTORING`) e "Restore from S3"; `ReportDialog.tsx` = report post-restore. `TasksPanel`: Annulla sui task in corso. `Modal.tsx`: `<dialog>` nativo (focus intrappolato, Escape, focus restituito).
- Engine: `ops.Execute` porta a termine l'istruzione `rds_*` anche se il client se ne va (il contesto ferma solo l'attesa).

**Documentazione** (agente `inspect`, seconda parte)
- `docs/guida-utente.md` (italiano): installazione, prerequisiti, configurazione, ogni comando della CLI con output reale anonimizzato, GUI, risoluzione dei problemi (messaggio, causa, cosa fare); incluso nei pacchetti in `/usr/share/doc/dialga/`. Segnaposto `> In arrivo` per wizard, Attività e job (da riempire: ora esistono, vedi la wave 2). `docs/release.md`, `docs/iam-policy.md` e `CHANGELOG.md` tradotti in italiano (sezione `[Non rilasciato]`). TODO: confermare dove pubblicare il runbook della procedura `.bacpac`.

### Aggiunte della wave 2 (branch `feature/v0.2-wave2`, 2026-10-16)

**Pipeline dei job** (agenti `transfer`, `pipeshare`)
- `engine/internal/pipeline`: job di import/export su più DB. Import = `upload` (slot transfer) → `restore` + `wait` (uno slot RDS dal submit alla fine del task) → `post` (non con `noRecovery`); export = `export` + `wait` → `download` (solo con `destination`). `Build` fa il pre-flight di tutti i DB prima di partire (il controllo `source` del restore è omesso: l'upload mette il backup), più chiavi/file duplicati; una conferma per job (target se `protected`, altrimenti cosa si sostituisce) e un `Digest`. Stripe: `files` (fino a 10), chiave `*` = inizio e fine comuni; KO se il pattern tocca altri oggetti. Step costruiti da parametri JSON (`UploadParams`, `TaskParams`, `WaitParams`, `PostParams`, `DownloadParams`) per la ripresa.
- API `POST /api/v1/jobs`: `dryRun` → 200 piano; avvio → 201 + `Location`; 412 `{reason, plan}`; 422 conferma o password; `skipFailing`. Rimandare `items[].key` e `digest` dell'anteprima.
- CLI `dialga import <file>...` (`--db`, `--key`/`--prefix`, `--differential`, `--norecovery`, `--overwrite`, `--skip-failing`, `--interval`) e `dialga run <piano.yaml>` (schema in `docs/piani-job.md`): job in primo piano, Ctrl-C = annulla. Exit 0/2/3/1. I job della CLI restano in memoria (non compaiono in Attività).
- **Un tunnel per target e per job:** in `serve` le sessioni ops passano da `session.Sessions{Tunnels}` (lease del `tunnels.Manager`; `ops.OpenWith`, `opsOpenWith` nell'API); nella CLI `app.jobOpenOps` apre il tunnel una volta (`app.jobTunnel`, `app.openOpsVia` nei test) e lo chiude a fine job, anche per `jobs resume`.
- `pipeline.Build` controlla a plan-time lo spazio libero della cartella di destinazione di un export (`Env.FreeSpace`, default `transfer.FreeBytes`): avviso se non basta; il controllo reale resta al download.
- **Checksum:** gli step di upload/download lo passano ai `Reporter` che hanno `Checksum(...)`; `jobs.Reporter` lo registra in `Item.Checksum` (`{algorithm, value, verified, note}`, schema `JobChecksum`, tipo web `JobChecksum`). Con più file in un upload c'è un solo checksum d'insieme (`verified` = tutti, il dettaglio è nello step). Il risultato dell'Esporta lo mostra anche in modalità job.
- Test di integrazione `internal/pipeline`: MinIO (`transfertest.RunMinIO`) + SQL Server 2022 + mock; il test fa da RDS (restore solo se il backup è in S3, backup scritto nel bucket). `internal/inspect` può fallire con «containers left» se un'altra esecuzione usa Docker (label condivisa): rilanciarlo da solo.
- Limiti: nome DB dal file, non dall'header; full+differential dello stesso DB non nello stesso job; mai provato su AWS reale.

**Ripresa dei job** (agenti `packaging`, `jobs2`)
- `Manager.Resume(id, ResumeOptions{Env, InstanceKey})`: solo i job `interrupted`, stesso id; il log continua con `job.resumed`. I DB terminati e gli step riusciti restano; gli altri li ricostruisce la factory del loro `kind` (`jobs.Register`, `DefaultRegistry`; `Options.Registry` nei test), che riceve `Rebuild{Job, Item, Step, Env}`; una factory che restituisce nil marca lo step come fatto. Step non ricostruibile = `ErrNotResumable`, nulla cambia. `InstanceKey` = `<account_id>/<region>/<instance>` (`api.InstanceKey`).
- Step riprendibili: `jobs.WithKind(step, kind, params)` (kind `pipeline.*`, params JSON, mai segreti) su `StepInfo`; `Reporter.Checkpoint` li aggiorna; `Reporter.LastTaskID` dà l'ultimo task dell'item (il `wait` non ha più stato privato: `ItemState` rimosso). Le factory della pipeline sono in `internal/pipeline/resume.go`.
- Un `restore`/`export` con `TaskID` già registrato non si riaccoda (factory nil, il `wait` segue quel task). L'upload riprende da `ListParts` (`Upload.Resume`) con l'`UploadState` dell'item; i file finiti sono marcati `Done` via `Checkpoint` e saltati. Limiti: se l'engine muore tra l'accettazione del restore e il journal, il restore si riaccoda (RDS lo rifiuta, visibile); il download riparte da zero.
- API `POST /api/v1/jobs/{id}/resume` (body `{"confirm"}` per i target protetti, 202 col Job): 409 se non interrotto; 422 per target sparito o su un'altra istanza, conferma mancante/errata, nessuna password, step non ricostruibile; 501 senza pipeline (`Options.JobEnv` nil; di default = `pipelineEnv`); 502 se l'ambiente non si costruisce.
- CLI `dialga jobs resume <id>` (`--confirm`, `--dir`): esegue il job in questo processo (`app.jobEnv`, `app.jobInterval`) e prende il lock del job (`ErrLocked`: se l'app o un altro `dialga` lo esegue, rifiuta e rimanda ad Attività; senza chi lo tenga funziona anche con l'app aperta). Ctrl-C lo interrompe di nuovo. Exit 0 riuscito, 3 fallito o annullato, 1 errore.
- Test: `TestIntegrationResumeInterruptedJobs` (3 interruzioni: a metà upload, a metà wait, dopo il restore accodato; `rdsHold` tiene in coda i task di un DB).

**GUI: Attività e wizard** (agenti `packaging`, `ops`, `firstrun`, `databases`, `wizards`)
- Attività (`#activity`, `#activity/<id>`): elenco e dettaglio dei job, annulla, pulsante Riprendi sui job interrotti (domanda in linea; nome da riscrivere per i target protetti), poi segue il job dal suo `job.resumed`.
- UI dei piani unica (dialogo e wizard): `plans.ts` (client; `planHint` = fallback di `ErrorBanner` dopo `hints.ts`), `PlanPreview.tsx` (`PlanPreviewView`: `title`, `showConfirm`, `pending`, `readyWith`), `PlanRun.tsx` (`usePlanRun`, `PlanRunView`, `PlanAnnouncer`), `TaskProgress.tsx`; `PlanDialog.tsx` le usa in un modale.
- `web/src/Wizard.tsx`: guscio condiviso Import/Export (stepper con `aria-current="step"`, ritorno ai passi fatti, bloccato durante e dopo l'esecuzione, pannello «Passo N di M» che prende il focus). Classi `wizard-*` in un solo blocco CSS.
- **Esporta** (`#export`, `#export/<target>?db=…`, anche da «Esporta i selezionati» in Database): database → opzioni (chiave S3 per DB, vuota = default dell'engine; cartella di download assoluta) → finestre di backup e manutenzione (da `backupWindow`/`maintenanceWindow` dell'overview, UTC; avviso se le attraversa) → anteprima → avanzamento → esito (URI S3, file locale, checksum). `exportBackend.ts`: con `POST /api/v1/jobs` è un job (sopravvive alla pagina, compare in Attività); con 405 ripiega su piano + download (`exportRun.ts`).
- **Importa** (`#import`): un solo job per tutti i backup (`importItems`, `startItems`, `importReady` in `importWizard.ts`), passo File con la scelta dell'istanza; le chiavi striped (`*`) non si rimandano, le ricostruisce l'engine; una sola conferma (`plan.confirmation`); risultato → `#activity/<id>`, «Cancel import». Chiamate condivise con l'Esporta in `exportJob.ts` (`previewJob`, `startJob`, `followSnapshots`). 412 all'avvio: mostra `reason` e rifà l'anteprima; 422: messaggio, resta sull'anteprima. Uscire dalla pagina non ferma il job.
- Add instance propone di default il tunnel integrato (`connect.mode: tunnel`, senza porta); `external` (11433) e `direct` restano selezionabili. Lo stesso default vale per `dialga config init` e `config add-target` (deciso da Fabio il 2026-10-16).
- Selettore cartella/file nativo: serve `tauri-plugin-dialog` (crate nuovi, da chiedere): oggi il percorso si scrive a mano. `web/src/uploads.ts` (adattatore degli upload) rimosso.

**Correzioni** (agente `ops`)
- CLI `wait` cerca prima il task; un glob di restore con un solo risultato usa la chiave esatta; il mock `rds_fn_task_status` espone `duration(mins)`; si registra da dove viene il `task_id`; `TunnelIndicator` occupato con `aria-disabled`, errori con `ErrorBanner` e una sola GET.

### Password SQL (branch `feature/sql-password` + `feature/sql-password-gui`, 2026-10-16)

- **Resolver unico** `session.Passwords` (`engine/internal/session/passwords.go`), usato da CLI, API e job (avvio e ripresa); i parametri dei job non la contengono mai. Ordine: memoria dell'engine (non per `env:` e `secretsmanager:`) → fonte configurata. `prompt` = memoria → `DIALGA_SQL_PASSWORD` → voce del portachiavi; `env:VAR`; `keyring` (servizio `dialga`, account = nome del target); `secretsmanager:<ARN>` (GetSecretValue con profilo, regione e guardia account del target; JSON con `password` come i segreti gestiti da RDS, o testo). La memoria si perde se il target è rimosso o cambiano istanza, account, regione o `sql.user`.
- **Portachiavi** (`keyring.go`): interfaccia `Keyring` + `SystemKeyring()` su `zalando/go-keyring` (Secret Service su Linux); `Usable()` sonda con una lettura (cache 30 s), presenza della voce in cache 15 s. Nei test `keyring.MockInit()` (TestMain in `internal/api` e `cmd/dialga`); negli e2e `web/e2e/global-setup.ts` dà all'engine un D-Bus inesistente, così il portachiavi dell'utente non viene mai toccato.
- **API**: `TargetSummary.password {source, available, canRemember, envVar?}` (`envVar` = solo il nome: `VAR` o `DIALGA_SQL_PASSWORD`). `PUT /api/v1/targets/{name}/password` (`{password, remember}`, `?verify=1` apre prima una sessione SQL: 422 solo per login rifiutato, 18456; 502 se AWS o tunnel non rispondono; 409 con fonte `env:`/`secretsmanager:`; 501 `remember` senza portachiavi). `DELETE` dimentica memoria e portachiavi. Senza password: **428** + header `Dialga-Password-Target` (testo `SQL password needed for target <nome>`) su databases, tasks, piani, execute, report, `POST /jobs` (anche dryRun) e resume; l'overview resta 200 con `sql: skipped`; nel check SSE il passo SQL è `skipped` con dettaglio. Le fonti `env:`/`secretsmanager:` che non danno password restano 422. CORS: `PUT` e `DELETE` consentiti, `Dialga-Password-Target` esposto su ogni risposta.
- **CLI**: `dialga password set|forget|status [target]` (`cmd_password.go`; `set` chiede senza eco o legge una riga da stdin). Il flusso `prompt` degli altri comandi prova prima il portachiavi.
- **GUI**: `PasswordProvider` (in `App`) con dialogo e contatore `epoch`: i caricamenti (Databases, wizard Export, overview, task, report, anteprima dei piani, check) si rieseguono da soli dopo il salvataggio; le azioni (esecuzione di un piano, avvio o ripresa di un job) dicono "Password salvata: ripeti l'azione". `ErrorBanner` mostra `PasswordNeededBanner` ("Enter password") al posto del 428. `PasswordDialog`: campo `current-password` con mostra/nascondi, "Remember in the system keyring" solo con `canRemember`; il valore non esce dallo stato del dialogo. `PasswordState` nella card Istanze (origine + "Enter password"/"Forget password"). Il suggerimento sotto gli errori di sessione SQL dipende dalla modalità di connessione (`sqlSessionHint`).
- **Dipendenze** (approvate da Fabio per questo task): `zalando/go-keyring` v0.2.8 (2026-03-23), `aws-sdk-go-v2/service/secretsmanager` v1.50.1 (2026-09-24); transitive `godbus/dbus/v5` v5.2.2, `danieljoos/wincred` v1.2.3. Policy IAM: statement facoltativo `secretsmanager:GetSecretValue` (+ `kms:Decrypt` con chiave gestita dal cliente).

### Restore su un database esistente (branch `feature/restore-replace` + `feature/restore-replace-gui`, 2026-10-16)

- Engine: strategie `replace: auto|swap|drop` (+ `backupFirst`, default true) in `engine/internal/ops/replace.go`; i passi sono `ops.ReplaceStep`, usati sia come azioni di `ops.Prepared` sia come step di job (`pipeline.replaceItem`). **swap** = restore con nome temporaneo `<db>_dialga_new_<stamp>` → wait → SINGLE_USER → rinomina `<db>` → `<db>_old_<stamp>` → rinomina del temporaneo → `<db>` → MULTI_USER → post (se la seconda rinomina fallisce, il vecchio torna `<db>`). **drop** = [backup su S3 `<prefix>/<db>_before_replace_<stamp>.bak` + wait] → `rds_drop_database` → restore → wait → post. **auto** sceglie swap se due copie stanno nello spazio libero, altrimenti drop con backup; se spazio o dimensione non sono leggibili sceglie swap con un avviso. Conferma: nome del database (più il target se protetto). Un replace non ammette `--no-wait`.
- Step nuovi in pipeline: kind `pipeline.sql` (`SQLParams`: drop, single-user, multi-user, rename), `TaskParams` id `backup`, `WaitParams.ID`, `PostParams.Note`. Le factory rivalidano e ricostruiscono le istruzioni; gli step leggono i nomi sull'istanza prima di agire (ripresa sicura tra le due rinomine).
- `stamp` (yyyymmddhhmmss UTC) dà i nomi; il digest lo ignora; l'anteprima lo restituisce (`request.stamp`, `plan.stamp`) e la GUI lo rimanda (nei job al livello alto di `JobRequest`). Risposte: `PlanPreview.replace`, `PlanDone.result` (testo).
- Rinomina = `EXEC rdsadmin.dbo.rds_modify_db_name` — verificata su `mssql-dev` il 2026-10-19 (esiste, `god` ha EXECUTE, parametri `@old_db_name`, `@new_db_name`). Mock nel database `rdsadmin` di `engine/internal/testdata/mock-rds.sql` (con `ALTER DATABASE … MODIFY NAME`); la copia in `~/Claudio/AWS/Sales/mssql-import/test/mock-rds.sql` non è aggiornata. Golden T-SQL nuovi (`single-user`, `multi-user`, `rename`, `backup-before-replace`) scritti a mano: `gen-golden.sh` non li produce.
- GUI: `ReplaceChoice.tsx` (radiogroup Stop/auto/swap/drop + checkbox del backup, avviso senza backup) in `PlanDialog` (Restore from S3) e nel passo Destination dell'import; `ReplaceOutcome.tsx` mostra il DB vecchio con "Drop <old>" o l'URI del backup. Etichette EN/IT per i passi nuovi.
- Esito strutturato `ReplaceOutcome {strategy, oldDatabase?, backupKey?}` su `PlanDone.replace` e `jobs.Item.replace` (`oldDatabase` solo dopo la rinomina riuscita, `backupKey` solo a backup finito); strategia risolta con nomi e chiave su `PlanPreview.replace` e `JobItemPlan.replace`. La GUI li usa (il parsing dei testi resta solo come ripiego per engine vecchi) e riconosce il restore temporaneo da `strategy == "swap"` + id `restore`. Id dei passi: `backup`, `wait-backup`, `drop`, `single-user`, `rename-old`, `rename-new`, `multi-user`, `multi-user-old` (+ `restore`, `wait`, `post`; nei job con prefisso `<db>/`); check `replace`, `sessions`, `names`, `destination`. Replace non si combina con `differential` né `noRecovery` (la GUI nasconde la scelta).
- Limite: nel progresso e nell'esito dell'import le etichette dei passi del replace sono i titoli inglesi dell'engine (l'anteprima è tradotta).

### Elenco dei bucket (branch `feature/bucket-picker`, 2026-10-16)

- Engine: `discover.Buckets` (`engine/internal/discover/buckets.go`) chiama `ListBuckets` con il filtro `BucketRegion` (solo i bucket della regione del target), paginato, ordinato per nome; `AccessDenied` diventa `discover.ErrBucketListDenied`. Tutti i fake di `discover.Service` devono implementare `Buckets`.
- API: `GET /api/v1/discovery/buckets?profile=&region=` → `{buckets:[{name, region?, createdAt?}]}`; 403 con testo fisso senza `s3:ListAllMyBuckets` (il web lo riconosce dallo status), 502 per gli altri errori AWS.
- CLI: `config init`/`add-target` propongono i bucket come scelta numerata + "other (type it)"; con `--bucket` l'elenco non viene chiesto; senza permesso si scrive il nome.
- Web: `BucketField.tsx` (combobox ARIA 1.2 editabile, nome sempre digitabile), usato da Add instance, l'unico punto dove il bucket si modifica; il ruolo del campo è `combobox`, non più `textbox`.
- IAM: statement facoltativo `OptionalListBuckets` (`s3:ListAllMyBuckets` su `*`, approvato da Fabio il 2026-10-16) in `docs/iam-policy.{json,md}`: elenca solo i nomi e non si può restringere a singoli bucket.

### Attività mostra tutto: GUI (branch `feature/activity-all-gui`, 2026-10-16)

- Il lato engine (journal condiviso, lock per job, operazioni come job, rifiuti registrati, job CLI) è descritto nella sezione dei job sopra.
- `jobs.ts`: `kind` import|export|operation, `operation`, `origin` app|cli, `refusal {status, reason, checks}` su `Job` e `JobSummary` (nell'elenco senza `checks`); helper `shownState` (refused), `matchesFilter`, `filterCounts`, `recordedRefusal`.
- Activity: titoli per operazione, badge `CLI`, stato "Rifiutato" distinto da "Fallito", `RefusalPanel` nel dettaglio (solo i check `failed`), chip All/Running/Failed/Refused come radio nativi (`.segmented`) con la scelta in `sessionStorage` (`dialga.activity.filter`), nota "anteprime non registrate". `CancelConfirm` avvisa che con scambio o drop il database può restare a metà.
- `PlanDialog`/`usePlanRun`: evento SSE `job` e `done.jobId` → "Open in Activity"; mentre gira "It keeps running: follow it in Activity"; "Cancel operation" con conferma in linea (focus su "Keep running", Escape non annulla) e poi `cancelJob`.
- `ErrorBanner recorded`: "See it in Activity" (`#activity`) per i *Run* rifiutati 412/422/502; per il 428 resta il banner della password.
- Il report di un database (`GET …/report`) è in sola lettura e non crea job.

### Accesso ai database (branch `feature/database-access`, 2026-10-19)

- Operazioni `grant`, `revoke`, `fix-orphan` in `engine/internal/ops/access.go` (T-SQL in `mssql/access_tsql.go` con golden `access-*.sql`; letture in `mssql/access.go`: `Logins`, `DatabaseAccess`, `MsdbUser`, `BackupOperators`, `UserSchemas`). Stessa forma di restore/drop: `Params` (`Login`, `User`, `Level`, `Backup`, `CreateLogin`, `Remove`) + `Validate` senza sessione, `Build` = pre-flight in sola lettura → `Prepared` con `PlanAction` (id `create-login`, `create-user`, `map-user`, `add-role`, `drop-role`, `drop-user`, `msdb-user`, `backup-grants`, `backup-revokes`, `msdb-drop-user`; `add-role`/`drop-role` si ripetono), anteprima, digest, conferma, job `operation`.
- Mai toccati: master user del target (`sql.user`), `sa`, `rdsa`, `##…##`, `dbo`/`guest`/`sys`/`INFORMATION_SCHEMA`; il login non si droppa mai. Nomi validati con `validate.PrincipalName` (pattern del contratto).
- `revoke --remove user` è distruttivo (conferma = nome del login; target protetto = nome del target) e fallisce se l'utente possiede schemi. Pulizia in msdb (`backup-revokes`, `msdb-drop-user`) solo se **nessun altro** database ha l'utente in `db_backupoperator`; se un database non è leggibile dal master user si resta conservativi (avviso, permessi lasciati).
- **Password del nuovo login** (`createLogin`): solo in `ExecuteRequest.loginPassword` / `ExecOptions.LoginPassword` / `pipeline.Env.LoginPassword` e nell'unica istruzione che gira. Piano, anteprima, digest, journal (`AccessParams.Statement` mascherato), eventi ed errori portano `mssql.MaskedPassword` (`****`; l'errore di SQL Server è ripulito). Policy `ops.CheckLoginPassword` (≥12 caratteri, 3 classi su 4) → 422 senza password o debole. Una ripresa del job non ha la password: `create-login` è no-op se il login esiste, altrimenti fallisce con un messaggio chiaro. Test che cercano la password in anteprima, JSON del piano, journal ed eventi: `ops/access_test.go`, `api/access_test.go`, `cmd/dialga/access_test.go`.
- Gli step sono idempotenti (`RunAccessStep` legge prima: login/utente già presenti, ruolo già dato o tolto = no-op). Nel job gli id ripetuti diventano `add-role#2`…; `pipeline.OperationEvents` toglie il suffisso (lo stream SSE ha gli id del piano). Step journal: kind `pipeline.access` (`AccessParams`, rivalidato alla ripresa).
- API: `GET /api/v1/targets/{name}/logins`, `GET …/databases/{db}/access` (viste, nessun job), piani `grant|revoke|fix-orphan` su `/plans/{operation}` e `/execute`. CLI: `dialga access list|logins|grant|revoke|fix-orphan` (dry run di default, `--apply`, `--confirm`, `-o json`); `--password-stdin` legge una riga, `--generate-password` fa 20 caratteri (crypto/rand) e li stampa **una volta**, su stderr (campo `generatedPassword` in JSON), solo dopo che il login esiste e mai in dry run.
- Test: `ops/access_integration_test.go` (SQL Server 2022 reale: grant → revoke → fix-orphan, login che si connette davvero); i permessi che il master user RDS ha e il mock no (`ALTER ANY LOGIN`, `ALTER ANY USER`/`VIEW DEFINITION` in msdb, `WITH GRANT OPTION` sui 3 oggetti, `rds_task_status`) stanno in `accessMock`. **Non provato su RDS reale**: che il master user veda i permessi di altri utenti in msdb e possa fare `GRANT` su quegli oggetti. I fake SQL degli altri pacchetti incorporano `ops/opstest.NoAccess`.
- GUI (`web/`): riga Database → **Accessi…** (`AccessDialog.tsx`, solo DB `ONLINE`): tabella degli utenti (`GET …/access`: login, livello, backup, ruoli), orfani evidenziati con **Fix orphan**, utenti non gestiti (`dbo`, master user…) in sola lettura, nota sui principal Windows. **Grant access** apre `PlanDialog` con `fields="grant"` (`GrantFields.tsx`: select dei login senza il master user, oppure *New login* con nome e password, **Generate** 20 caratteri con `crypto.getRandomValues`, mostra/copia, controllo di policy in `access.ts` identico a `ops.CheckLoginPassword`); **Revoke…** chiede prima cosa togliere (livello, backup, utente) in linea, poi `PlanDialog` (la rimozione dell'utente chiede di riscrivere il login). Dopo un'esecuzione il pannello rilegge gli accessi alla chiusura del dialogo.
- GUI, password del nuovo login: vive solo nello stato di `PlanDialog` (`secret`), mai in `PlanRequest`/anteprima/chiavi/log; `usePlanRun.execute(preview, confirm, loginPassword)` la mette nel solo body di `/execute` (e solo se `request.createLogin`); `close()` la azzera. A esecuzione riuscita resta un pulsante *Copy* finché il dialogo è aperto (Dialga non la conserva). Il 428 della password **SQL del target** (master) passa dal `PasswordProvider` come altrove: non c'entra con quella del nuovo login.
- GUI, step con id ripetuto (`add-role`): `upsertStep` accoppia id **e** label (running → finale dello stesso step), `stepKeys` dà chiavi React `id#n` in `StepList` e nelle azioni di `PlanPreviewView`. I check/azioni di accesso usano `ACCESS_CHECK` (`plancheck.*`) o il titolo dell'engine (non tradotto: nomina ruolo e utente).

## Contratto di layout atteso dalle pipeline

Ogni job **si salta da solo** finché la sua parte non esiste: `exit 0` in Bitbucket, job `layout`/`meta` su GitHub.

| Percorso | Requisito |
|---|---|
| `engine/go.mod` | Attiva i job dell'engine; la versione di Go si prende da qui |
| `engine/cmd/dialga/` | `package main` con `var version = "dev"`, sovrascritta con `-ldflags -X main.version=…` |
| Test di integrazione | Build tag `integration`, testcontainers (SQL Server 2022 + mock `rds_*` + MinIO). In Bitbucket girano senza Ryuk e con il servizio docker a 4 GB |
| `package.json` + `pnpm-workspace.yaml` (radice) | Workspace `web` + `desktop`; `"packageManager": "pnpm@<x.y.z>"`; `pnpm-lock.yaml` committato |
| `web/package.json` | Script `lint`, `typecheck`, `test`, `build`; output in `web/dist/` |
| `desktop/package.json` | `@tauri-apps/cli` e script `"tauri": "tauri"` |
| `desktop/src-tauri/` | `Cargo.lock` committato. `tauri.conf.json` con: `version` = tag; `build.frontendDist: "../../web/dist"`; `beforeBuildCommand` che builda `web/`; `bundle.externalBin: ["binaries/dialga"]` |
| `desktop/src-tauri/binaries/` | Generata dalla CI (in `.gitignore`) |

## Rilascio v1 (Linux)

1. Bump della versione con `ci/bump-version.sh X.Y.Z` (mai a mano), CHANGELOG aggiornato. Commit, PR, merge e tag **solo dopo conferma** (regole git globali). Procedura completa: `docs/release.md`.
2. Tag `vX.Y.Z`. La pipeline Bitbucket esegue check, integrazione, CLI per 6 piattaforme e pacchetti deb/rpm. Se un file di versione è diverso dal tag, si ferma subito (`bump-version.sh --check` nello step desktop).
3. Lo step **"Release · upload to Bitbucket Downloads"** è manuale (pulsante *Run*). Carica CLI, pacchetti e `SHA256SUMS`.
4. Per provare il packaging senza pubblicare: pipeline custom `release-dry-run`.

Pacchetti non firmati fino alla v2.0.

## Variabili e segreti

| Dove | Nome | Tipo | Note |
|---|---|---|---|
| Bitbucket | `BITBUCKET_DOWNLOADS_TOKEN` | **Secured** | Repository access token con "Repositories: Write". Arriva a `curl` da un file di header, mai sulla riga di comando. Vuoto = i file restano artifact della pipeline (14 giorni) |
| Bitbucket | `GITHUB_MIRROR_REPO`, `GITHUB_MIRROR_TOKEN` | Variabile / **secured** | Solo v2.0. PAT fine-grained con Contents + Workflows write. Vuote = mirror spento |
| GitHub (v2.0) | `ENABLE_LINUX_ARM64` | Variabile | Bundle Linux arm64 |

## Comandi locali

```bash
# engine
cd engine && gofmt -l . && go vet ./... && go test -race ./...
bash ci/build-engine.sh dist 0.0.1-dev /tmp/dialga-dist        # cross-build di prova

# pnpm non è installato: wrapper usa e getta (solo cache utente)
mkdir -p /tmp/pnpm-shim && printf '#!/bin/sh\nexec npx --yes pnpm@12.11.2 "$@"\n' >/tmp/pnpm-shim/pnpm && chmod +x /tmp/pnpm-shim/pnpm
export PATH=/tmp/pnpm-shim:$PATH && pnpm install

# engine: test di integrazione (Docker, testcontainers; in serie per non superare la memoria)
cd engine && TESTCONTAINERS_RYUK_DISABLED=true go test -tags integration -p 1 -count=1 ./...

# web: gli stessi script della CI (+ e2e Playwright; una volta: pnpm -C web exec playwright install --only-shell chromium)
pnpm -C web run lint && pnpm -C web run typecheck && pnpm -C web run test && pnpm -C web run build && pnpm -C web run e2e

# prerequisiti e config della macchina
dialga doctor

# app desktop: sidecar per l'host, poi build (deb/rpm in desktop/src-tauri/target/release/bundle/)
bash ci/build-engine.sh sidecar dev x86_64-unknown-linux-gnu
pnpm -C desktop tauri build --bundles deb,rpm

# L'app legge i target da `dialga config path`; la password SQL arriva dall'ambiente dell'engine
# (che la eredita dall'app) secondo sql.password del target, es. con "prompt":
#   read -rs DIALGA_SQL_PASSWORD && export DIALGA_SQL_PASSWORD && desktop/src-tauri/target/release/dialga-desktop

# UI nel browser: engine con DIALGA_DEV=1 (token di almeno 32 caratteri sulla prima riga di stdin,
# stdin aperto), poi Vite con URL e token dell'engine
head -c 48 /dev/urandom | base64 | tr -d '/+=\n' > token && echo >> token && chmod 600 token
( cat token; sleep infinity ) | DIALGA_DEV=1 dialga --config <config.yaml> serve   # stampa {"event":"ready","port":N,...}
VITE_ENGINE_URL=http://127.0.0.1:<N> VITE_ENGINE_TOKEN=$(tr -d '\n' < token) pnpm -C web dev   # http://localhost:1420
```

- Prerequisiti GUI su Fedora: `webkit2gtk4.1-devel`, `libxdo-devel`, `libappindicator-gtk3-devel`, `librsvg2-devel`, `openssl-devel` (presenti).
- `pnpm` non è installato: si usa `npx pnpm@<versione>` (solo cache utente).

## Da verificare al primo run delle pipeline

- ~~Cache con `key.files` su file non ancora esistenti~~ **Verificato il 2026-10-15:** se il file chiave manca, Bitbucket **fa fallire lo step** (`agent.cache.key-failure`), non salta la cache. Oggi `engine/go.sum`, `pnpm-lock.yaml` e `desktop/src-tauri/Cargo.lock` esistono; vanno sempre committati.
- ~~ENTRYPOINT dell'immagine `zricethezav/gitleaks`~~ **Verificato:** funziona in Bitbucket. I falsi positivi già nella storia vanno in `.gitleaksignore` (fingerprint `commit:file:regola:riga`); nei test non si scrivono token come stringhe letterali.
- Step desktop Linux: immagine `node:24-bookworm` (glibc 2.36: pacchetti per Debian 12+, Ubuntu 24.04+, Fedora 37+; Ubuntu 22.04 `TODO: confermare`) + rustup + dipendenze webkit. Pubblicare solo i pacchetti della pipeline (una build locale su Fedora 43 richiede glibc 2.39).
- Validazione locale già fatta: YAML, `actionlint` 0 errori, `build-engine.sh` provato su un modulo fittizio e sullo scheletro reale.

## Identità visiva

**Fonte: Decisyon Design System**, il design system predefinito dell'organizzazione: `https://claude.ai/artifact/Gqg3mSjAaRgJ7YAtFoxPSZ`.

| Aspetto | Valore |
|---|---|
| Cosa si carica | `tokens.css`, `components/bundle.css`, React 18, `components/bundle.js` (global `DecisyonDesignSystem_06d538`) |
| Colori | Primario `#5b8210` (scuro `#6f9c14`), brand `#88b116` |
| Tema scuro | `data-theme="dark"` |
| Font e icone | Geist / Geist Mono, Lucide |
| Layout | `sidebar-07`; procedure guidate come "dashboard + wizard a passi" |
| Regole UX | Skeleton al posto degli spinner, tre tipi di empty state |

**Prototipo** delle schermate (Istanze, wizard Importa, Attività) sul DS: `https://claude.ai/artifact/FatGDn7kAd6t9bgsS7yEya` (privato). È in italiano con il nome di lavoro: va allineato all'inglese e a "Dialga".

## Regole di lavoro

- Branch `feature/…`, `fix/…`, `chore/…`, mai lavoro diretto su `main`. Conventional Commits, semver con tag `vX.Y.Z` (fase `0.x`).
- Codice, commenti, commit e stringhe UI (chiavi i18n) in inglese; documentazione in italiano.
- Target di build: si cambiano **solo** in `ci/build-engine.sh`, più la matrice desktop di `release.yml` per la v2.
- Mai password, chiavi o endpoint reali nel codice, nei test o nelle fixture: si usano segnaposto (`[account-id]`) e il mock `rds_*`.
