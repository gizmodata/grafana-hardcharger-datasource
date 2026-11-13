#!/usr/bin/env bash
# Prints the path of a hardcharger-server binary for the live tests, downloading it on first use.
#
#   export HARDCHARGER_SERVER_BINARY="$(scripts/hardcharger-server.sh)"
#
# The binary comes from the `hardcharger` Python package, installed into a virtual environment of
# its own (never into whatever environment happens to be active): $HARDCHARGER_VENV, or
# .venv-hardcharger in the repository. HARDCHARGER_VERSION picks the server version.
set -euo pipefail

version="${HARDCHARGER_VERSION:-2.12.0}"
root="$(cd "$(dirname "$0")/.." && pwd)"
venv="${HARDCHARGER_VENV:-$root/.venv-hardcharger}"

if [ ! -x "$venv/bin/python" ]; then
  python3 -m venv "$venv" >&2
fi
if ! "$venv/bin/python" -c "import hardcharger" >/dev/null 2>&1; then
  "$venv/bin/python" -m pip install --quiet --disable-pip-version-check "hardcharger==$version" >&2
fi

# GitHub's release downloads answer 5xx now and then: try a few times before giving up.
attempt=1
while true; do
  if binary="$("$venv/bin/python" -m hardcharger download --version "$version")"; then
    break
  fi
  if [ "$attempt" -ge 5 ]; then
    echo "hardcharger-server $version could not be downloaded" >&2
    exit 1
  fi
  echo "download failed (attempt $attempt of 5)" >&2
  sleep $((attempt * 15))
  attempt=$((attempt + 1))
done

if [ ! -x "$binary" ]; then
  echo "no executable at $binary" >&2
  exit 1
fi
echo "$binary"
