#!/usr/bin/env bash
# CRUD on the users collection: insert, get by _id, update with a _v guard, delete, count.
set -euo pipefail

. "$(dirname "$0")/../lib/ndb.sh"

for id in u1 u2; do
	ndb DELETE "/v1/c/users/$id" >/dev/null
done

# region insert
results=$(ndb POST /v1/c/users/insert '{"insert":[
  {"_id":"u1","email":"ada@example.com","plan":"free"},
  {"_id":"u2","email":"grace@example.com","plan":"free"}
]}')

echo "insert u1, u2 -> $(jq -r '"v\(.results[0].v), v\(.results[1].v)"' <<<"$results")"
# endregion insert

# region get
ada=$(ndb GET /v1/c/users/u1)
version=$(jq -r '._v' <<<"$ada")

echo "get u1: $(jq -r '"\(.email) plan=\(.plan) v\(._v)"' <<<"$ada")"
# endregion get

# region update
saved=$(ndb POST /v1/c/users/u1/update "{\"_v\":$version,\"\$set\":{\"plan\":\"pro\"}}")

echo "update u1 plan=pro (_v $version) -> v$(jq -r '.doc._v' <<<"$saved")"
# endregion update

# region conflict
stale=$(ndb POST /v1/c/users/u1/update "{\"_v\":$version,\"\$set\":{\"plan\":\"team\"}}")

if [[ "$(jq -r '.matched' <<<"$stale")" == "0" ]]; then
	echo "update u1 plan=team (_v $version) -> matched 0"

	fresh=$(ndb GET /v1/c/users/u1 | jq -r '._v')
	retried=$(ndb POST /v1/c/users/u1/update "{\"_v\":$fresh,\"\$set\":{\"plan\":\"team\"}}")

	echo "reload u1: v$fresh, retry with _v $fresh -> v$(jq -r '.doc._v' <<<"$retried")"
fi
# endregion conflict

# region delete
ndb DELETE /v1/c/users/u2 >/dev/null

echo "delete u2"
# endregion delete

# region count
total=$(ndb_header X-Ndb-Total POST /v1/c/users/find '{"count":true,"limit":1}')
on_team=$(ndb_header X-Ndb-Total POST /v1/c/users/find '{"where":{"plan":"team"},"count":true,"limit":1}')

echo "count: $total user(s), $on_team on plan team"
# endregion count
