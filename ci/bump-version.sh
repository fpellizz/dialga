#!/usr/bin/env bash
#
# ci/bump-version.sh
#
# Sets the release version in every file that declares it, so the deb/rpm,
# the desktop crate and the pnpm workspace agree with the tag:
#   desktop/src-tauri/tauri.conf.json  "version": what the packages carry,
#                                      compared with the tag by the pipelines
#   desktop/src-tauri/Cargo.toml       [package] version
#   desktop/src-tauri/Cargo.lock       the entry of that package (the CI
#                                      builds with --locked)
#   desktop/package.json, web/package.json
# The engine has no version file: ci/build-engine.sh stamps it from the tag.
#
# Usage:
#   ci/bump-version.sh X.Y.Z           update every file, then verify them
#   ci/bump-version.sh --check X.Y.Z   verify only: exit 1 on any mismatch
# A leading "v" is accepted (ci/bump-version.sh --check "$BITBUCKET_TAG").
#
# All or nothing: the new contents are written to temporary files and
# checked first; the real files change only if every one is right. A
# package.json or Cargo.toml with a version that is not in FILES makes the
# script fail, so a new workspace package cannot be left behind.
#
# Plain bash, sed and awk: it runs in every CI image (rust:1 has no node).

set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT

readonly TAURI_CONF="desktop/src-tauri/tauri.conf.json"
readonly CARGO_TOML="desktop/src-tauri/Cargo.toml"
readonly CARGO_LOCK="desktop/src-tauri/Cargo.lock"
readonly FILES=(
  "$TAURI_CONF"
  "$CARGO_TOML"
  "$CARGO_LOCK"
  "desktop/package.json"
  "web/package.json"
)

die() { echo "bump-version: $*" >&2; exit 1; }

usage() { die "usage: $0 [--check] X.Y.Z"; }

# MAJOR.MINOR.PATCH only: a pre-release suffix ("1.0.0-rc.1") is not a valid
# rpm version and sorts after the release in dpkg.
check_version() {
  [[ "$1" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] ||
    die "invalid version '$1': expected MAJOR.MINOR.PATCH (e.g. 0.2.0)"
}

# Top-level "version" of a JSON file formatted with two-space indentation
# (the only "version" key at that depth); exactly one, or fail.
json_get() {  # <file>
  local lines
  lines=$(grep -E '^  "version": "[^"]*",?$' "$1" || true)
  [[ -n "$lines" && $(wc -l <<<"$lines") -eq 1 ]] ||
    die "${1#"$ROOT"/}: expected exactly one top-level \"version\" line"
  sed -E 's/^  "version": "([^"]*)",?$/\1/' <<<"$lines"
}

json_set() {  # <file> <version> > new content
  sed -E "s/^(  \"version\": \")[^\"]*(\",?)$/\1$2\2/" "$1"
}

# "version" of the [package] table of a Cargo.toml.
toml_get() {  # <file>
  awk '
    /^\[/ { in_pkg = ($0 == "[package]") }
    in_pkg && /^version = "[^"]*"$/ { sub(/^version = "/, ""); sub(/"$/, ""); print; n++ }
    END { exit n == 1 ? 0 : 1 }
  ' "$1" || die "${1#"$ROOT"/}: expected exactly one version in [package]"
}

toml_set() {  # <file> <version> > new content
  awk -v v="$2" '
    /^\[/ { in_pkg = ($0 == "[package]") }
    in_pkg && /^version = "[^"]*"$/ { print "version = \"" v "\""; next }
    { print }
  ' "$1"
}

toml_name() {  # <file>: "name" of the [package] table
  awk '
    /^\[/ { in_pkg = ($0 == "[package]") }
    in_pkg && /^name = "[^"]*"$/ { sub(/^name = "/, ""); sub(/"$/, ""); print; exit }
  ' "$1"
}

# "version" of one [[package]] entry of a Cargo.lock, found by name.
lock_get() {  # <file> <package name>
  awk -v name="$2" '
    /^\[\[package\]\]$/ { in_pkg = 0 }
    $0 == "name = \"" name "\"" { in_pkg = 1; next }
    in_pkg && /^version = "[^"]*"$/ { sub(/^version = "/, ""); sub(/"$/, ""); print; n++; in_pkg = 0 }
    END { exit n == 1 ? 0 : 1 }
  ' "$1" || die "${1#"$ROOT"/}: expected exactly one [[package]] named '$2'"
}

lock_set() {  # <file> <package name> <version> > new content
  awk -v name="$2" -v v="$3" '
    /^\[\[package\]\]$/ { in_pkg = 0 }
    $0 == "name = \"" name "\"" { in_pkg = 1; print; next }
    in_pkg && /^version = "[^"]*"$/ { print "version = \"" v "\""; in_pkg = 0; next }
    { print }
  ' "$1"
}

crate_name() { toml_name "$ROOT/$CARGO_TOML"; }

get_version() {  # <repo-relative file> [path to read instead]
  local f=$1 path=${2:-$ROOT/$1}
  case "$f" in
    *.json)     json_get "$path" ;;
    *.toml)     toml_get "$path" ;;
    *.lock)     lock_get "$path" "$(crate_name)" ;;
    *)          die "$f: unknown file type" ;;
  esac
}

set_version() {  # <repo-relative file> <version> > new content
  local f=$1 path=$ROOT/$1
  case "$f" in
    *.json)     json_set "$path" "$2" ;;
    *.toml)     toml_set "$path" "$2" ;;
    *.lock)     lock_set "$path" "$(crate_name)" "$2" ;;
    *)          die "$f: unknown file type" ;;
  esac
}

# Every manifest that declares a version must be in FILES.
check_coverage() {
  local f rel known missing=0
  while IFS= read -r f; do
    rel=${f#"$ROOT"/}
    for known in "${FILES[@]}"; do [[ "$rel" == "$known" ]] && continue 2; done
    case "$f" in
      *.json) grep -qE '^  "version":' "$f" || continue ;;
      *.toml) [[ -n "$(toml_get "$f" 2>/dev/null)" ]] || continue ;;
    esac
    echo "bump-version: $rel declares a version but is not handled: add it to FILES" >&2
    missing=1
  done < <(find "$ROOT" \( -name node_modules -o -name target -o -name .git -o -name .pnpm-store -o -name dist \) -prune \
             -o \( -name package.json -o -name Cargo.toml \) -type f -print | sort)
  ((missing == 0)) || exit 1
}

changelog_note() {  # <version>
  local log="$ROOT/CHANGELOG.md"
  [[ -f "$log" ]] || return 0
  grep -qE "^## \[$1\]" "$log" ||
    echo "note: CHANGELOG.md has no '## [$1]' section yet (rename [Non rilasciato] to it, with the date)" >&2
}

cmd_check() {  # <version>
  local f got bad=0
  for f in "${FILES[@]}"; do
    [[ -f "$ROOT/$f" ]] || die "$f not found"
    got=$(get_version "$f")
    if [[ "$got" == "$1" ]]; then
      printf '  ok        %-36s %s\n' "$f" "$got"
    else
      printf '  MISMATCH  %-36s %s (expected %s)\n' "$f" "$got" "$1"
      bad=1
    fi
  done
  ((bad == 0)) || die "version mismatch: run ci/bump-version.sh $1"
  echo "OK: version $1 in ${#FILES[@]} files"
}

TMPS=()
cleanup() { if ((${#TMPS[@]})); then rm -f -- "${TMPS[@]}"; fi; }
trap cleanup EXIT

cmd_bump() {  # <version>
  local new=$1 f i old got changes=""
  for i in "${!FILES[@]}"; do
    f=${FILES[$i]}
    [[ -f "$ROOT/$f" ]] || die "$f not found"
    old=$(get_version "$f")
    TMPS[i]=$(mktemp "$ROOT/$f.XXXXXX")
    set_version "$f" "$new" >"${TMPS[$i]}"
    got=$(get_version "$f" "${TMPS[$i]}")
    [[ "$got" == "$new" ]] || die "$f: rewrite produced '$got', not '$new' (nothing changed)"
    # Only the version line may differ.
    [[ $(diff "$ROOT/$f" "${TMPS[$i]}" | grep -c '^[<>]' || true) -le 2 ]] ||
      die "$f: rewrite touched more than the version line (nothing changed)"
    changes+=$(printf '  %-36s %s -> %s' "$f" "$old" "$new")$'\n'
  done

  # Everything checked: copy over (keeps owner and mode of the originals).
  for i in "${!FILES[@]}"; do
    cat "${TMPS[$i]}" >"$ROOT/${FILES[$i]}"
  done
  cmd_check "$new" >/dev/null
  printf '%s' "$changes"
  echo "OK: version $new in ${#FILES[@]} files"
}

main() {
  local mode=bump
  if [[ "${1:-}" == "--check" ]]; then mode=check; shift; fi
  (($# == 1)) || usage
  local version=${1#v}
  check_version "$version"
  check_coverage
  case "$mode" in
    check) cmd_check "$version" ;;
    bump)  cmd_bump "$version" ;;
  esac
  changelog_note "$version"
}

main "$@"
