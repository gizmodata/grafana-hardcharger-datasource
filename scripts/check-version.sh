#!/usr/bin/env bash
# Checks that the plugin's version is the same everywhere it is written, and prints it.
#
#   scripts/check-version.sh           package.json, src/plugin.json and CHANGELOG.md agree
#   scripts/check-version.sh v1.2.3    ... and are the version of this tag
#
# src/plugin.json may hold the placeholder %VERSION%, which the build replaces with package.json's
# version (dist/plugin.json, when it exists, is checked too).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
tag="${1:-}"

version="$(python3 -c 'import json; print(json.load(open("package.json"))["version"])')"
fail() { echo "version check failed: $1" >&2; exit 1; }

case "$version" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) fail "package.json version \"$version\" is not X.Y.Z" ;;
esac

source_version="$(python3 -c 'import json; print(json.load(open("src/plugin.json"))["info"]["version"])')"
if [ "$source_version" != "%VERSION%" ] && [ "$source_version" != "$version" ]; then
  fail "src/plugin.json says $source_version, package.json $version"
fi
if [ -f dist/plugin.json ]; then
  built="$(python3 -c 'import json; print(json.load(open("dist/plugin.json"))["info"]["version"])')"
  if [ "$built" != "$version" ]; then
    fail "dist/plugin.json says $built, package.json $version (rebuild the frontend)"
  fi
fi
if ! grep -q "^## \[$version\] - [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$" CHANGELOG.md; then
  fail "CHANGELOG.md has no \"## [$version] - YYYY-MM-DD\" section"
fi
if [ -n "$tag" ] && [ "$tag" != "v$version" ]; then
  fail "the tag is $tag, the version $version (expected the tag v$version)"
fi
echo "$version"
