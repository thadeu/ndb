#!/usr/bin/env bash
# Direct mode: other processes append NDJSON lines to files, ndb only reads them.
# It starts ./append.sh as the "other process".
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"

. "$here/../lib/ndb.sh"

history='"ts":{"$lt":"2026-11-02T00:00:00Z"}'

count() {
	ndb POST /app_logs/_count "{\"query\":{$1}}" | jq -r '.count'
}

# The daemon sees a new line within a moment (file events, with a 5 s poll as the fallback).
wait_for() {
	for _ in $(seq 1 75); do
		if [[ "$(count "$1")" == "$2" ]]; then
			return 0
		fi

		sleep 0.2
	done

	echo "timeout waiting for the daemon to read the files" >&2

	return 1
}

wait_for "$history" 12

echo "history: 12 lines from 2 files"

# region filter
echo "errors, newest first:"

ndb POST /app_logs/_search "{\"query\":{$history,\"level\":\"error\"},\"sort\":{\"ts\":-1}}" |
	jq -r '.[] | "  \(.ts[11:19]) \(.app + "      " | .[0:6]) \(.msg)"'

echo "slow (ms >= 800): $(count "$history,\"ms\":{\"\$gte\":800}")"
# endregion filter

# region regex
echo "payment paths (\$regex): $(count "$history,\"path\":{\"\$regex\":\"^/api/v[0-9]+/payments\"}")"
# endregion regex

# region cursor
headers=$(mktemp)
body="{\"query\":{$history},\"sort\":{\"ts\":1},\"limit\":5}"
pages=()

while :; do
	rows=$(ndb POST /app_logs/_search "$body" -D "$headers" | jq 'length')
	cursor=$(tr -d '\r' <"$headers" | awk -F': ' 'tolower($1) == "x-ndb-next" { print $2 }')

	pages+=("$rows")

	if [[ -z "$cursor" ]]; then
		break
	fi

	body="{\"query\":{$history},\"sort\":{\"ts\":1},\"limit\":5,\"after\":\"$cursor\"}"
done

rm -f "$headers"

echo "pages (limit 5): $(IFS=,; echo "${pages[*]}" | sed 's/,/, /g')"
# endregion cursor

# region append
msg="hello from curl $(date +%s)$RANDOM"
line="{\"ts\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"app\":\"cli\",\"level\":\"info\",\"msg\":\"$msg\"}"

"$here/append.sh" cli 3003 "$line"

echo "append: cli wrote one line"

wait_for "\"msg\":\"$msg\"" 1

echo "the new line is in the collection"

added=$(ndb POST /app_logs/_search "{\"query\":{\"msg\":\"$msg\"}}")

echo "it has no _id, so it has no _doc/{id} routes: _id is \"$(jq -r '.[0]._id // ""' <<<"$added")\""
# endregion append
