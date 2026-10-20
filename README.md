# Dialga

Import and export Microsoft SQL Server databases on **Amazon RDS for SQL Server** through S3, from a desktop app or a CLI.

Dialga wraps the native RDS procedures (`rds_restore_database`, `rds_backup_database`, …) behind pre-flight checks, a dry run that shows the exact T-SQL, confirmations for production targets and a journal of every operation. It replaces the `mssql-import.sh` script.

| | |
|---|---|
| **Version** | 0.7.0 ([CHANGELOG](CHANGELOG.md)) |
| **Platforms** | Linux x86_64: desktop app + CLI (deb, rpm). CLI only: Linux, macOS, Windows (amd64, arm64) |
| **Full user guide** | [`docs/guida-utente.md`](docs/guida-utente.md) (Italian) |
| **License** | MIT ([LICENSE](LICENSE)) |

## Contents

1. [What it does](#1-what-it-does)
2. [How it works](#2-how-it-works)
3. [Prerequisites](#3-prerequisites)
4. [AWS setup and IAM](#4-aws-setup-and-iam)
5. [Installation](#5-installation)
6. [Configuration](#6-configuration)
7. [Usage](#7-usage)
8. [Safety model](#8-safety-model)
9. [Troubleshooting](#9-troubleshooting)
10. [Development](#10-development)
11. [Release](#11-release)
12. [Repository layout](#12-repository-layout)

## 1. What it does

| Feature | CLI | Desktop app |
|---|---|---|
| Pre-flight checks of a target (account, instance, option group, bucket, storage, tunnel, SQL login with verified TLS, native tasks) | `dialga check` | Instances → *Run check* |
| Machine diagnostics (SSM plugin, container runtime, AWS profiles) | `dialga doctor` | Settings → *Prerequisites* |
| Guided configuration of targets | `dialga config init`, `add-target`, `remove-target` | Instances → *Add instance*, Settings → *Targets* |
| Integrated SSM tunnel to the instance | `dialga tunnel`, `connect.mode: tunnel` | Shared tunnel per target, status in the sidebar |
| Databases of an instance | `dialga databases` | Databases |
| Inspect `.bak` files (engine version, encryption, TDE, FILESTREAM, restored size) in a throwaway SQL Server container | `dialga inspect` | Import wizard, *Verify* step |
| Upload/download backups to/from S3 (multipart, checksum verified, resumable) | `dialga upload`, `dialga download` | Import and Export wizards |
| Restore, export (backup to S3), finish, drop, cancel | `dialga restore`, `export`, `finish`, `drop`, `cancel` | Databases row actions |
| Restore over an existing database (swap with rename, or drop with a safety backup) | `dialga restore --replace auto\|swap\|drop` | *Replace* choice in the restore dialog |
| Many databases as one job (import or export) | `dialga import`, `dialga run <plan.yaml>` | Import and Export wizards |
| Jobs: list, details, resume | `dialga jobs`, `jobs show`, `jobs resume` | Activity (every operation, refused runs and CLI jobs included) |
| Post-restore report (files, logins, orphan users, objects) | `dialga post` | Databases → *Report* |
| **Database access**: grant a SQL login read / read-write / owner access and the right to back the database up, revoke, fix orphan users, create logins | `dialga access list\|logins\|grant\|revoke\|fix-orphan` | Databases → *Access…* |
| SQL password from prompt, keyring, environment or Secrets Manager | `dialga password set\|forget\|status` | Password dialog, *Remember* |
| Notice of a new release (on by default, can be turned off) | `dialga version`, `dialga version --check`, `DIALGA_NO_UPDATE_CHECK=1` | Sidebar notice, Settings → *Check for updates* |

## 2. How it works

```
 ┌──────────────── workstation ─────────────────┐            ┌──────────── AWS account ────────────┐
 │ Desktop app (Tauri + React)                  │            │                                     │
 │      │ local API on 127.0.0.1 (bearer token) │            │  S3 bucket  ◄──── RDS option role ──┤
 │      ▼                                       │  AWS APIs  │     ▲                               │
 │ dialga engine (Go) ── also the CLI ──────────┼───────────►│     │ upload/download               │
 │      │                                       │            │                                     │
 │      └── SQL (TLS verified) ── SSM tunnel ───┼──► bridge EC2 (ssm-bridge) ──► RDS SQL Server   │
 └──────────────────────────────────────────────┘            └─────────────────────────────────────┘
```

- **One engine, two front ends.** `/usr/bin/dialga` is the CLI and also the engine the desktop app starts as a sidecar (`dialga serve`: local API on a random `127.0.0.1` port, session token on stdin, OpenAPI contract at `GET /api/v1/openapi.yaml`).
- **Backups travel through S3.** Dialga uploads a `.bak` to the bucket, then asks RDS to restore it (`msdb.dbo.rds_restore_database`); an export asks RDS to back up to the bucket, then downloads the file. RDS reads and writes S3 with the IAM role of its `SQLSERVER_BACKUP_RESTORE` option, never with your credentials.
- **SQL Server is reached** directly, through a tunnel you already have, or through an **integrated SSM port-forwarding tunnel** via a bridge EC2 instance in the VPC. The TLS certificate of SQL Server is always verified against the RDS endpoint name (the Amazon RDS CA bundle is embedded).
- **Jobs** are journaled under the user data directory, shared by the CLI and the app, and can be resumed.

## 3. Prerequisites

### 3.1 On the workstation

| What | Needed for | Check |
|---|---|---|
| Linux x86_64 with glibc ≥ 2.36 (Debian 12+, Ubuntu 24.04+, Fedora 37+); Ubuntu 22.04: TODO: confirm | Desktop app | `ldd --version` |
| An **AWS profile** (`~/.aws/config`, access keys or SSO) in the account of the target | Every command | `aws sts get-caller-identity --profile <profile>` |
| The **IAM permissions** of [section 4.2](#42-iam-policy-for-dialga-users) | Every command | `dialga check` |
| **`session-manager-plugin`**, newer than 1.2.497.0 | Integrated tunnel (`connect.mode: tunnel`, `dialga tunnel`) | `session-manager-plugin --version` |
| **podman** (recommended) or **docker**, and the image `mcr.microsoft.com/mssql/server:2022-latest` (~1.5 GB, pulled by you, never by Dialga) | `dialga inspect` only | `podman image exists mcr.microsoft.com/mssql/server:2022-latest` |
| A **SQL Server login** on the instance (usually the RDS master user, see [4.4](#44-sql-server-permissions)) | SQL checks, tasks, databases, operations | Ask the instance administrator |
| A Secret Service keyring (GNOME Keyring, KWallet) | Optional: *Remember* the SQL password | `dialga password status` |

```bash
# Session Manager plugin on Fedora/RHEL (other distributions: AWS docs "Install the Session Manager plugin")
sudo dnf install https://s3.amazonaws.com/session-manager-downloads/plugin/latest/linux_64bit/session-manager-plugin.rpm

# SQL Server image for `dialga inspect`
podman pull mcr.microsoft.com/mssql/server:2022-latest

# What is missing on this machine, with the install command for your distribution
dialga doctor
```

### 3.2 On AWS (once per instance)

| What | Why |
|---|---|
| An RDS for SQL Server instance | The target |
| An **option group** with the `SQLSERVER_BACKUP_RESTORE` option and an **IAM role** that can use the bucket ([4.1](#41-iam-role-of-the-rds-backup-option)) | Native backup and restore through S3 |
| An **S3 bucket** in the same region, with an import and an export prefix (default `import/` and `export/`) | Where backups travel |
| For the integrated tunnel: a **bridge EC2 instance** tagged `Name=ssm-bridge` (configurable) in the VPC of the instance, managed by SSM, whose security group can reach the instance on 1433 ([4.3](#43-ssm-bridge-for-the-integrated-tunnel)) | SQL access from outside the VPC |

## 4. AWS setup and IAM

There are **two different identities**:

| Identity | Who | Permissions |
|---|---|---|
| The **RDS option role** | RDS itself, when it restores from or backs up to S3 | [4.1](#41-iam-role-of-the-rds-backup-option) |
| The **Dialga user** | The IAM user, group or role behind the `aws_profile` of each target | [4.2](#42-iam-policy-for-dialga-users) |

### 4.1 IAM role of the RDS backup option

Configured once per instance, as in the AWS documentation *Support for native backup and restore in SQL Server* (IAM role and `SQLSERVER_BACKUP_RESTORE` option). In short:

Trust policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Principal": { "Service": "rds.amazonaws.com" }, "Action": "sts:AssumeRole" }
  ]
}
```

Permissions (replace `[bucket]`):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": ["s3:ListBucket", "s3:GetBucketLocation"], "Resource": "arn:aws:s3:::[bucket]" },
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:ListMultipartUploadParts", "s3:AbortMultipartUpload"],
      "Resource": "arn:aws:s3:::[bucket]/*"
    }
  ]
}
```

Then add the option `SQLSERVER_BACKUP_RESTORE` with `IAM_ROLE_ARN` = that role to the option group of the instance. `dialga check` verifies that the option is there and that the option group is `in-sync`. With SSE-KMS on the bucket, the role also needs `kms:GenerateDataKey` and `kms:Decrypt` on the key.

### 4.2 IAM policy for Dialga users

The minimal policy is [`docs/iam-policy.json`](docs/iam-policy.json) (also installed as `/usr/share/doc/dialga/iam-policy.json`), explained in [`docs/iam-policy.md`](docs/iam-policy.md). Attach one copy per account and region to the user, group or role behind the `aws_profile` of each target, after replacing the placeholders:

| Placeholder | Replace with | Example |
|---|---|---|
| `[account-id]` | `account_id` of the target | `123456789012` |
| `[region]` | `region` of the target | `eu-west-1` |
| `[bucket]` | `bucket` of the target | `my-sqlserver-backups` |
| `[import-prefix]`, `[export-prefix]` | `prefixes.import`, `prefixes.export` (default `import`, `export`) | `import` |
| `[secret-name]` | Only for the optional Secrets Manager statement | `dialga/dev-sql` |

| Sid | Actions | Used for |
|---|---|---|
| `Identity` | `sts:GetCallerIdentity` | Account guard on every command |
| `RdsDescribe` | `rds:DescribeDBInstances`, `rds:DescribeOptionGroups` | Instance state, endpoint, option group, storage |
| `RdsFreeStorage` | `cloudwatch:GetMetricStatistics` | Free storage (`FreeStorageSpace`); CloudWatch has no resource-level permissions: `*` |
| `BackupBucket` | `s3:ListBucket` | `HeadBucket` (not restricted by prefix, or `HeadBucket` is denied) |
| `OptionalListBuckets` | `s3:ListAllMyBuckets` | Optional: the bucket drop-down in *Add instance* and `config init`; without it you type the name. Cannot be scoped (`*`) and shows every bucket name of the account |
| `BackupObjects` | `s3:GetObject`, `s3:PutObject`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts` | Upload to the import prefix, download from the export prefix, resume and abort multipart uploads |
| `TunnelFindBridge` | `ec2:DescribeInstances` | Integrated tunnel: find the bridge (`*`, no resource-level permissions) |
| `TunnelStartSession` | `ssm:StartSession` | Integrated tunnel: port forwarding through the bridge, only with the document `AWS-StartPortForwardingSessionToRemoteHost` (no shell) |
| `TunnelTerminateOwnSession` | `ssm:TerminateSession` | Integrated tunnel: close only your own sessions |
| `OptionalSqlPasswordFromSecretsManager` | `secretsmanager:GetSecretValue` | Only with `sql.password: secretsmanager:<ARN>`, on that secret only; remove it otherwise. A customer-managed KMS key also needs `kms:Decrypt` |

Notes:

- Several targets on one bucket: one `[bucket]/[prefix]/*` line per prefix. Several buckets: one `BackupBucket` resource and object resources for each.
- With SSE-KMS on the bucket, add `kms:GenerateDataKey` and `kms:Decrypt` on that key.
- To restrict `ssm:StartSession` to the bridge, split the statement and add `"Condition": {"StringEquals": {"ssm:resourceTag/Name": "ssm-bridge"}}` on the instance resource (the condition does not apply to the document).
- SQL Server access is **not** IAM: it comes from the SQL login of the target (`sql.user`).

### 4.3 SSM bridge for the integrated tunnel

Only for `connect.mode: tunnel` (the default for new targets):

- an EC2 instance in the **VPC of the RDS instance**, `running`, tagged `Name=ssm-bridge` (or the `connect.bridge_tag` of the target);
- registered with **Systems Manager** (SSM agent and an instance profile with `AmazonSSMManagedInstanceCore`);
- its security group allowed to reach the RDS instance on port **1433** (the RDS security group must accept it).

Dialga finds the bridge by tag, VPC and state, opens `AWS-StartPortForwardingSessionToRemoteHost` to the RDS endpoint on a local port, and reconnects on its own if the session drops.

### 4.4 SQL Server permissions

Dialga logs in with the SQL login of the target, normally the **RDS master user**. It has what every feature needs: the `rds_*` procedures in `msdb`, `rdsadmin.dbo.rds_modify_db_name` for renames and, for database access, `ALTER ANY LOGIN`, user and role management in user databases and `GRANT … WITH GRANT OPTION` on the msdb backup procedures (verified on a live instance). A less privileged login is enough for read-only commands only.

## 5. Installation

### 5.1 Download

Packages are in the **Downloads** of the Bitbucket repository `decisyon/dialga`:

| File | For |
|---|---|
| `Dialga_X.Y.Z_amd64.deb` | Debian, Ubuntu: desktop app + CLI |
| `Dialga-X.Y.Z-1.x86_64.rpm` | Fedora and other rpm distributions: desktop app + CLI |
| `dialga_X.Y.Z_<os>_<arch>.tar.gz` / `.zip` | CLI only (Linux, macOS, Windows; amd64, arm64) |
| `SHA256SUMS` | Checksums of all files |

```bash
sha256sum -c --ignore-missing SHA256SUMS
```

Packages are **not signed** until v2.0: the checksum only protects against a damaged download.

### 5.2 Install

| Distribution | Install or upgrade | Remove |
|---|---|---|
| Debian, Ubuntu | `sudo apt install ./Dialga_X.Y.Z_amd64.deb` | `sudo apt remove dialga` |
| Fedora | `sudo dnf install ./Dialga-X.Y.Z-1.x86_64.rpm` | `sudo dnf remove dialga` |

- Use `apt`/`dnf`, not `dpkg -i`/`rpm -i`: they also install WebKitGTK 4.1 and GTK 3.
- If `dnf` refuses an unsigned local package (`localpkg_gpgcheck=1`), add `--nogpgcheck` for this file after checking `SHA256SUMS`.
- There is no auto-update: install new versions the same way, over the old one.

Installed files:

| Path | Content |
|---|---|
| `/usr/bin/dialga` | The CLI (and the engine of the desktop app) |
| `/usr/bin/dialga-desktop` | The desktop app, menu entry **Dialga** |
| `/usr/share/doc/dialga/` | `config.example.yaml`, `iam-policy.json`, `CHANGELOG.md` |

**CLI only:** extract the archive and put `dialga` on your `PATH` (for example `~/.local/bin`); check with `command -v dialga && dialga version`.

Shell completion (optional):

```bash
mkdir -p ~/.local/share/bash-completion/completions
dialga completion bash > ~/.local/share/bash-completion/completions/dialga
```

### 5.3 First run

1. `dialga doctor`: what is missing on the machine.
2. `dialga config init` in a terminal, or *Add instance* in the app: creates the first target.
3. `dialga config validate`, then `dialga check`.
4. Start the app from the menu (**Dialga**) or with `dialga-desktop`.

Removing the package leaves the configuration (`~/.config/dialga/`) and the window data (`~/.local/share/com.decisyon.dialga/`).

## 6. Configuration

One YAML file shared by the CLI and the app: `dialga config path` (normally `~/.config/dialga/config.yaml`, mode `0600`). Every command takes `--config <file>`. A commented example is [`docs/config.example.yaml`](docs/config.example.yaml).

```yaml
apiVersion: v1
targets:
  dev-sql:                          # lower case letters, digits and "-"
    account_id: "123456789012"      # account guard: every command stops if the profile points elsewhere
    region: eu-west-1
    aws_profile: default
    instance: my-sqlserver          # RDS DB instance identifier (not the endpoint)
    bucket: my-sqlserver-backups    # bucket of the SQLSERVER_BACKUP_RESTORE option
    prefixes: { import: import, export: export }
    protected: false                # true for production: every change asks to retype the target name
    max_tasks: 2                    # native tasks in parallel: 1 or 2 (RDS maximum)
    sql: { user: admin, password: prompt }
    connect: { mode: tunnel, local_port: 0, bridge_tag: ssm-bridge }
```

Parsing is **strict**: an unknown key is an error, never a silent default. `dialga config validate` says which target is wrong and why; `dialga config show` prints the file with defaults applied.

**Connection modes** (`connect.mode`):

| Mode | When | What Dialga does |
|---|---|---|
| `tunnel` | Workstation outside the VPC (usual case) | Opens the SSM tunnel itself; the app keeps one shared tunnel per target |
| `external` | You already have a tunnel open | Connects to `127.0.0.1:<local_port>` |
| `direct` | Workstation inside the VPC | Connects to the RDS endpoint |

**SQL password** (`sql.password`): a reference, never the password itself.

| Value | Source |
|---|---|
| `prompt` (default) | Typed in the app or the terminal, then `DIALGA_SQL_PASSWORD`, then the keyring if remembered |
| `env:VAR` | Environment variable `VAR` |
| `keyring` | System keyring (service `dialga`, account = target name): `dialga password set <target>` |
| `secretsmanager:<ARN>` | AWS Secrets Manager (JSON with a `password` field, as RDS-managed secrets, or a plain string) |

The password is never written to the configuration, the job journal, events or logs.

## 7. Usage

Every command that changes something is a **dry run by default**: it runs the read-only checks and prints the plan with the exact T-SQL; add `--apply` to run it. Common options: `--config <file>`, `-o text|json`, and `-t <target>` (optional when the configuration has one target).

```bash
dialga check                                  # pre-flight checks of the (only) target
dialga databases dev-sql                      # databases with state, size, compatibility, owner
dialga inspect ./sales.bak -t dev-sql         # can this backup be restored on that instance?

dialga import ./sales.bak ./hr.bak -t dev-sql            # preview: upload + restore + report
dialga import ./sales.bak -t dev-sql --apply             # run it as a job
dialga restore sales import/sales.bak --replace auto     # restore over an existing database (preview)
dialga export sales -t dev-sql --apply                   # native backup to S3
dialga download export/sales_20261019-101500.bak -t dev-sql --apply

dialga jobs                                   # the journal (CLI and app)
dialga jobs resume <job-id>

dialga access list sales -t dev-sql                              # who can use the database
dialga access grant sales app_reader --level read -t dev-sql     # preview
dialga access grant sales app_rw --level readwrite --backup \
  --create-login --generate-password --apply -t dev-sql          # new login, password shown once
dialga access revoke sales app_rw --remove backup --apply -t dev-sql
dialga access fix-orphan sales legacy_user --apply -t dev-sql

dialga tunnel dev-sql --port 11433            # keep a tunnel open for SQL clients (Ctrl-C closes it)
```

Exit codes: `0` OK, `1` error, `2` a check failed (nothing changed), `3` a native task ended in `ERROR` or `CANCELLED`.

The **desktop app** has the same features: *Instances* (one card per target: overview, live checks, tasks, tunnel), *Databases* (row actions: report, export, finish, access, drop; restore from S3), the *Import* and *Export* wizards, *Activity* (every job, refused runs, CLI jobs; resume, cancel) and *Settings* (English or Italian, theme, targets, prerequisites).

The complete reference, with every option and screen, is the user guide: [`docs/guida-utente.md`](docs/guida-utente.md).

## 8. Safety model

- **Read-only checks first**, dry run by default, the exact T-SQL in every preview, and a digest so that what runs is what was previewed.
- **Account guard**: a target pins its AWS account; a profile pointing elsewhere stops every command.
- **Protected targets** (`protected: true`) ask to retype the target name before any change; destructive actions (drop, replace, removing a user) ask to retype the object name on any target.
- **Restore over an existing database** keeps the old one (swap: renamed to `<db>_old_<stamp>`) or backs it up to S3 first (drop).
- **Secrets**: SQL passwords only as references; the password of a new login only in the executed `CREATE LOGIN`, masked (`****`) everywhere else, never in the journal or logs.
- **Local API** on `127.0.0.1` only, with a per-session bearer token and `Host`/`Origin` checks.
- **Privacy**: the only network access that is not AWS or your instance is the update check, one unauthenticated `GET` of the latest release to `api.github.com` (at most every 12 hours; nothing about you or your machine is sent). Turn it off in Settings or with `DIALGA_NO_UPDATE_CHECK=1`.
- Dialga never changes the master user, `sa`, `rdsa` or system principals, and never drops logins.

## 9. Troubleshooting

| Symptom | Look at |
|---|---|
| Anything AWS (profile, expired SSO, wrong account, permissions) | `dialga check` names the failing call; [4.2](#42-iam-policy-for-dialga-users) |
| The tunnel does not open | `session-manager-plugin --version` (> 1.2.497.0), bridge tag/VPC/state, bridge security group to 1433 |
| The SQL login fails or is skipped | `dialga password status`, `sql.user`, the password source |
| A `.bak` cannot be restored | `dialga inspect` (a backup newer than the instance needs the `.bacpac` route described in the guide) |
| Users without a login after a restore | `dialga access list <db>`, then `fix-orphan` or `grant --create-login` |

More in section 7 of the [user guide](docs/guida-utente.md).

## 10. Development

| Part | Path | Stack |
|---|---|---|
| Engine, CLI, local API | `engine/` | Go 1.26 (`cmd/dialga`, `internal/…`); OpenAPI contract in `engine/internal/api/openapi.yaml` |
| Web UI | `web/` | React, Vite, Decisyon Design System (Base UI), Vitest, Playwright |
| Desktop shell | `desktop/` | Tauri v2 (Rust), engine as sidecar |

Requirements: Go 1.26, Node ≥ 22 with pnpm 12.11.2 (or `npx pnpm@12.11.2`), Rust stable, Docker or Podman for the integration tests. Desktop build libraries: on Fedora `webkit2gtk4.1-devel`, `libxdo-devel`, `libappindicator-gtk3-devel`, `librsvg2-devel`, `openssl-devel`; on Debian `libwebkit2gtk-4.1-dev`, `libxdo-dev`, `libayatana-appindicator3-dev`, `librsvg2-dev`, `libssl-dev`.

```bash
# engine
cd engine && gofmt -l . && go vet ./... && go test -race ./...
cd engine && TESTCONTAINERS_RYUK_DISABLED=true go test -tags integration -p 1 -count=1 ./...   # SQL Server + rds_* mock + MinIO

# web (same scripts as CI)
pnpm install
pnpm -C web run lint && pnpm -C web run typecheck && pnpm -C web run test && pnpm -C web run build && pnpm -C web run e2e

# desktop: sidecar for the host, then deb/rpm in desktop/src-tauri/target/release/bundle/
bash ci/build-engine.sh sidecar dev x86_64-unknown-linux-gnu
pnpm -C desktop tauri build --bundles deb,rpm
```

Conventions (branches, Conventional Commits, tests, architecture notes) are in [`CLAUDE.md`](CLAUDE.md); job plans in [`docs/piani-job.md`](docs/piani-job.md).

**CI** (Bitbucket Pipelines, `bitbucket-pipelines.yml`): pull requests run the checks of the changed parts (engine, web, desktop, secrets) and the integration tests; `main` runs the secrets scan; tags `v*` run everything, build the CLI for six platforms and the Linux packages, and wait for a manual *upload to Bitbucket Downloads*.

## 11. Release

See [`docs/release.md`](docs/release.md). In short: on a `chore/release-vX.Y.Z` branch, move `## [Non rilasciato]` to `## [X.Y.Z] - date` in the CHANGELOG, run `bash ci/bump-version.sh X.Y.Z` (and `--check`), open a PR, merge it, tag the merge commit `vX.Y.Z` (annotated) and push the tag. A published tag is never moved: re-run its pipeline instead.

## 12. Repository layout

| Path | Content |
|---|---|
| `engine/` | Go engine, CLI and local API |
| `web/` | React UI (also served by Vite for development) |
| `desktop/` | Tauri desktop shell and packaging (icons in `desktop/src-tauri/icons/`) |
| `ci/` | Build and release scripts (`build-engine.sh`, `bump-version.sh`) |
| `docs/` | User guide, IAM policy, example configuration, job plans, release procedure, brand assets |
| `CHANGELOG.md` | Changes per version (Italian) |
