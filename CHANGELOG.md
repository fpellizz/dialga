# Changelog

Qui sono registrate tutte le modifiche rilevanti di Dialga. Il formato segue
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) e il progetto
usa il [Semantic Versioning](https://semver.org/) (`0.x`: la CLI,
l'API e la configurazione possono ancora cambiare tra versioni minor).

Rilascio: `docs/release.md`. Uso: `docs/guida-utente.md`.

## [Non rilasciato]

## [0.3.0] - 2026-10-16

### Aggiunto

- **`dialga import <file.bak>...`**: importa più database come un solo job
  (upload, restore, attesa del task, report `post`), in parallelo nei limiti
  di `max_tasks`. Opzioni `--db`, `--key`, `--prefix`, `--differential`,
  `--norecovery`, `--overwrite`, `--skip-failing`, `--interval`. Anteprima
  per default con i controlli di tutti i database, una sola conferma per
  job, Ctrl-C annulla il job (upload abortiti, task nativi già avviati
  continuano su RDS). Exit code 0, 2 (controllo fallito, nulla parte), 3
  (task in `ERROR` o `CANCELLED`), 1.
- **`dialga run <piano.yaml>`**: import o export di più database descritti
  da un piano YAML (`kind: Import` o `Export`, lettura stretta, backup
  striped con `files`, `destination` per scaricare l'export). Formato e
  controlli in `docs/piani-job.md`.
- **`dialga jobs`** (elenco), **`jobs show <id>`** e **`jobs resume <id>`**:
  leggono il journal dei job dell'app; la ripresa vale solo per i job
  interrotti, continua gli upload dalle parti già in S3, segue i task già
  in coda, chiede il nome del target se protetto (`--confirm`) ed è
  rifiutata finché l'app desktop tiene il journal.
- **App desktop**: wizard **Import** (File, Verifica con `inspect`,
  Destinazione, Anteprima, Avanzamento, Esito) e wizard **Export**
  (Database, Opzioni, Finestre, Anteprima, Avanzamento, Esito) con le
  finestre dei backup automatici e di manutenzione e una stima della durata;
  *Export selected* in Databases; pagina **Activity** con elenco, dettaglio
  ed eventi dal vivo, *Cancel job* e *Resume* sui job interrotti, e il
  numero dei job in corso nella barra laterale.
- **Job**: un solo tunnel SSM per target per job (nella CLI è del job,
  nell'app è il tunnel condiviso del target).

### Modificato

- **`dialga config init` e `config add-target`** usano come modalità di
  connessione predefinita `tunnel` (tunnel SSM integrato), come *Add
  instance* dell'app; prima era `external`. Un target già nel file senza
  `connect.mode` resta `external`.
- Le voci **Import**, **Export** e **Activity** della barra laterale non
  sono più disabilitate.

### Sicurezza

- La ripresa di un job viene rifiutata se il target non punta più alla
  stessa istanza (account, regione e nome) o allo stesso bucket del job
  registrato. I passaggi ricostruiti rivalidano chiavi S3, nomi dei
  database e percorsi, e il T-SQL di restore ed export viene rigenerato dai
  parametri e confrontato con quello registrato. Un job scritto da una
  build precedente a questo controllo non si può riprendere.
- Un download senza `--overwrite` non sostituisce mai un file comparso
  nella destinazione durante il trasferimento.
- `dialga run` legge piani di al massimo 1 MiB; il prefisso S3 di un job
  viene validato prima delle chiavi; `POST /api/v1/jobs` accetta corpi
  fino a 256 KiB (gli altri endpoint restano a 64 KiB).

## [0.2.0] - 2026-10-16

Prima linea di rilascio: configurazione guidata, controlli, tunnel SSM
condiviso, database dell'istanza, ispezione dei `.bak`, trasferimenti su S3
e operazioni native di restore ed export, dalla CLI e dall'app desktop
Linux. I wizard Importa ed Esporta, la schermata Activity e la ripresa dei
job arrivano nella prossima versione.

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
- **Trasferimenti su S3** (`dialga upload`, `dialga download`): upload
  multipart con parti dimensionate sul file, ritentativo di ogni parte,
  checksum CRC64NVME verificato da S3 e rilettura finale dell'oggetto;
  download per intervalli in un file temporaneo, con controllo dello spazio
  libero e del checksum. Un oggetto o un file esistente non viene sostituito
  senza `--overwrite`; i file che non possono partire sono elencati con il
  motivo e gli altri passano (exit 2). Ctrl-C non lascia nulla (l'upload
  multipart viene abortito). Il motore sa anche mettere in pausa un upload e
  completarlo (`ListParts` fa fede), per la futura ripresa dei job.
- **Operazioni native** (`dialga restore`, `export`, `finish`, `cancel`,
  `drop`): anteprima per default con controlli preliminari e T-SQL esatto,
  `--apply` per eseguire, conferma da riscrivere (nome del target se
  protetto, nome del database per `drop`, `--confirm` senza terminale),
  impronta del piano, `--norecovery` e `--differential`, `--no-wait`, attesa
  del task nativo. Ctrl-C ferma l'attesa, mai il task. Exit code 2 se un
  controllo preliminare fallisce (nulla parte), 3 se il task finisce in
  `ERROR` o `CANCELLED`.
- **Report dopo il restore** (`dialga post`): proprietà, file, accesso del
  login, utenti SQL orfani con i comandi per ricrearne il login, utenti
  Windows, oggetti per tipo.
- **Primo avvio**: `dialga config init` (guidato: profilo, regione, istanza
  trovata nell'account, bucket verificato con `HeadBucket`, login, origine
  della password, modalità), `config add-target` e `config remove-target`;
  scritture atomiche con permessi `0600`, versione precedente in `.bak`,
  mai una password nel file. `dialga doctor` controlla plugin SSM, runtime
  container, profili AWS e configurazione, senza chiamate AWS, e indica il
  comando di installazione per la distribuzione.
- **Tunnel condiviso**: un solo tunnel SSM per target, usato dall'app per
  panoramica, check, database e operazioni; aperto dall'uso si chiude dopo
  10 minuti senza utilizzatori, aperto con *Connect* resta aperto fino a
  *Disconnect*. Un comando singolo della CLI usa sempre una porta libera;
  `connect.local_port` vale per `dialga tunnel` e per il tunnel dell'app.
  L'app ferma l'engine con garbo all'uscita, così i tunnel chiudono le
  sessioni SSM.
- **App desktop**: azioni sui database (*Export*, *Report*, *Finish
  restore*, *Drop*) e *Restore from S3*; finestra del piano con anteprima,
  T-SQL esatto, conferma e avanzamento del task dal vivo; *Cancel* sui task
  in corso; *Add instance* con ricerca delle istanze; schede
  *Prerequisites* e *Targets* in Settings; indicatore del tunnel sulla
  scheda e riepilogo nella barra laterale.
- **Job e API**: motore dei job multi-database con scheduler, eventi e
  journal; API per elenco, dettaglio, flusso di eventi e annullamento dei
  job, per anteprima ed esecuzione dei piani e per i trasferimenti. Non
  hanno ancora un'interfaccia: la schermata Activity arriva dopo.
- **Accessibilità**: revisione WCAG 2.2 AA dell'app (finestre, schede di
  Settings, focus, annunci per gli screen reader) e suggerimenti su cosa
  fare negli errori.

### Corretto

- **`SIGPIPE` e `SIGTERM` fermano un comando come Ctrl-C** (come `SIGHUP`):
  la pulizia (tunnel, sessione SSM, upload multipart) avviene anche con una
  pipe chiusa (`| head`) o con un `kill`.
- **Avviso sullo spazio libero nel restore**: se il backup occupa più dell'80%
  dello spazio libero dell'istanza (o più di tutto) il restore avverte che
  il database ripristinato occupa la somma dei suoi file, non la dimensione
  del `.bak`.

### Limiti noti

- I trasferimenti sono stati provati solo su MinIO, mai su S3 reale.
- Il limite di database per classe di istanza non viene controllato.
- I pacchetti non sono firmati: il checksum protegge solo da un download
  danneggiato.
- Upload in pausa e ripresa: nessun comando o pulsante nella v0.2.0.
