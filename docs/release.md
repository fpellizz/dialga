# Rilascio di Dialga (v1, Linux)

Un rilascio è un tag `vX.Y.Z` su `main`. La pipeline Bitbucket del tag
compila la CLI per sei piattaforme e i pacchetti Linux; uno step **manuale**
li carica, con `SHA256SUMS`, nei Downloads del repository. I pacchetti sono
**non firmati** fino alla v2.0.

| Output | Nomi dei file |
|---|---|
| App desktop + CLI, Debian/Ubuntu | `Dialga_X.Y.Z_amd64.deb` |
| App desktop + CLI, Fedora e altre distribuzioni rpm | `Dialga-X.Y.Z-1.x86_64.rpm` |
| Solo CLI | `dialga_X.Y.Z_{linux,darwin}_{amd64,arm64}.tar.gz`, `dialga_X.Y.Z_windows_{amd64,arm64}.zip` |
| Checksum | `SHA256SUMS` (tutti i file sopra) |

Le versioni sono semplici `MAJOR.MINOR.PATCH`: un suffisso di pre-release
come `-rc.1` non è una versione rpm valida, e in dpkg viene ordinato dopo il
rilascio. Nella `0.x` una MINOR può rompere la CLI, l'API o la
configurazione.

## 1. Preparare il commit di rilascio

Su un branch (`chore/release-vX.Y.Z`), partendo da un `main` aggiornato:

1. `CHANGELOG.md`: rinomina `## [Non rilasciato]` in
   `## [X.Y.Z] - AAAA-MM-GG` e aggiungi sopra una nuova `## [Non rilasciato]`
   vuota.
2. Imposta la versione ovunque sia dichiarata e verificala:

   ```bash
   bash ci/bump-version.sh X.Y.Z          # tauri.conf.json, Cargo.toml, Cargo.lock, package.json
   bash ci/bump-version.sh --check X.Y.Z  # what the pipeline compares with the tag
   ```

   Lo script non cambia nulla se anche un solo file non si può aggiornare, e
   fallisce se un manifest con una versione non è nel suo elenco.
3. Facoltativo, consigliato quando cambia il packaging: compila i pacchetti
   in locale (§7).
4. Commit `chore(release): vX.Y.Z`, poi pull request e merge.

## 2. Tag

Sul commit di merge in `main`:

```bash
git switch main && git pull --ff-only
bash ci/bump-version.sh --check X.Y.Z
git tag -a vX.Y.Z -m "Dialga vX.Y.Z"
git push origin vX.Y.Z
```

## 3. Cosa fa la pipeline del tag

| Ordine | Step | Risultato |
|---|---|---|
| 1 (in parallelo) | Test e golangci-lint dell'engine; lint, typecheck, test e build del web; fmt, clippy e test del desktop; gitleaks | Gli stessi controlli di una pull request, tutti |
| 2 | Test di integrazione dell'engine | SQL Server 2022 + mock `rds_*` + MinIO |
| 3 | Cross-build dell'engine | Sei archivi della CLI e il sidecar Linux con la versione del tag |
| 4 | Bundle desktop Linux | Si ferma se `tauri.conf.json` non ha la versione del tag; crea il deb e l'rpm |
| 5 | **Release: upload to Bitbucket Downloads** | **Manuale** (pulsante *Run*) |
| 6 | Mirror su GitHub | Spento fino alla v2.0 |

Se uno step fallisce, la pipeline si ferma prima dell'upload: non viene
pubblicato nulla.

## 4. Upload nei Downloads (manuale)

1. Bitbucket, *Pipelines*, pipeline del tag: controlla gli artifact degli
   step 3 e 4.
2. Premi *Run* su "Release · upload to Bitbucket Downloads". Lo step copia
   archivi e pacchetti, scrive `SHA256SUMS` e carica tutto.
   Gli artifact della pipeline durano 14 giorni: eseguilo entro quel
   termine, altrimenti riesegui la pipeline del tag.
3. Senza `BITBUCKET_DOWNLOADS_TOKEN` lo step non carica nulla. In quel caso
   scarica gli artifact degli step 3 e 4 e caricali a mano
   (*Repository*, *Downloads*, *Add files*), insieme a:

   ```bash
   rm -f SHA256SUMS.engine && sha256sum -- * > SHA256SUMS
   ```

TODO: confermare se Downloads sostituisce un file ricaricato con lo stesso
nome o li tiene entrambi.

## 5. Installazione

Prima verifica i file, nella cartella in cui li hai scaricati:

```bash
sha256sum -c --ignore-missing SHA256SUMS
```

Il checksum protegge da un download danneggiato o incompleto. Arriva dallo
stesso posto dei pacchetti, quindi non vale come firma (v2.0).

| Distribuzione | Installazione o aggiornamento | Rimozione |
|---|---|---|
| Debian, Ubuntu | `sudo apt install ./Dialga_X.Y.Z_amd64.deb` | `sudo apt remove dialga` |
| Fedora | `sudo dnf install ./Dialga-X.Y.Z-1.x86_64.rpm` | `sudo dnf remove dialga` |

Usa `apt`/`dnf`, non `dpkg -i`/`rpm -i`: installano anche le dipendenze
(WebKitGTK 4.1 e GTK 3). Il pacchetto si chiama `dialga` in entrambi i
formati.

Cosa viene installato:

| Percorso | Contenuto |
|---|---|
| `/usr/bin/dialga` | La CLI. L'app desktop usa lo stesso binario come engine (`dialga serve`) |
| `/usr/bin/dialga-desktop` | L'app desktop; voce di menu "Dialga" |
| `/usr/share/applications/Dialga.desktop` | Voce di menu |
| `/usr/share/icons/hicolor/<size>/apps/dialga-desktop.png` | Icone, da 16 a 512 px |
| `/usr/share/doc/dialga/` | `config.example.yaml`, `iam-policy.json`, `CHANGELOG.md` |

La CLI è nel `PATH` subito dopo l'installazione. Completamento della shell,
se serve:

```bash
mkdir -p ~/.local/share/bash-completion/completions
dialga completion bash > ~/.local/share/bash-completion/completions/dialga
```

Solo CLI (senza GUI, qualsiasi OS): estrai l'archivio della piattaforma e
metti `dialga` nel `PATH`, per esempio in `~/.local/bin`. Una CLI in
`/usr/local/bin` o `~/.local/bin` viene prima di `/usr/bin/dialga` del
pacchetto: verifica con `command -v dialga` e `dialga version`.

## 6. Primo avvio di un pacchetto non firmato

- **Installazione**: `apt` e `dnf` installano per default un file locale
  senza firma. Se il sistema la impone anche ai pacchetti locali (`dnf` con
  `localpkg_gpgcheck=1`), aggiungi `--nogpgcheck` solo per questo file, dopo
  aver verificato `SHA256SUMS`. `rpm -K` riporta i digest OK e nessuna
  firma: è normale fino alla v2.0.
- **Configurazione** (il pacchetto non crea nulla):

  ```bash
  dialga config path       # ~/.config/dialga/config.yaml
  cfg=$(dialga config path)
  [ -e "$cfg" ] || install -D -m 600 /usr/share/doc/dialga/config.example.yaml "$cfg"
  "${EDITOR:-vi}" "$cfg"   # replace the placeholders
  dialga config validate
  dialga check             # read-only pre-flight checks of the target
  ```

- **Permessi AWS**: `/usr/share/doc/dialga/iam-policy.json`, spiegata in
  `docs/iam-policy.md`.
- **Password SQL**: con `sql.password: prompt` la CLI la chiede sul
  terminale. L'app desktop la legge dal proprio ambiente, quindi per quei
  target va avviata da un terminale (il supporto al portachiavi dell'OS è
  previsto):

  ```bash
  read -rs DIALGA_SQL_PASSWORD && export DIALGA_SQL_PASSWORD && dialga-desktop
  ```

- **Nessun aggiornamento automatico**: le nuove versioni si scaricano dai
  Downloads e si installano come sopra.
- **Cosa resta dopo la rimozione**: la configurazione (`~/.config/dialga/`)
  e i dati della finestra (`~/.local/share/com.decisyon.dialga/`).

## 7. Provare il packaging senza pubblicare

- **Pipeline**: *Run pipeline*, qualsiasi branch, custom `release-dry-run`:
  archivi della CLI e pacchetti come artifact, nessun upload. La versione
  dell'engine è `dev-<commit>`; i pacchetti hanno la versione di
  `tauri.conf.json`.
- **In locale** (prerequisiti Fedora in `CLAUDE.md`):

  ```bash
  pnpm install --frozen-lockfile
  bash ci/build-engine.sh sidecar X.Y.Z x86_64-unknown-linux-gnu
  pnpm -C desktop tauri build --bundles deb,rpm
  # desktop/src-tauri/target/release/bundle/{deb,rpm}/
  dpkg-deb -I desktop/src-tauri/target/release/bundle/deb/Dialga_X.Y.Z_amd64.deb
  rpm -qip --requires desktop/src-tauri/target/release/bundle/rpm/Dialga-X.Y.Z-1.x86_64.rpm
  ```

## 8. Rollback

| Situazione | Cosa fare |
|---|---|
| La pipeline del tag è fallita prima dell'upload | Non è stato pubblicato nulla. Correggi su un branch e rilascia la PATCH successiva. Spostare il tag (`git push --delete origin vX.Y.Z` e di nuovo il tag) è accettabile solo perché non è stato caricato nulla |
| Un rilascio pubblicato è difettoso | Cancella i suoi file dai Downloads, tieni il tag, rilascia la PATCH successiva. Non riusare mai un numero di versione già pubblicato |
| Un utente ha bisogno della versione precedente | Scaricala dai Downloads (tienici almeno il rilascio precedente) e installala sopra quella corrente |

Installare una versione precedente:

```bash
sudo apt install --allow-downgrades ./Dialga_<previous>_amd64.deb   # Debian, Ubuntu
sudo dnf downgrade ./Dialga-<previous>-1.x86_64.rpm                 # Fedora
```

Nella `0.x` la configurazione può cambiare tra versioni minor: prima
dell'aggiornamento tieni una copia di `config.yaml`, e dopo un downgrade
controllala con `dialga config validate`.

## Limiti noti

- Solo x86_64. I pacchetti Linux arm64 arrivano con il workflow GitHub
  della v2.0 (`ENABLE_LINUX_ARM64`); l'archivio arm64 della CLI esiste già.
- Il binario desktop richiede la glibc del sistema che l'ha compilato, o
  una più recente. La pipeline compila su `node:24`, oggi Debian 12 (glibc
  2.36): Debian 12+, Ubuntu 24.04+, Fedora 37+. Ubuntu 22.04 (glibc 2.35):
  TODO: confermare. Una build su Fedora 43 richiede glibc 2.39
  (`pidfd_spawnp`): pubblica solo i pacchetti compilati dalla pipeline.
- Licenza: TODO: confermare (i pacchetti non ne dichiarano una).
