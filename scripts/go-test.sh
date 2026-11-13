#!/usr/bin/env bash
# Runs the Go tests (unit and live) and fails unless the live tests really ran: none skipped, and
# at least MIN_LIVE_TESTS of them passed. The live tests need a hardcharger-server binary
# ($HARDCHARGER_SERVER_BINARY, see scripts/hardcharger-server.sh); without one they fail.
#
#   scripts/go-test.sh [extra `go test` arguments]
set -euo pipefail

min_live="${MIN_LIVE_TESTS:-15}"
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
log="$(mktemp)"
trap 'rm -f "$log"' EXIT

status=0
go test -json -count=1 "$@" ./pkg/... >"$log" || status=$?

# The failures and the package results, as text.
python3 - "$log" "$min_live" "$status" <<'PY'
import json
import sys

log, min_live, status = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
passed, failed, skipped = set(), set(), set()
output = {}
for line in open(log, encoding="utf-8", errors="replace"):
    try:
        event = json.loads(line)
    except ValueError:
        continue
    test, action = event.get("Test"), event.get("Action")
    key = (event.get("Package"), test)
    if action == "output":
        output.setdefault(key, []).append(event.get("Output", ""))
    if test is None:
        if action in ("pass", "fail"):
            print(f"{action.upper():5} {event['Package']} ({event.get('Elapsed', 0):.1f}s)")
        continue
    if action == "pass":
        passed.add(key)
    elif action == "fail":
        failed.add(key)
    elif action == "skip":
        skipped.add(key)

for key in sorted(failed, key=str):
    print(f"\n--- FAIL {key[1]}")
    print("".join(line for line in output.get(key, []) if not line.startswith('{"@level"')).rstrip())

top = lambda keys: {key for key in keys if "/" not in key[1]}
live = {key for key in top(passed) if key[1].startswith("TestLive")}
print(f"\n{len(top(passed))} tests passed ({len(live)} live), {len(top(failed))} failed, {len(skipped)} skipped")
problems = []
if status != 0 or failed:
    problems.append("tests failed")
if skipped:
    problems.append("tests were skipped: " + ", ".join(sorted(key[1] for key in skipped)))
if len(live) < min_live:
    problems.append(f"only {len(live)} live tests passed; at least {min_live} must run against a real server")
if problems:
    sys.exit("FAILED: " + "; ".join(problems))
PY
