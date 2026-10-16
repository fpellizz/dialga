# Releasing Dialga (v1, Linux)

A release is a `vX.Y.Z` tag on `main`. The Bitbucket tag pipeline builds
the CLI for six platforms and the Linux packages; a **manual** step uploads
them, with `SHA256SUMS`, to the repository Downloads. Packages are
**unsigned** until v2.0.

| Output | File names |
|---|---|
| Desktop app + CLI, Debian/Ubuntu | `Dialga_X.Y.Z_amd64.deb` |
| Desktop app + CLI, Fedora and other rpm distributions | `Dialga-X.Y.Z-1.x86_64.rpm` |
| CLI only | `dialga_X.Y.Z_{linux,darwin}_{amd64,arm64}.tar.gz`, `dialga_X.Y.Z_windows_{amd64,arm64}.zip` |
| Checksums | `SHA256SUMS` (all of the above) |

Versions are plain `MAJOR.MINOR.PATCH`: a pre-release suffix such as
`-rc.1` is not a valid rpm version and sorts after the release in dpkg.
In `0.x` a MINOR may break the CLI, the API or the configuration.

## 1. Prepare the release commit

On a branch (`chore/release-vX.Y.Z`), from an up-to-date `main`:

1. `CHANGELOG.md`: rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`
   and add a new empty `## [Unreleased]` above it.
2. Set the version everywhere it is declared and check it:

   ```bash
   bash ci/bump-version.sh X.Y.Z          # tauri.conf.json, Cargo.toml, Cargo.lock, package.json files
   bash ci/bump-version.sh --check X.Y.Z  # what the pipeline compares with the tag
   ```

   The script changes nothing if any file cannot be updated, and fails if a
   manifest with a version is not in its list.
3. Optional, recommended for packaging changes: build the packages locally
   (section 7).
4. Commit `chore(release): vX.Y.Z`, open the pull request, merge it.

## 2. Tag

On the merge commit in `main`:

```bash
git switch main && git pull --ff-only
bash ci/bump-version.sh --check X.Y.Z
git tag -a vX.Y.Z -m "Dialga vX.Y.Z"
git push origin vX.Y.Z
```

## 3. What the tag pipeline does

| Order | Step | Result |
|---|---|---|
| 1 (parallel) | Engine tests and golangci-lint, web lint/typecheck/test/build, desktop fmt/clippy/test, gitleaks | Same checks as a pull request, all of them |
| 2 | Engine integration tests | SQL Server 2022 + `rds_*` mock + MinIO |
| 3 | Engine cross-build | Six CLI archives, and the Linux sidecar stamped with the tag version |
| 4 | Desktop Linux bundles | Stops if `tauri.conf.json` is not the tag version; builds the deb and the rpm |
| 5 | **Release: upload to Bitbucket Downloads** | **Manual** (*Run* button) |
| 6 | Mirror to GitHub | Off until v2.0 |

Any failing step stops the pipeline before the upload: nothing is published.

## 4. Upload to Downloads (manual)

1. Bitbucket, *Pipelines*, the pipeline of the tag: check the artifacts of
   steps 3 and 4.
2. Press *Run* on "Release · upload to Bitbucket Downloads". It copies the
   archives and the packages, writes `SHA256SUMS` and uploads everything.
   Pipeline artifacts last 14 days: run it within that time, or run the
   tag pipeline again.
3. Without `BITBUCKET_DOWNLOADS_TOKEN` the step uploads nothing. Then
   download the artifacts of steps 3 and 4, and upload them by hand
   (*Repository*, *Downloads*, *Add files*) together with:

   ```bash
   rm -f SHA256SUMS.engine && sha256sum -- * > SHA256SUMS
   ```

TODO: confirm whether Downloads replaces a file uploaded again with the
same name or keeps both.

## 5. Install

Check the files first, in the folder where they were downloaded:

```bash
sha256sum -c --ignore-missing SHA256SUMS
```

The checksum guards against a damaged or incomplete download. It comes
from the same place as the packages, so it is no signature (v2.0).

| Distribution | Install or upgrade | Remove |
|---|---|---|
| Debian, Ubuntu | `sudo apt install ./Dialga_X.Y.Z_amd64.deb` | `sudo apt remove dialga` |
| Fedora | `sudo dnf install ./Dialga-X.Y.Z-1.x86_64.rpm` | `sudo dnf remove dialga` |

Use `apt`/`dnf`, not `dpkg -i`/`rpm -i`: they also install the
dependencies (WebKitGTK 4.1 and GTK 3). The package name is `dialga` in
both formats.

What gets installed:

| Path | Content |
|---|---|
| `/usr/bin/dialga` | The CLI. The desktop app runs the same binary as its engine (`dialga serve`) |
| `/usr/bin/dialga-desktop` | The desktop app; menu entry "Dialga" |
| `/usr/share/applications/Dialga.desktop` | Menu entry |
| `/usr/share/icons/hicolor/<size>/apps/dialga-desktop.png` | Icons, 16 to 512 px |
| `/usr/share/doc/dialga/` | `config.example.yaml`, `iam-policy.json`, `CHANGELOG.md` |

The CLI is on `PATH` right after installation. Shell completion, if
wanted:

```bash
mkdir -p ~/.local/share/bash-completion/completions
dialga completion bash > ~/.local/share/bash-completion/completions/dialga
```

CLI only (no GUI, any OS): unpack the archive for the platform and put
`dialga` on `PATH`, for example in `~/.local/bin`. A CLI in
`/usr/local/bin` or `~/.local/bin` comes before the packaged
`/usr/bin/dialga`: check with `command -v dialga` and `dialga version`.

## 6. First run of an unsigned package

- **Installing**: `apt` and `dnf` install a local file without a signature
  by default. If the system enforces signatures on local packages (`dnf`
  with `localpkg_gpgcheck=1`), add `--nogpgcheck` for this file only, after
  checking `SHA256SUMS`. `rpm -K` reports the digests as OK and no
  signature: expected until v2.0.
- **Configuration** (nothing is created by the package):

  ```bash
  dialga config path       # ~/.config/dialga/config.yaml
  cfg=$(dialga config path)
  [ -e "$cfg" ] || install -D -m 600 /usr/share/doc/dialga/config.example.yaml "$cfg"
  "${EDITOR:-vi}" "$cfg"   # replace the placeholders
  dialga config validate
  dialga check             # read-only pre-flight checks of every target
  ```

- **AWS permissions**: `/usr/share/doc/dialga/iam-policy.json`, explained
  in `docs/iam-policy.md`.
- **SQL password**: with `sql.password: prompt`, the CLI asks for it on
  the terminal. The desktop app reads it from its environment, so start it
  from a terminal for those targets (OS keychain support is planned):

  ```bash
  read -rs DIALGA_SQL_PASSWORD && export DIALGA_SQL_PASSWORD && dialga-desktop
  ```

- **No auto-update**: new versions come from Downloads, installed as above.
- **What stays after removal**: the configuration (`~/.config/dialga/`)
  and the app data of the window (`~/.local/share/com.decisyon.dialga/`).

## 7. Try the packaging without publishing

- **Pipeline**: *Run pipeline*, any branch, custom `release-dry-run`: CLI
  archives and packages as artifacts, no upload. The engine version is
  `dev-<commit>`; the packages carry the version of `tauri.conf.json`.
- **Locally** (Fedora prerequisites in `CLAUDE.md`):

  ```bash
  pnpm install --frozen-lockfile
  bash ci/build-engine.sh sidecar X.Y.Z x86_64-unknown-linux-gnu
  pnpm -C desktop tauri build --bundles deb,rpm
  # desktop/src-tauri/target/release/bundle/{deb,rpm}/
  dpkg-deb -I desktop/src-tauri/target/release/bundle/deb/Dialga_X.Y.Z_amd64.deb
  rpm -qip --requires desktop/src-tauri/target/release/bundle/rpm/Dialga-X.Y.Z-1.x86_64.rpm
  ```

## 8. Rollback

| Situation | What to do |
|---|---|
| Tag pipeline failed before the upload | Nothing was published. Fix on a branch and release the next PATCH. Moving the tag instead (`git push --delete origin vX.Y.Z`, tag again) is acceptable only because nothing was uploaded |
| A published release is broken | Delete its files from Downloads, keep the tag, release the next PATCH. Never reuse a published version number |
| A user needs the previous version | Download it from Downloads (keep at least the previous release there) and install it over the current one |

Installing an older version:

```bash
sudo apt install --allow-downgrades ./Dialga_<previous>_amd64.deb   # Debian, Ubuntu
sudo dnf downgrade ./Dialga-<previous>-1.x86_64.rpm                 # Fedora
```

In `0.x` the configuration may change between minor versions: keep a copy
of `config.yaml` before upgrading, and check it with `dialga config
validate` after a downgrade.

## Known limits

- x86_64 only. Linux arm64 packages arrive with the v2.0 GitHub workflow
  (`ENABLE_LINUX_ARM64`); the arm64 CLI archive exists already.
- The desktop binary needs the glibc of the image that built it, or newer.
  Built on Fedora 43 it needs glibc 2.39 (`pidfd_spawnp`). TODO: confirm
  the minimum for the pipeline image.
- License: TODO: confirm (the packages carry none).
