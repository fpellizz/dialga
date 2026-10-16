# Changelog

Qui sono registrate tutte le modifiche rilevanti di Dialga. Il formato segue
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) e il progetto
usa il [Semantic Versioning](https://semver.org/) (`0.x`: la CLI,
l'API e la configurazione possono ancora cambiare tra versioni minor).

Rilascio: `docs/release.md`. Uso: `docs/guida-utente.md`.

## [Non rilasciato]

Prima linea di rilascio: configurazione, controlli in sola lettura,
tunnel SSM integrato, database dell'istanza, ispezione dei `.bak` e
monitoraggio dei task nativi, dalla CLI e dall'app desktop Linux.

### Aggiunto

- **CLI `dialga`**: `check [target]`, `status [target]` (`--db`,
  `--limit`), `wait <task-id>` (`--interval`; Ctrl-C ferma l'attesa, mai il
  task), `config path|validate|show`, `serve`, `version` e `completion`.
  Opzioni globali `--config` e `--output text|json`. Exit code: 0 OK,
  1 errore, 2 un controllo non superato, 3 task RDS finito in `ERROR` o
  `CANCELLED`.
- **Configurazione dei target** (`apiVersion: v1`) in
  `<cartella di configurazione utente>/dialga/config.yaml`: lettura stretta
  (le chiavi sconosciute sono errori), default, validazione e controllo
  dell'account su ogni comando. La password SQL è solo un riferimento
  (`prompt`, `env:VAR`), mai salvata. Esempio commentato:
  `docs/config.example.yaml`.
- **Controlli preliminari** con gli stessi verdetti di
  `mssql-import.sh check`: account AWS, istanza (`available`, option group
  `in-sync`), opzione `SQLSERVER_BACKUP_RESTORE`, bucket raggiungibile,
  storage (spazio libero da CloudWatch), tunnel, login SQL con il
  certificato TLS verificato sul bundle CA di Amazon RDS incorporato, task
  nativi. Lo stato `warning` segnala ciò che richiede attenzione senza far
  fallire il controllo.
- **Task nativi di backup e restore**: elenco (`status`) e attesa fino alla
  fine (`wait`), da `msdb.dbo.rds_fn_task_status`.
- **Tunnel SSM integrato** (`connect.mode: tunnel`): port forwarding verso
  l'endpoint RDS attraverso l'istanza bridge (tag `Name=ssm-bridge`, o
  `connect.bridge_tag`) nel VPC dell'istanza, con riconnessione automatica.
  Lo usano `check`, `status`, `wait`, `databases` e l'API; `dialga tunnel
  [target] [--port N]` ne tiene aperto uno in primo piano per i client SQL
  (stati in NDJSON con `-o json`). Richiede `session-manager-plugin` dopo
  la 1.2.497.0.
- **Database dell'istanza**: `dialga databases [target]` (alias `dbs`) con
  stato, dimensione di dati e log, compatibility level, recovery model e
  owner.
- **Ispezione dei `.bak`** (`dialga inspect <file.bak>... [-t target]
  [--image IMG]`): SQL Server usa e getta in podman o docker, file montati
  in sola lettura, `RESTORE LABELONLY`/`HEADERONLY`/`FILELISTONLY`, backup
  su più file (stripe) raggruppati. Con un target: verdetto sulla versione
  del motore, cifratura, TDE e FILESTREAM, con il rimedio `.bacpac`, e
  spazio dei file ripristinati rispetto al libero (avviso oltre l'80%).
  Exit code 0 ripristinabile, 2 non ripristinabile, 1 errore.
- **Piani di azioni** nell'engine: anteprima (dry-run) per default,
  conferma per i target protetti e le azioni distruttive, e il T-SQL delle
  procedure `rds_*` identico a `mssql-import.sh` (golden file). Li useranno
  i comandi di import ed export.
- **API locale** per l'app desktop (`dialga serve`): 127.0.0.1 su una porta
  casuale, token bearer di sessione ricevuto su stdin, controlli su `Host`
  e `Origin`, l'engine si ferma quando il suo stdin si chiude. Endpoint per
  stato, target, check (Server-Sent Events), task, database, panoramica
  dell'istanza e ispezione dei `.bak`, descritti da un contratto
  OpenAPI 3.1 servito su `GET /api/v1/openapi.yaml`.
- **App desktop** (Tauri v2, React, Decisyon Design System) con l'engine
  come sidecar: schermata Instances (una scheda per target con panoramica
  dell'istanza, check dal vivo e pannello dei task), schermata Databases
  (filtro e ordinamento) e Settings (lingua inglese o italiano, tema
  sistema, chiaro o scuro, percorso della configurazione, versione
  dell'engine).
- **Pacchetti Linux** (deb, rpm), non firmati: l'app, la CLI `dialga` in
  `/usr/bin`, una voce di menu, icone da 16 a 512 px, e in
  `/usr/share/doc/dialga/` la configurazione di esempio, la policy IAM
  minima e questo changelog.
- **Policy IAM minima** per gli utenti di Dialga: `docs/iam-policy.json`
  (spiegata in `docs/iam-policy.md`).
- **Strumenti di rilascio**: Bitbucket Pipelines (controlli sulle pull
  request; sui tag `v*` la CLI per sei piattaforme, i pacchetti Linux e un
  upload manuale nei Downloads del repository con `SHA256SUMS`),
  `ci/bump-version.sh` e `docs/release.md`.
- **Documentazione per l'utente**: `docs/guida-utente.md`.
