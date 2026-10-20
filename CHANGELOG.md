# Changelog

Qui sono registrate tutte le modifiche rilevanti di Dialga. Il formato segue
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) e il progetto
usa il [Semantic Versioning](https://semver.org/) (`0.x`: la CLI,
l'API e la configurazione possono ancora cambiare tra versioni minor).

Rilascio: `docs/release.md`. Uso: `docs/guida-utente.md`.

## [Non rilasciato]

## [0.6.4] - 2026-10-20

### Aggiunto

- Barra laterale **comprimibile**: un pulsante la riduce a una colonna di sole
  icone, e la scelta resta ricordata.
- Menu **Actions** su ogni riga della pagina Databases: *Report* resta
  visibile, Export, Finish restore, Access… e Drop (in fondo, separato) sono
  nel menu, utilizzabile anche da tastiera.
- Licenza **MIT** (`LICENSE`) e metadati AppStream: KDE Discover e GNOME
  Software mostrano nome, autore, licenza e icona dell'app installata.

### Modificato

- Le pagine con tabelle (Databases e le altre) usano tutta la larghezza della
  finestra; la tabella scorre in orizzontale se la finestra è troppo stretta,
  invece di tagliare le azioni.

## [0.6.3] - 2026-10-20

### Aggiunto

- Pagina **About** nel menu, subito sotto *Settings*: versione dell'engine,
  API locale, autore e repository (spostata dal fondo di *Settings*).

### Corretto

- L'icona della finestra su Wayland (KDE) era quella generica: la finestra si
  presenta ora come `Dialga`, lo stesso nome della voce di menu, e prende la
  sua icona.

## [0.6.2] - 2026-10-20

### Corretto

- Un'operazione avviata con *Run* (export, restore, drop…) non viene più
  rifiutata con `context canceled` se la finestra si chiude, o l'app si
  ricarica, mentre girano i controlli preliminari: i controlli e il job
  vanno avanti comunque.
- L'icona nella barra delle applicazioni di KDE Plasma restava quella di
  Decisyon dopo l'aggiornamento (cache delle icone per nome): la voce di menu
  usa ora l'icona `dialga`, installata in tutte le misure.

## [0.6.1] - 2026-10-19

### Aggiunto

- `README.md` in inglese: funzioni, architettura, prerequisiti, ruolo IAM
  dell'opzione `SQLSERVER_BACKUP_RESTORE`, policy IAM per gli utenti, bridge
  SSM, installazione, configurazione, uso, sviluppo e rilascio.

### Modificato

- Nuova icona dell'app (finestra, voce di menu, icone dei pacchetti deb e rpm)
  e logo nella barra laterale: Dialga al posto del simbolo Decisyon.

## [0.6.0] - 2026-10-19

### Aggiunto

- **Accesso ai database**. Dare o togliere a un login SQL l'accesso a un
  database (lettura, lettura e scrittura, proprietario), con il permesso di
  fare backup (`--backup`); creare il login con password generata o letta da
  stdin (`CHECK_POLICY ON`, mostrata una sola volta); revocare livello, backup
  o utente; legare un utente orfano al suo login (`fix-orphan`, o `grant`
  con un login dello stesso nome, anche creato al momento). Un `db_owner` con i
  permessi in msdb conta come abilitato al backup. Collaudato su `mssql-dev`
  con un login usa e getta: grant con backup, backup lanciato dal login, revoke. CLI
  `dialga access list|logins|grant|revoke|fix-orphan` (dry run di default,
  `--apply`); API `GET …/logins`, `GET …/databases/{db}/access` e piani
  `grant|revoke|fix-orphan`; app: *Access…* nella riga del database. Il master
  user del target, `sa`, `rdsa`, i `##…##`, `dbo` e `guest` non vengono mai
  toccati. Guida in `docs/guida-utente.md` (§5.15, §6.4, §7.2).

### Modificato

- La rinomina di un restore con sostituzione (`rdsadmin.dbo.rds_modify_db_name`)
  è verificata su `mssql-dev` (2026-10-19).

## [0.5.0] - 2026-10-19

### Aggiunto

- **Activity completa**. Ogni operazione è un job nel journal e compare in
  Activity: le singole operazioni della pagina Databases (restore da S3,
  export, drop, finish, cancel) ora girano come job di tipo
  `operation` (passi del piano, avanzamento del task nativo, annullabili,
  riprendibili; continuano se il client chiude lo stream); i tentativi
  rifiutati al *Run* (412, 422, 428, 502) lasciano un job `failed` con
  `refusal` (stato, motivo, controlli falliti); i job della CLI (`import`,
  `run`, `restore`, `export`, `drop`, `finish`, `cancel` con `--apply`) entrano
  nello stesso journal con origine `cli`, anche con l'app aperta. Nuovi campi
  di `Job`/`JobSummary`: `kind` (`import`|`export`|`operation`), `operation`,
  `origin` (`app`|`cli`), `refusal`; `POST .../plans/{op}/execute` apre con
  l'evento SSE `job` (`{"id"}`) e `done` ha `jobId`. Il lock del journal è
  ora per job (`.<id>.lock`) e non più uno solo per la cartella: l'app non
  fallisce né perde job se una run CLI è in corso, `dialga jobs resume`
  rifiuta solo se un altro processo ha quel job; annullare da Activity un job
  di un altro processo risponde 409.
- **Restore su un database esistente** (`replace`). RDS rifiuta un restore
  nativo sopra un database che esiste: ora `dialga restore --replace
  auto|swap|drop` (e `dialga import`, il piano YAML con `replace:` e
  `backupFirst:`, `POST .../plans/restore` e gli item di `POST /api/v1/jobs`)
  lo fanno esplicitamente. `swap` ripristina in `<db>_dialga_new_<stamp>` e
  scambia i nomi (il vecchio resta come `<db>_old_<stamp>`, con compensazione
  se la seconda rinomina fallisce); `drop` salva prima il database su S3
  (`<export>/<db>_before_replace_<stamp>.bak`, `--no-backup-first` per
  saltarlo), lo elimina e ripristina; `auto` sceglie `swap` se due copie
  stanno nello spazio libero, altrimenti `drop` con il backup, e dice
  perché nel controllo `replace`. Conferma riscrivendo il nome del database,
  controlli su sessioni attive e nomi liberi, T-SQL esatto in anteprima, e
  ripresa sicura dei job interrotti. Le rinomine usano
  `rdsadmin.dbo.rds_modify_db_name` (TODO: confermare su mssql-dev).
- **Elenco dei bucket**. *Add instance* mostra il bucket come campo con
  elenco (combobox accessibile) riempito con i bucket dell'account nella
  regione scelta, con filtro mentre si scrive; un nome digitato resta sempre
  valido. `dialga config init` propone i bucket come scelta numerata più
  «other (type it)» (`--bucket` ha la precedenza). Senza `s3:ListAllMyBuckets`
  l'elenco non c'è e si digita il nome. API: `GET /api/v1/discovery/buckets`
  (403 con testo fisso senza il permesso, 502 per gli altri errori). Policy
  IAM: nuovo statement facoltativo `OptionalListBuckets`.

## [0.4.0] - 2026-10-16

### Aggiunto

- **App desktop: la password SQL si inserisce dall'app**. Dove serve e manca
  compare *Enter password*: il dialogo verifica il login prima di salvarla,
  la tiene in memoria per la sessione e, se richiesto, la ricorda nel
  portachiavi di sistema (*Remember in the system keyring*). Le viste si
  ricaricano da sole; le azioni già avviate vanno ripetute. La scheda
  dell'istanza mostra da dove arriva la password, con *Forget password*.
- **Password SQL: quattro fonti**. `keyring` (portachiavi di sistema, servizio
  `dialga`, account = nome del target) e `secretsmanager:<ARN>` (JSON con
  `password`, o testo semplice; profilo, regione e controllo dell'account del
  target) ora funzionano; `prompt` legge anche la voce del portachiavi se la
  password è stata ricordata. Un solo resolver (`session.Passwords`) per CLI,
  API e job: la password non finisce mai in file, journal, parametri dei
  passi, eventi, log o errori.
- **`dialga password set|forget|status`**: memorizza (senza eco, o una riga da
  stdin), rimuove, o mostra fonte e voce del portachiavi di un target.
- **API**: `TargetSummary.password` (`source`, `available`, `canRemember`);
  `PUT /api/v1/targets/{name}/password` (`password`, `remember`, `?verify=1`)
  e `DELETE` dello stesso percorso; **428** con l'header
  `Dialga-Password-Target` dove la password serve e non si risolve (la
  panoramica resta 200 con SQL `skipped`).
- Policy IAM: lo statement opzionale per `secretsmanager:GetSecretValue` non è
  più "previsto"; nota su `kms:Decrypt` per le chiavi gestite dal cliente.
- Dipendenze: `github.com/zalando/go-keyring` v0.2.8 e
  `service/secretsmanager` v1.50.1.

### Modificato

- La password mancante di un target `prompt` o `keyring` risponde **428**
  invece di 422 (le fonti `env:` e `secretsmanager:` che non danno nulla
  restano 422).
- CORS: `PUT` consentito, `Dialga-Password-Target` esposto.
- Il suggerimento sotto gli errori di sessione SQL segue la modalità di
  connessione del target (prima citava sempre il tunnel già aperto).

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
