#!/usr/bin/env bash
# Smoke test: builds ndb, starts it on a temp data dir and unix socket, runs
# each endpoint with curl, then stops the daemon with SIGTERM.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/ndbsmoke.XXXXXX")"
sock="$tmp/ndb.sock"
pid=""

cleanup() {
	if [[ -n "$pid" ]]; then
		kill -TERM "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi

	rm -rf "$tmp"
}

trap cleanup EXIT

fail() {
	echo "smoke: FAIL: $*" >&2
	exit 1
}

# call METHOD PATH [BODY] prints the body; the status goes to $tmp/status.
call() {
	local method="$1" path="$2" body="${3-}"

	curl -s --unix-socket "$sock" -X "$method" -H 'Content-Type: application/json' \
		-d "$body" -o "$tmp/body" -w '%{http_code}' "http://ndb$path" >"$tmp/status"
	cat "$tmp/body"
}

expect() {
	local want="$1" got="$2"

	[[ "$got" == "$want" ]] || fail "status $got, want $want: $(cat "$tmp/body")"
}

(cd "$root" && go build -o "$tmp/ndb" ./cmd/ndb)

"$tmp/ndb" -config "$tmp/absent.yml" -data-dir "$tmp/data" -unix "$sock" 2>"$tmp/daemon.log" &
pid=$!

for _ in $(seq 1 100); do
	if [[ "$(curl -s -o /dev/null -w '%{http_code}' --unix-socket "$sock" http://ndb/health 2>/dev/null || true)" == "200" ]]; then
		break
	fi

	sleep 0.1
done

call GET /health >/dev/null
expect 200 "$(cat "$tmp/status")"

call PUT /v1/c/old '{"id":"post_id"}' >/dev/null
expect 400 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'catalog_invalid'* ]] || fail "the id setting must be catalog_invalid: $(cat "$tmp/body")"

call PUT /v1/c/posts '{"indexes":["status"]}' >/dev/null
expect 200 "$(cat "$tmp/status")"

call POST /v1/c/posts/insert '{"insert":[{"_id":"p1","status":"draft"},{"_id":"p2","status":"live"},{"status":"auto"}]}' >/dev/null
expect 200 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'"_id":"p1"'* ]] || fail "insert did not return p1: $(cat "$tmp/body")"

call POST /v1/c/posts/find '{"where":{"status":"live"}}' >/dev/null
expect 200 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'"p2"'* ]] || fail "find did not return p2"

call GET /v1/c/posts/p1 >/dev/null
expect 200 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'"_id":"p1"'* && "$(cat "$tmp/body")" == *'"_v":1'* ]] || fail "get p1: $(cat "$tmp/body")"

call GET /v1/c/posts/nope >/dev/null
expect 404 "$(cat "$tmp/status")"

call POST /v1/c/posts/update '{"_id":"p1","_v":1,"$set":{"status":"live"}}' >/dev/null
expect 200 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'"matched":1'* ]] || fail "update did not match: $(cat "$tmp/body")"

call POST /v1/c/posts/update '{"_id":"p1","_v":1,"$set":{"status":"gone"}}' >/dev/null
expect 200 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'"matched":0'* ]] || fail "stale update matched: $(cat "$tmp/body")"

call POST /v1/c/posts/p1/update '{"_v":2,"$set":{"status":"by-id"}}' >/dev/null
expect 200 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'"matched":1'* ]] || fail "update by id did not match: $(cat "$tmp/body")"

call DELETE /v1/c/posts/p2 >/dev/null
expect 200 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'"deleted":1'* ]] || fail "delete by id: $(cat "$tmp/body")"

call GET /v1/c/posts/stats >/dev/null
expect 200 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'"rows":2'* ]] || fail "stats: want 2 rows after delete: $(cat "$tmp/body")"

call DELETE /v1/c/posts/indexes/status >/dev/null
expect 200 "$(cat "$tmp/status")"

call GET /v1/c >/dev/null
expect 200 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'"posts"'* ]] || fail "catalog list misses posts"

call POST /v1/c/nope/find '{}' >/dev/null
expect 404 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'unknown_collection'* ]] || fail "unknown collection: wrong code"

call GET /metrics >/dev/null
expect 200 "$(cat "$tmp/status")"
[[ "$(cat "$tmp/body")" == *'ndb_requests_total'* ]] || fail "metrics: no ndb_requests_total"

kill -TERM "$pid"
wait "$pid" || fail "daemon exit status $? on SIGTERM (see $tmp/daemon.log)"
pid=""

[[ ! -S "$sock" ]] || fail "socket left behind after stop"

echo "smoke: ok"
