#!/usr/bin/env bash
# Packages ./dist as the plugin's zip, as Grafana's packaging action does, and writes its SHA-1.
#
#   scripts/package.sh [OUTPUT_DIR]    (default: the repository's root)
#
# It refuses a dist that lacks a backend binary for any platform the plugin ships for.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="$(cd "${1:-$root}" && pwd)"
cd "$root"

id="$(python3 -c 'import json; print(json.load(open("dist/plugin.json"))["id"])')"
version="$(python3 -c 'import json; print(json.load(open("dist/plugin.json"))["info"]["version"])')"
executable="$(python3 -c 'import json; print(json.load(open("dist/plugin.json"))["executable"])')"

for platform in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64.exe; do
  if [ ! -f "dist/${executable}_${platform}" ]; then
    echo "dist/${executable}_${platform} is missing: build the backend for every platform first" >&2
    exit 1
  fi
done
for file in module.js plugin.json README.md CHANGELOG.md LICENSE; do
  if [ ! -f "dist/$file" ]; then
    echo "dist/$file is missing: build the frontend first" >&2
    exit 1
  fi
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp -R dist "$work/$id"
chmod 755 "$work/$id/${executable}_"*
archive="$out/$id-$version.zip"
rm -f "$archive" "$archive.sha1"
(cd "$work" && zip -q -r "$archive" "$id")
if command -v sha1sum >/dev/null 2>&1; then
  sha1sum "$archive" | cut -d' ' -f1 >"$archive.sha1"
else
  shasum -a 1 "$archive" | cut -d' ' -f1 >"$archive.sha1"
fi
echo "$archive"
