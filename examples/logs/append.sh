#!/usr/bin/env bash
# The "app" of the direct-mode example: a plain process that appends one JSON
# line per event to its own file. It never talks to ndb. ndb reads the files.
#
#   append.sh APP PID 'JSON LINE'   append one line to data/logs/APP-PID.ndjson
#   append.sh --seed                write the history lines, only if the files are missing
#
# Rules for direct mode: one writer per file, one line per write(), end with a newline.
set -euo pipefail

dir="${LOG_DIR:-$(cd "$(dirname "$0")/.." && pwd)/data/logs}"

mkdir -p "$dir"

if [[ "${1-}" == "--seed" ]]; then
	if [[ ! -e "$dir/web-1001.ndjson" ]]; then
		cat >"$dir/web-1001.ndjson" <<'LINES'
{"ts":"2026-11-01T10:00:01Z","app":"web","level":"info","msg":"GET /api/v1/orders","path":"/api/v1/orders","ms":12}
{"ts":"2026-11-01T10:00:05Z","app":"web","level":"info","msg":"GET /api/v1/users","path":"/api/v1/users","ms":8}
{"ts":"2026-11-01T10:01:10Z","app":"web","level":"error","msg":"payment gateway timeout","path":"/api/v2/payments","ms":3000}
{"ts":"2026-11-01T10:02:00Z","app":"web","level":"warn","msg":"slow query","path":"/api/v1/orders","ms":900}
{"ts":"2026-11-01T10:03:30Z","app":"web","level":"info","msg":"GET /health","path":"/health","ms":1}
{"ts":"2026-11-01T10:04:00Z","app":"web","level":"error","msg":"payment declined","path":"/api/v2/payments","ms":220}
{"ts":"2026-11-01T10:05:00Z","app":"web","level":"info","msg":"GET /api/v2/orders","path":"/api/v2/orders","ms":15}
{"ts":"2026-11-01T10:06:00Z","app":"web","level":"error","msg":"db connection reset","path":"/api/v1/orders","ms":40}
LINES
	fi

	if [[ ! -e "$dir/worker-2002.ndjson" ]]; then
		cat >"$dir/worker-2002.ndjson" <<'LINES'
{"ts":"2026-11-01T10:00:30Z","app":"worker","level":"info","msg":"job started","ms":0}
{"ts":"2026-11-01T10:02:30Z","app":"worker","level":"error","msg":"job failed: smtp timeout","ms":5000}
{"ts":"2026-11-01T10:03:00Z","app":"worker","level":"info","msg":"job started","ms":0}
{"ts":"2026-11-01T10:05:30Z","app":"worker","level":"error","msg":"job failed: smtp refused","ms":150}
LINES
	fi

	exit 0
fi

app="$1" pid="$2" line="$3"

printf '%s\n' "$line" >>"$dir/$app-$pid.ndjson"
