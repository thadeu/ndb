#!/usr/bin/env bash
# Expiry like Redis: expire_in on insert, ttl, expire, persist. An expired document is hidden at once.
set -euo pipefail

. "$(dirname "$0")/../lib/ndb.sh"

# region declare
ndb PUT /v1/c/sessions '{"id":"sid","time":"created_at","expires_field":"expires_at"}' >/dev/null
# endregion declare

for id in s-short s-long s-keep; do
	ndb POST /v1/c/sessions/delete "{\"delete\":{\"id\":\"$id\"}}" >/dev/null
done

ttl() {
	ndb GET "/v1/c/sessions/ttl/$1" | jq -r '.ttl'
}

# region insert
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)

ndb POST /v1/c/sessions/insert "{\"insert\":[{\"sid\":\"s-short\",\"created_at\":\"$now\"}],\"expire_in\":2}" >/dev/null
ndb POST /v1/c/sessions/insert "{\"insert\":[{\"sid\":\"s-long\",\"created_at\":\"$now\"},{\"sid\":\"s-keep\",\"created_at\":\"$now\"}],\"expire_in\":\"1h\"}" >/dev/null

echo "insert s-short (expire_in 2s), s-long and s-keep (expire_in 1h)"
# endregion insert

# region ttl
echo "ttl s-short: $(ttl s-short)s, s-long: $(ttl s-long)s"
# endregion ttl

# region extend
ndb POST /v1/c/sessions/expire '{"id":"s-long","in":"30m"}' >/dev/null

echo "expire s-long 30m -> ttl $(ttl s-long)s"
# endregion extend

# region persist
ndb POST /v1/c/sessions/persist '{"id":"s-keep"}' >/dev/null

if [[ "$(ttl s-keep)" == "-1" ]]; then
	echo "persist s-keep -> ttl -1 (never expires)"
fi
# endregion persist

echo "waiting 3s..."
sleep 3

# region after
alive=$(ndb POST /v1/c/sessions/find '{}' | jq -r '.sid' | paste -sd, - | sed 's/,/, /g')

echo "live sessions: $(ndb_header X-Ndb-Total POST /v1/c/sessions/find '{"count":true,"limit":1}') ($alive)"

if [[ "$(ndb_status GET /v1/c/sessions/ttl/s-short)" == "404" ]]; then
	echo "ttl s-short: gone"
fi
# endregion after
