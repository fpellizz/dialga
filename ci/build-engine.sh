#!/usr/bin/env bash
#
# ci/build-engine.sh
#
# Cross-compiles the Go engine (engine/cmd/dialga) for the CI
# pipelines, so target list, file naming and ldflags live in one place:
#   - bitbucket-pipelines.yml         step "Engine · cross-build CLI"
#   - .github/workflows/release.yml   jobs "engine" and "desktop" (sidecar)
#
# Usage:
#   ci/build-engine.sh dist <version> <outdir>
#       Every release target, packaged as <app>_<version>_<os>_<arch>.tar.gz
#       (.zip for Windows), plus <outdir>/SHA256SUMS.engine.
#   ci/build-engine.sh sidecar <version> <rust-target-triple>
#       One binary named the way Tauri's externalBin expects it:
#       desktop/src-tauri/binaries/dialga-<triple>[.exe]
#
# Contract with the engine: package main declares `var version = "dev"`,
# overridden here with -ldflags -X. Pure Go (CGO_ENABLED=0), so every
# target builds from any host.

set -Eeuo pipefail

readonly APP="dialga"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT
readonly ENGINE_DIR="$ROOT/engine"
readonly MAIN_PKG="./cmd/$APP"

# GOOS/GOARCH/Rust target triple (the triple names the Tauri sidecar).
readonly TARGETS=(
  "linux/amd64/x86_64-unknown-linux-gnu"
  "linux/arm64/aarch64-unknown-linux-gnu"
  "windows/amd64/x86_64-pc-windows-msvc"
  "windows/arm64/aarch64-pc-windows-msvc"
  "darwin/amd64/x86_64-apple-darwin"
  "darwin/arm64/aarch64-apple-darwin"
)

die() { echo "build-engine: $*" >&2; exit 1; }

usage() {
  die "usage: $0 dist <version> <outdir> | sidecar <version> <rust-target-triple>"
}

# The version ends up in -ldflags and in file names: keep it boring.
check_version() {
  [[ "$1" =~ ^[0-9A-Za-z][0-9A-Za-z.+_-]*$ ]] || die "invalid version: '$1'"
}

build_one() {  # <goos> <goarch> <version> <output path>
  local goos=$1 goarch=$2 version=$3 out=$4
  echo "  -> $goos/$goarch: ${out#"$ROOT"/}"
  (cd "$ENGINE_DIR" &&
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$out" "$MAIN_PKG")
}

sha256() {  # sha256sum on Linux and Git Bash, shasum on macOS
  if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

cmd_dist() {
  local version=$1 outdir=$2 work entry goos goarch ext name f
  command -v zip >/dev/null || die "zip not found (needed for the Windows archives)"
  mkdir -p "$outdir"
  outdir=$(cd "$outdir" && pwd)
  work=$(mktemp -d)
  # shellcheck disable=SC2064  # expand $work now, on purpose
  trap "rm -rf '$work'" EXIT

  for entry in "${TARGETS[@]}"; do
    IFS=/ read -r goos goarch _ <<<"$entry"
    ext=""; [[ "$goos" == windows ]] && ext=".exe"
    name="${APP}_${version}_${goos}_${goarch}"
    mkdir -p "$work/$name"
    build_one "$goos" "$goarch" "$version" "$work/$name/$APP$ext"
    for f in LICENSE README.md; do
      if [[ -f "$ROOT/$f" ]]; then cp "$ROOT/$f" "$work/$name/"; fi
    done
    if [[ "$goos" == windows ]]; then
      (cd "$work" && zip -qr "$outdir/$name.zip" "$name")
    else
      tar -C "$work" -czf "$outdir/$name.tar.gz" "$name"
    fi
  done

  (cd "$outdir" && sha256 "${APP}_${version}_"* >SHA256SUMS.engine)
  echo "OK: $(wc -l <"$outdir/SHA256SUMS.engine") archives in $outdir"
}

cmd_sidecar() {
  local version=$1 triple=$2 entry goos goarch t ext
  for entry in "${TARGETS[@]}"; do
    IFS=/ read -r goos goarch t <<<"$entry"
    [[ "$t" == "$triple" ]] || continue
    ext=""; [[ "$goos" == windows ]] && ext=".exe"
    mkdir -p "$ROOT/desktop/src-tauri/binaries"
    build_one "$goos" "$goarch" "$version" "$ROOT/desktop/src-tauri/binaries/$APP-$triple$ext"
    return 0
  done
  die "unknown Rust target triple: $triple"
}

main() {
  (($# == 3)) || usage
  [[ -f "$ENGINE_DIR/go.mod" ]] || die "engine/go.mod not found: nothing to build"
  check_version "$2"
  case "$1" in
    dist)    cmd_dist "$2" "$3" ;;
    sidecar) cmd_sidecar "$2" "$3" ;;
    *)       usage ;;
  esac
}

main "$@"
