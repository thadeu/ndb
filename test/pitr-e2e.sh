#!/usr/bin/env bash
# End-to-end PITR on a small base: a daemon of its own, the CLI and curl.
# It waits 65 s for the first snapshot of the daemon, then restores across a
# compaction, renames with replace, restores a dropped partitioned
# collection, and checks it all after a kill -9.
#
#   go build -o bin/ndb ./cmd/ndb && test/pitr-e2e.sh
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
NDB=${NDB:-$root/bin/ndb}
W=$(mktemp -d "${TMPDIR:-/tmp}/ndbpitr.XXXXXX")
PID=""
trap '[ -n "$PID" ] && kill -9 $PID 2>/dev/null; rm -rf "$W"' EXIT
SOCK=$W/ndb.sock
export NDB_URL=unix://$SOCK
cat > $W/ndb.yml <<YML
data_dir: $W/data
listen:
  unix: $SOCK
pitr: 1d
collections:
  calls:
    indexes: [status]
  logs:
    time: ts
    partition_by: day
YML

start() { $NDB -config $W/ndb.yml >>$W/daemon.log 2>&1 & PID=$!; for i in $(seq 100); do $NDB health >/dev/null 2>&1 && return; sleep 0.05; done; echo "no daemon"; exit 1; }
c() { curl -s --unix-socket $SOCK "http://ndb$1" "${@:2}"; }
count() { c /$1/_count -d "${2:-{\}}" | sed -E 's/.*"count":([0-9]+).*/\1/'; }
check() { [ "$2" = "$3" ] && echo "ok   $1 = $3" || { echo "FAIL $1 = $2, want $3"; exit 1; }; }

start

docs=$(seq 1 2000 | awk '{printf "{\"_id\":\"c%d\",\"status\":\"s%d\",\"n\":%d}\n", $1, $1%4, $1}' | paste -sd,)
c /calls/_bulk -d "{\"docs\":[$docs]}" >/dev/null
today=$(date -u +%Y-%m-%dT%H:%M:%SZ); yday=$(date -u -d yesterday +%Y-%m-%dT%H:%M:%SZ)
c /logs/_bulk -d "{\"docs\":[{\"ts\":\"$today\"},{\"ts\":\"$today\"},{\"ts\":\"$yday\"}]}" >/dev/null
c /calls/_compact -X POST >/dev/null
check "calls before" "$(count calls)" 2000

echo "waiting 65s for the daemon to take its first snapshot"; sleep 65
check "snapshots of calls" "$(c /calls/_pitr | sed -E 's/.*"snapshots":([0-9]+).*/\1/')" 1
c /calls/_bulk -d '{"docs":[{"_id":"after-snap","status":"s0"}]}' >/dev/null
c /calls/_compact -X POST >/dev/null

sleep 1.2; T1=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ); sleep 0.05
c /calls/_delete_by_query -d '{"query":{"status":"s1"},"many":true}' >/dev/null
check "calls after the mistake" "$(count calls)" 1501
c /calls/_bulk -d '{"docs":[{"_id":"new1","status":"s9"}]}' >/dev/null

$NDB restore calls -at "$T1" -to calls_restored
check "copy at T1 (snapshot + compaction + WAL)" "$(count calls_restored)" 2001
check "copy has no later write" "$(c /calls_restored/_doc/new1 | grep -c not_found)" 1
$NDB pitr

$NDB rename calls_restored calls -replace -yes
check "calls after the swap" "$(count calls)" 2001

sleep 0.05; T2=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
$NDB restore calls -at "$T2" -to calls_check
check "restore of the swapped calls" "$(count calls_check)" 2001

T3=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ); sleep 0.05
c "/logs?confirm=logs" -X DELETE >/dev/null
$NDB restore logs -at "$T3" -to logs_back
check "logs back (partitioned)" "$(count 'logs_back')" 3

c /calls/_restore -d '{"at":"2020-01-01T00:00:00Z","to":"x"}' | grep -q out_of_window && echo "ok   out_of_window"

kill -9 $PID; wait $PID 2>/dev/null || true
start
check "calls after kill -9" "$(count calls)" 2001
check "logs_back after kill -9" "$(count logs_back)" 3
$NDB size
kill $PID; wait $PID 2>/dev/null || true
echo "E2E OK"
