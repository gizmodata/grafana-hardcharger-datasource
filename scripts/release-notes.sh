#!/usr/bin/env bash
# Prints the CHANGELOG.md section of a version: the release notes.
#
#   scripts/release-notes.sh 1.2.3 > release_notes.md
set -euo pipefail

version="${1:?usage: release-notes.sh VERSION}"
root="$(cd "$(dirname "$0")/.." && pwd)"
notes="$(awk -v ver="$version" '
  index($0, "## [" ver "]") == 1 { found = 1; next }
  found && /^## \[/ { exit }
  found { print }
' "$root/CHANGELOG.md")"
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "CHANGELOG.md has no section for $version" >&2
  exit 1
fi
printf '%s\n' "$notes"
