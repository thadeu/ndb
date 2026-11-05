#!/usr/bin/env bash
# Shared by every curl.sh. Reads NDB_URL and NDB_TOKEN, like the SDKs do.
#
#   ndb POST /v1/c/users/find '{"where":{"plan":"pro"}}'   prints the answer
#   ndb_status POST /v1/c/users/delete '{...}'              prints only the HTTP status
#   ndb_header X-Ndb-Next POST /v1/c/users/find '{...}'     prints one response header
#
# unix:///path is sent with --unix-socket. tcp://host:port is plain HTTP, and
# NDB_TOKEN becomes a bearer token (the unix socket never needs one).

NDB_URL="${NDB_URL:-unix:///tmp/ndb-examples.sock}"
NDB_BODY="$(mktemp)"
NDB_ARGS=(-sS -H 'Content-Type: application/json')

trap 'rm -f "$NDB_BODY"' EXIT

case "$NDB_URL" in
unix://*)
	NDB_ARGS+=(--unix-socket "${NDB_URL#unix://}")
	NDB_BASE="http://ndb"
	;;
tcp://*)
	NDB_BASE="http://${NDB_URL#tcp://}"
	;;
*)
	echo "NDB_URL must start with unix:// or tcp://" >&2
	exit 1
	;;
esac

if [[ -n "${NDB_TOKEN:-}" ]]; then
	NDB_ARGS+=(-H "Authorization: Bearer $NDB_TOKEN")
fi

ndb() {
	local method="$1" path="$2" body="${3-}"
	local extra=()

	shift $(($# < 3 ? $# : 3))

	if [[ -n "$body" ]]; then
		extra=(-d "$body")
	fi

	curl "${NDB_ARGS[@]}" -X "$method" ${extra[@]+"${extra[@]}"} "$@" "$NDB_BASE$path"
}

# ndb_header NAME METHOD PATH BODY prints one response header, for example X-Ndb-Total.
ndb_header() {
	local name="$1"
	shift

	ndb "$@" -D - -o /dev/null | tr -d '\r' | awk -F': ' -v n="$name" 'tolower($1) == tolower(n) { print $2 }'
}

# The answer body of the last ndb_status call stays in $NDB_BODY.
ndb_status() {
	local method="$1" path="$2" body="${3-}"
	local extra=()

	if [[ -n "$body" ]]; then
		extra=(-d "$body")
	fi

	curl "${NDB_ARGS[@]}" -o "$NDB_BODY" -w '%{http_code}' -X "$method" ${extra[@]+"${extra[@]}"} "$NDB_BASE$path"
}
