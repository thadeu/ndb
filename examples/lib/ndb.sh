#!/usr/bin/env bash
# Shared by every curl.sh. Reads NDB_URL and NDB_TOKEN, like the SDKs do.
#
#   ndb POST /users/_search '{"query":{"plan":"pro"}}'      prints the answer
#   ndb_status POST /users/_doc '{...}'                     prints only the HTTP status
#   ndb_header X-Ndb-Next POST /users/_search '{...}'       prints one response header
#
# unix:///path is sent with --unix-socket. tcp://host:port is plain HTTP, and
# NDB_TOKEN becomes a bearer token (the unix socket never needs one).
#
# As in the SDKs, the URL can hold the token and the database:
# tcp://:<token>@host:port/<database>, or unix:///path?db=<database>. The
# token of the URL wins over NDB_TOKEN; percent-encoding is not decoded here.

NDB_URL="${NDB_URL:-unix:///tmp/ndb-examples.sock}"
NDB_BODY="$(mktemp)"
NDB_ARGS=(-sS -H 'Content-Type: application/json')
NDB_DB=""

trap 'rm -f "$NDB_BODY"' EXIT

ndb_address="${NDB_URL%%\?*}"
ndb_query=""

if [[ "$NDB_URL" == *\?* ]]; then
	ndb_query="${NDB_URL#*\?}"
fi

if [[ "&$ndb_query" =~ \&db=([^\&]*) ]]; then
	NDB_DB="${BASH_REMATCH[1]}"
fi

case "$ndb_address" in
unix://*)
	NDB_ARGS+=(--unix-socket "${ndb_address#unix://}")
	NDB_BASE="http://ndb"
	;;
tcp://*)
	ndb_authority="${ndb_address#tcp://}"
	ndb_path=""

	if [[ "$ndb_authority" == */* ]]; then
		ndb_path="${ndb_authority#*/}"
		ndb_authority="${ndb_authority%%/*}"
	fi

	if [[ "$ndb_authority" == *@* ]]; then
		ndb_userinfo="${ndb_authority%@*}"
		ndb_authority="${ndb_authority##*@}"

		if [[ "$ndb_userinfo" == *:* && -n "${ndb_userinfo#*:}" ]]; then
			NDB_TOKEN="${ndb_userinfo#*:}"
		fi
	fi

	ndb_path="${ndb_path%/}"

	if [[ -n "$ndb_path" ]]; then
		NDB_DB="$ndb_path"
	fi

	NDB_BASE="http://$ndb_authority"
	;;
*)
	echo "NDB_URL must start with unix:// or tcp://" >&2
	exit 1
	;;
esac

if [[ -n "${NDB_TOKEN:-}" ]]; then
	NDB_ARGS+=(-H "Authorization: Bearer $NDB_TOKEN")
fi

if [[ -n "$NDB_DB" ]]; then
	NDB_ARGS+=(-H "Ndb-Database: $NDB_DB")
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
