#!/usr/bin/env bash
# CRUD on the users collection: insert, get by _id, update with a _v guard, delete, count.
set -euo pipefail

. "$(dirname "$0")/../lib/ndb.sh"

for id in u1 u2; do
	ndb DELETE "/users/_doc/$id" >/dev/null
done

# region insert
results=$(ndb POST /users/_bulk '{"docs":[
  {"_id":"u1","email":"ada@example.com","plan":"free"},
  {"_id":"u2","email":"grace@example.com","plan":"free"}
]}')

echo "insert u1, u2 -> $(jq -r '"v\(.results[0]._v), v\(.results[1]._v)"' <<<"$results")"
# endregion insert

# region get
ada=$(ndb GET /users/_doc/u1)
version=$(jq -r '._v' <<<"$ada")

echo "get u1: $(jq -r '"\(.email) plan=\(.plan) v\(._v)"' <<<"$ada")"
# endregion get

# region update
saved=$(ndb POST /users/_update/u1 "{\"query\":{\"_v\":$version},\"update\":{\"\$set\":{\"plan\":\"pro\"}}}")

echo "update u1 plan=pro (_v $version) -> v$(jq -r '.doc._v' <<<"$saved")"
# endregion update

# region conflict
stale=$(ndb POST /users/_update/u1 "{\"query\":{\"_v\":$version},\"update\":{\"\$set\":{\"plan\":\"team\"}}}")

if [[ "$(jq -r '.matched' <<<"$stale")" == "0" ]]; then
	echo "update u1 plan=team (_v $version) -> matched 0"

	fresh=$(ndb GET /users/_doc/u1 | jq -r '._v')
	retried=$(ndb POST /users/_update/u1 "{\"query\":{\"_v\":$fresh},\"update\":{\"\$set\":{\"plan\":\"team\"}}}")

	echo "reload u1: v$fresh, retry with _v $fresh -> v$(jq -r '.doc._v' <<<"$retried")"
fi
# endregion conflict

# region delete
ndb DELETE /users/_doc/u2 >/dev/null

echo "delete u2"
# endregion delete

# region count
total=$(ndb POST /users/_count '{}' | jq -r '.count')
on_team=$(ndb POST /users/_count '{"query":{"plan":"team"}}' | jq -r '.count')

echo "count: $total user(s), $on_team on plan team"
# endregion count
