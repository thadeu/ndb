#!/usr/bin/env bash
# CRUD on the users collection: insert, get, update with if_version, delete, count.
set -euo pipefail

. "$(dirname "$0")/../lib/ndb.sh"

for id in u1 u2; do
	ndb POST /v1/c/users/delete "{\"delete\":{\"id\":\"$id\"}}" >/dev/null
done

# region insert
results=$(ndb POST /v1/c/users/insert '{"insert":[
  {"user_id":"u1","email":"ada@example.com","plan":"free"},
  {"user_id":"u2","email":"grace@example.com","plan":"free"}
]}')

echo "insert u1, u2 -> $(jq -r '"v\(.results[0].v), v\(.results[1].v)"' <<<"$results")"
# endregion insert

# region get
ada=$(ndb POST /v1/c/users/find '{"where":{"user_id":"u1"},"versions":true}')
version=$(jq -r '._v' <<<"$ada")

echo "get u1: $(jq -r '"\(.email) plan=\(.plan) v\(._v)"' <<<"$ada")"
# endregion get

# region update
saved=$(ndb POST /v1/c/users/update "{\"update\":{\"id\":\"u1\",\"set\":{\"plan\":\"pro\"},\"if_version\":$version}}")

echo "update u1 plan=pro (if_version $version) -> v$(jq -r '.v' <<<"$saved")"
# endregion update

# region conflict
status=$(ndb_status POST /v1/c/users/update "{\"update\":{\"id\":\"u1\",\"set\":{\"plan\":\"team\"},\"if_version\":$version}}")

if [[ "$status" == "409" ]]; then
	echo "update u1 plan=team (if_version $version) -> $(jq -r '.error.code' "$NDB_BODY")"

	fresh=$(ndb POST /v1/c/users/find '{"where":{"user_id":"u1"},"versions":true}' | jq -r '._v')
	retried=$(ndb POST /v1/c/users/update "{\"update\":{\"id\":\"u1\",\"set\":{\"plan\":\"team\"},\"if_version\":$fresh}}")

	echo "reload u1: v$fresh, retry with if_version $fresh -> v$(jq -r '.v' <<<"$retried")"
fi
# endregion conflict

# region delete
ndb POST /v1/c/users/delete '{"delete":{"id":"u2"}}' >/dev/null

echo "delete u2"
# endregion delete

# region count
total=$(ndb_header X-Ndb-Total POST /v1/c/users/find '{"count":true,"limit":1}')
on_team=$(ndb_header X-Ndb-Total POST /v1/c/users/find '{"where":{"plan":"team"},"count":true,"limit":1}')

echo "count: $total user(s), $on_team on plan team"
# endregion count
