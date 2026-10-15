#!/usr/bin/env bash
#
# ci/tauri-placeholders.sh
#
# Lays down what tauri-build checks at compile time, for the desktop CHECK
# jobs (cargo fmt / clippy / test) that build neither the frontend nor the
# engine:
#   - web/dist/index.html          frontendDist, embedded by generate_context!
#   - desktop/src-tauri/binaries/dialga-<host triple>[.exe]
#                                  externalBin (the engine sidecar)
# Existing files are never touched. Release builds do NOT use this script:
# they build the real frontend and sidecar (.github/workflows/release.yml).
#
# Called by: bitbucket-pipelines.yml (Desktop step), .github/workflows/ci.yml
# (job "desktop").

set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT

triple=$(rustc -vV | sed -n 's/^host: //p')
[[ -n "$triple" ]] || { echo "tauri-placeholders: cannot read the rustc host triple" >&2; exit 1; }

index="$ROOT/web/dist/index.html"
if [[ ! -f "$index" ]]; then
  mkdir -p "$(dirname "$index")"
  printf '<!doctype html><title>placeholder</title>\n' >"$index"
  echo "placeholder: web/dist/index.html"
fi

ext=""; [[ "$triple" == *windows* ]] && ext=".exe"
sidecar="$ROOT/desktop/src-tauri/binaries/dialga-$triple$ext"
if [[ ! -e "$sidecar" ]]; then
  mkdir -p "$(dirname "$sidecar")"
  printf '#!/bin/sh\nexit 0\n' >"$sidecar"
  chmod +x "$sidecar"
  echo "placeholder: ${sidecar#"$ROOT"/}"
fi
