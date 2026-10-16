# Changelog

All notable changes to Dialga are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/) (`0.x`: the CLI, the API
and the configuration may still change between minor versions).

Releasing: `docs/release.md`.

## [Unreleased]

First release line (v0.1): configuration, read-only checks and native
task tracking, from the CLI and from the Linux desktop app.

### Added

- **CLI `dialga`**: `check [target]`, `status [target]` (`--db`,
  `--limit`), `wait <task-id>` (`--interval`; Ctrl-C stops the wait, never
  the task), `config path|validate|show`, `serve`, `version` and
  `completion`. Global `--config` and `--output text|json`. Exit codes:
  0 OK, 1 error, 2 a check failed, 3 the RDS task ended in `ERROR` or
  `CANCELLED`.
- **Target configuration** (`apiVersion: v1`) in
  `<user config dir>/dialga/config.yaml`: strict parsing (unknown keys are
  errors), defaults, validation and an account guard on every command.
  The SQL password is only a reference (`prompt`, `env:VAR`), never
  stored. Commented example: `docs/config.example.yaml`.
- **Pre-flight checks** with the same verdicts as `mssql-import.sh check`:
  AWS account, instance (`available`, option group `in-sync`),
  `SQLSERVER_BACKUP_RESTORE` option, bucket reachable, storage (free space
  from CloudWatch), tunnel, SQL login with the TLS certificate verified
  against the embedded Amazon RDS CA bundle, native tasks. A `warning`
  status reports what needs attention without failing.
- **Native backup/restore tasks**: list them (`status`) and follow one to
  the end (`wait`), from `msdb.dbo.rds_fn_task_status`.
- **Action plans** in the engine: dry-run preview by default, confirmation
  for protected targets and destructive actions, and the T-SQL of the
  `rds_*` procedures, identical to `mssql-import.sh` (golden files). Used
  by the import and export commands of v0.2.
- **Local API** for the desktop app (`dialga serve`): 127.0.0.1 on a
  random port, per-session bearer token received on stdin, `Host` and
  `Origin` checks, the engine stops when its stdin closes. Endpoints for
  health, targets, checks (Server-Sent Events) and tasks, described by an
  OpenAPI 3.1 contract served at `GET /api/v1/openapi.yaml`.
- **Desktop app** (Tauri v2, React, Decisyon Design System) with the
  engine as sidecar: Instances screen (one card per target, live checks,
  tasks panel) and Settings (language English or Italian, theme system,
  light or dark, configuration path, engine version).
- **Linux packages** (deb, rpm), unsigned: the app, the `dialga` CLI in
  `/usr/bin`, a desktop entry, icons from 16 to 512 px, and the example
  configuration, the minimal IAM policy and this changelog in
  `/usr/share/doc/dialga/`.
- **Minimal IAM policy** for Dialga users: `docs/iam-policy.json`
  (explained in `docs/iam-policy.md`).
- **Release tooling**: Bitbucket Pipelines (checks on pull requests; on
  `v*` tags the CLI for six platforms, the Linux packages and a manual
  upload to the repository Downloads with `SHA256SUMS`),
  `ci/bump-version.sh` and `docs/release.md`.
