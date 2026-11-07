#!/usr/bin/env bash
# Expiry like Redis: expire_in on insert, ttl, expire, persist. An expired document is hidden at once.
set -euo pipefail

. "$(dirname "$0")/../lib/ndb.sh"

# region declare
ndb PUT /sessions '{"time":"created_at","expires_field":"expires_at"}' >/dev/null
# endregion declare

for id in s-short s-long s-keep; do
	ndb DELETE "/sessions/_doc/$id" >/dev/null
done

ttl() {
	ndb GET "/sessions/_ttl/$1" | jq -r '.ttl'
}

# region insert
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)

ndb POST /sessions/_bulk "{\"docs\":[{\"_id\":\"s-short\",\"created_at\":\"$now\"}],\"expire_in\":2}" >/dev/null
ndb POST /sessions/_bulk "{\"docs\":[{\"_id\":\"s-long\",\"created_at\":\"$now\"},{\"_id\":\"s-keep\",\"created_at\":\"$now\"}],\"expire_in\":\"1h\"}" >/dev/null

echo "insert s-short (expire_in 2s), s-long and s-keep (expire_in 1h)"
# endregion insert

# region ttl
echo "ttl s-short: $(ttl s-short)s, s-long: $(ttl s-long)s"
# endregion ttl

# region extend
ndb POST /sessions/_expire/s-long '{"in":"30m"}' >/dev/null

echo "expire s-long 30m -> ttl $(ttl s-long)s"
# endregion extend

# region persist
ndb POST /sessions/_persist/s-keep >/dev/null

if [[ "$(ttl s-keep)" == "-1" ]]; then
	echo "persist s-keep -> ttl -1 (never expires)"
fi
# endregion persist

echo "waiting 3s..."
sleep 3

# region after
alive=$(ndb POST /sessions/_search '{}' | jq -r 'map(._id) | join(", ")')

echo "live sessions: $(ndb POST /sessions/_count '{}' | jq -r '.count') ($alive)"

if [[ "$(ndb_status GET /sessions/_ttl/s-short)" == "404" ]]; then
	echo "ttl s-short: gone"
fi
# endregion after
