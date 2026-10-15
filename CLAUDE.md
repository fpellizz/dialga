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
| `feature/bootstrap` | CI, scheletro dell'engine, asset del brand | `7efcd62`, non pushato |
| `feature/spike-tauri-sidecar` | Spike dell'architettura: engine `serve` + API + check, frontend `web/`, shell `desktop/` | Non committato |

Lo **spike** è validato dal vivo su `mssql-dev`:
- l'engine fa da sidecar con token su stdin;
- l'API su 127.0.0.1 respinge le richieste senza token (401), da origini estranee (403) e con host falsi (403);
- il check in streaming (SSE) passa 5/5 in circa 4 s;
- il **login SQL avviene con il certificato RDS verificato** attraverso il tunnel. Le prove contrarie falliscono come devono: hostname sbagliato e radici di sistema;
- la UI nel browser (Vite dev + engine con `DIALGA_DEV=1`) mostra il flusso completo.

## Decisioni prese

| Tema | Decisione (2026-10-15) |
|---|---|
| Stack | Engine Go + API locale (HTTP + SSE), shell Tauri v2 con l'engine come sidecar, frontend React 19 + TS |
| UI | Decisyon Design System (`DecisyonDesignSystem_06d538`): shadcn stile Nova su **Base UI** (non Radix), Geist, Lucide, primario `#5b8210` |
| Lingua | **Inglese di default**, italiano selezionabile (i18next). Copy secondo le regole del DS: sentence case, imperativi, tono neutro |
| Piattaforme v1 | **Linux**: pacchetti deb e rpm; CLI cross-compilata per tutti gli OS. Installer Windows/macOS → v2.0 |
| React | **18.3, non 19**: il bundle del Decisyon Design System si aggancia a React 18 come globale |
| Password SQL | Per ora solo dall'ambiente dell'engine (`DIALGA_SQL_PASSWORD`, fallback `SQLCMDPASSWORD`), mai nelle richieste API. Portachiavi dell'OS dalla v0.1 |
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
| `engine/cmd/dialga` | `serve` (API locale per la shell), `version`, `help` |
| `engine/internal/api` | HTTP su 127.0.0.1, porta casuale, token Bearer a tempo costante, controllo di Host e Origin (CORS). `GET /api/v1/health`, `POST /api/v1/checks` (SSE) |
| `engine/internal/check` | STS, RDS, tunnel, login SQL con TLS verificato, DB e task |
| `engine/internal/rdsca` | Bundle CA RDS incorporato (`global-bundle.pem`) |
| `engine/internal/validate` | `DBName`, `S3Key`: le regole di `valid_db`/`valid_key` di `mssql-import.sh` |
| `package.json`, `pnpm-workspace.yaml` | Workspace pnpm (`pnpm@12.11.2`) con `web` e `desktop`. pnpm 12 applica un'età minima di rilascio: le eccezioni per Tauri 2.12.x le ha aggiunte pnpm stesso (`minimumReleaseAgeExclude`) |
| `web/` | React 18 + TS 7 + Vite 8 + Biome + Vitest. `src/engine.ts` (URL e token dalla shell, o `VITE_ENGINE_*` in dev), `src/api.ts` + `src/sse.ts` (fetch e SSE con header Bearer), `src/App.tsx` (schermata "Check an instance"), `src/styles/ds-tokens.css` (copia dei token del DS) |
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

# Il login SQL del check legge la password dall'ambiente dell'engine (che la eredita dall'app):
#   read -rs DIALGA_SQL_PASSWORD && export DIALGA_SQL_PASSWORD && desktop/src-tauri/target/release/dialga-desktop
# UI nel browser: engine con DIALGA_DEV=1, poi VITE_ENGINE_URL/VITE_ENGINE_TOKEN per `pnpm -C web dev`
```

- Prerequisiti GUI su Fedora: `webkit2gtk4.1-devel`, `libxdo-devel`, `libappindicator-gtk3-devel`, `librsvg2-devel`, `openssl-devel` (presenti).
- `pnpm` non è installato: si usa `npx pnpm@<versione>` (solo cache utente).

## Da verificare al primo run delle pipeline

- Cache con `key.files` su file non ancora esistenti (attesa: cache saltata, non errore).
- ENTRYPOINT dell'immagine `zricethezav/gitleaks` in Bitbucket.
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
