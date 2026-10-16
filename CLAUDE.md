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

**Stato al 2026-10-15:**

| Branch | Contenuto | Commit |
|---|---|---|
| `feature/bootstrap` | CI, scheletro dell'engine, asset del brand | Unito in `main` (PR #1) |
| `feature/spike-tauri-sidecar` | Spike dell'architettura: engine `serve` + API + check, frontend `web/`, shell `desktop/` | Unito in `main` (PR #2, fix gitleaks PR #3) |
| `feature/v0.1-foundations` | v0.1, prima parte: configurazione dei target, CLI `check`/`config` (testo/JSON), controllo dell'account, connessione `external`/`direct`. Provato dal vivo su `mssql-dev` | Unito in `main` (PR #4) |
| `feature/v0.1-foundations` | v0.1, seconda parte: API dei target (`GET /api/v1/targets`, check per target), GUI con sidebar e schermata Instances, EN/IT. Provata dal vivo nel browser (Vite + engine) | PR #5 |
| `feature/v0.1-foundations` | v0.1, terza parte: task nativi RDS. CLI `status`/`wait` (exit 3 se il task finisce in ERROR/CANCELLED), API `GET /api/v1/targets/{name}/tasks`, pannello Tasks nella scheda dell'istanza (si aggiorna ogni 30 s finché un task è in corso). Pacchetti condivisi `awsx`/`mssql`/`session`. Provata dal vivo su `mssql-dev` (sola lettura) | In corso |

Lo **spike** è validato dal vivo su `mssql-dev`:
- l'engine fa da sidecar con token su stdin;
- l'API su 127.0.0.1 respinge le richieste senza token (401), da origini estranee (403) e con host falsi (403);
- il check in streaming (SSE) passa 5/5 in circa 4 s;
- il **login SQL avviene con il certificato RDS verificato** attraverso il tunnel. Le prove contrarie falliscono come devono: hostname sbagliato e radici di sistema;
- la UI nel browser (Vite dev + engine con `DIALGA_DEV=1`) mostra il flusso completo.

## Decisioni prese

| Tema | Decisione (2026-10-15) |
|---|---|
| Stack | Engine Go + API locale (HTTP + SSE), shell Tauri v2 con l'engine come sidecar, frontend React 18.3 + TS |
| UI | Decisyon Design System (`DecisyonDesignSystem_06d538`): shadcn stile Nova su **Base UI** (non Radix), Geist, Lucide, primario `#5b8210` |
| Lingua | **Inglese di default**, italiano selezionabile dalla sidebar (scelta in `localStorage` `dialga.lang`). Dizionari tipizzati in `web/src/i18n.tsx`, senza librerie. Copy secondo le regole del DS: sentence case, imperativi, tono neutro |
| Piattaforme v1 | **Linux**: pacchetti deb e rpm; CLI cross-compilata per tutti gli OS. Installer Windows/macOS → v2.0 |
| React | **18.3, non 19**: il bundle del Decisyon Design System si aggancia a React 18 come globale |
| Password SQL | Mai nelle richieste API né nelle risposte. La config di ogni target dice da dove arriva (`sql.password`): `env:VAR`, oppure `prompt` = `DIALGA_SQL_PASSWORD` dall'ambiente (la CLI su terminale la chiede). Portachiavi dell'OS previsto (`keyring`) |
| Firma dei pacchetti | v2.0 |
| GitHub | Mirror e workflow pronti ma **spenti** fino alla v2.0 |

## Contenuto

| Percorso | Cosa fa |
|---|---|
| `bitbucket-pipelines.yml` | Check su ogni push; integrazione su PR, `main` e tag. Su `main` e tag: cross-build della CLI. Sui tag anche pacchetti Linux deb/rpm e upload **manuale** nei Downloads. Mirror su GitHub (v2.0, spento) |
| `.github/workflows/ci.yml` | Check su GitHub: engine su Linux, Windows e macOS, lint, integrazione, web, desktop, gitleaks. Gira solo col mirror attivo |
| `.github/workflows/release.yml` | Percorso di rilascio **v2.0**: bundle per i tre OS → release GitHub in bozza |
| `ci/build-engine.sh` | Cross-build della CLI (`dist`) o del sidecar Tauri (`sidecar`), condiviso dalle due CI |
| `ci/tauri-placeholders.sh` | File finti richiesti da `tauri-build` nei job di check (mai nei rilasci) |
| `engine/` | Modulo `github.com/decisyon/dialga/engine` (Go 1.26), dettaglio sotto |
| `engine/cmd/dialga` | CLI (cobra): `check [target]`, `status [target]` (`--db`, `--limit`: task nativi, dal più recente), `wait <task-id>` (`-t target`, `--interval`, default 30s: segue il task fino alla fine; Ctrl-C ferma l'attesa, non il task), `config path\|validate\|show`, `serve` (API locale per la shell), `version`. Opzioni globali `--config`, `--output text\|json`. Exit code: 0 OK, 1 errore, 2 controllo non superato, 3 task RDS in `ERROR`/`CANCELLED` |
| `engine/internal/config` | Configurazione dei target (`apiVersion: v1`), lettura stretta (chiavi sconosciute = errore), default e validazione. Percorso `os.UserConfigDir()/dialga/config.yaml`. La password è solo un riferimento: `prompt`, `env:VAR`; `keyring` e `secretsmanager:<ARN>` previsti, non ancora attivi |
| `docs/config.example.yaml` | Esempio commentato, con soli segnaposto |
| `engine/internal/api` | HTTP su 127.0.0.1, porta casuale, token Bearer a tempo costante, controllo di Host e Origin (CORS). La config si rilegge a ogni richiesta. `GET /api/v1/health`; `GET /api/v1/targets` (elenco senza riferimenti alla password; file assente = elenco vuoto, file non valido = campo `error`); `POST /api/v1/targets/{name}/checks` (SSE `notice`/`step`/`done`; 409 senza config, 404 target sconosciuto, 422 modalità `tunnel`); `GET /api/v1/targets/{name}/tasks?db=&limit=` (task nativi; 400 parametri non validi, 422 password non disponibile o `tunnel`, 502 se AWS o SQL non rispondono; ogni chiamata apre la sua sessione SQL) |
| `engine/internal/check` | I passi del check (credenziali, istanza, connessione, login SQL con TLS verificato, DB e task) sopra `awsx` e `mssql`. `target.go`: da target configurato a richiesta di check (`RequestFor`) |
| `engine/internal/awsx` | Credenziali AWS con controllo dell'account (`Load`, `*WrongAccountError`) e lettura dell'istanza RDS (`DescribeInstance`) |
| `engine/internal/mssql` | Connessione con certificato verificato sul bundle RDS (`Open`, `Addr`); task nativi da `msdb.dbo.rds_fn_task_status` (`Tasks.List`/`Get`, interfaccia `TaskReader`, `Wait` con tentativi sugli errori temporanei). Orari = ora del server senza fuso (`2006-01-02T15:04:05`) |
| `engine/internal/session` | Sessione SQL su un target configurato (AWS + account → endpoint → TDS + ping) e password dall'ambiente (`PasswordFromEnv`, `ErrPromptNeeded`) |
| `engine/internal/rdsca` | Bundle CA RDS incorporato (`global-bundle.pem`) |
| `engine/internal/validate` | `DBName`, `S3Key`: le regole di `valid_db`/`valid_key` di `mssql-import.sh` |
| `package.json`, `pnpm-workspace.yaml` | Workspace pnpm (`pnpm@12.11.2`) con `web` e `desktop`. pnpm 12 applica un'età minima di rilascio: le eccezioni per Tauri 2.12.x le ha aggiunte pnpm stesso (`minimumReleaseAgeExclude`) |
| `web/` | React 18 + TS 7 + Vite 8 + Biome + Vitest. `src/engine.ts` (URL e token dalla shell, o `VITE_ENGINE_*` in dev), `src/api.ts` + `src/sse.ts` (fetch e SSE con header Bearer: `listTargets`, `checkTarget`), `src/i18n.tsx` (EN/IT), `src/App.tsx` (layout sidebar + pagina), `src/Sidebar.tsx` (navigazione: Instances attiva, Import/Export/Activity "Soon"; lingua; stato dell'engine), `src/InstancesPage.tsx` (schede dei target, skeleton, empty state con il percorso della config, errori della config, check con `src/StepList.tsx`, task con `src/TasksPanel.tsx`), `src/styles/ds-tokens.css` (copia dei token del DS, da non modificare) + `src/styles/app.css` |
| `desktop/` | Shell Tauri v2. `src-tauri/src/main.rs`: avvia il sidecar, passa il token su stdin, comando `engine_info`, chiude l'engine all'uscita. CSP `connect-src` limitato a `127.0.0.1`; al frontend solo `core:default`. Bundle deb/rpm |
| `docs/brand/` | `decisyon-symbol.svg` (dal DS), `decisyon-mark-1024.png` (sorgente per `tauri icon`), `decisyon-logo.jpg` (dal sito, solo riferimento) |

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

1. Bump della versione in `desktop/src-tauri/tauri.conf.json`, e allineata in `package.json` / `Cargo.toml`. Commit, PR, merge e tag **solo dopo conferma** (regole git globali).
2. Tag `vX.Y.Z`. La pipeline Bitbucket esegue check, integrazione, CLI per 6 piattaforme e pacchetti deb/rpm. Se la versione di `tauri.conf.json` è diversa dal tag, si ferma.
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

# web: gli stessi script della CI
pnpm -C web run lint && pnpm -C web run typecheck && pnpm -C web run test && pnpm -C web run build

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
- Step desktop Linux: immagine `node:24` (Debian) + rustup + dipendenze webkit; verificare tempi e cache con il primo `desktop/`.
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
