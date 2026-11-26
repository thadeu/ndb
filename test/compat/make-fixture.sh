#!/usr/bin/env bash
# Makes the compatibility fixture of one release: a data dir written by that
# release and the answers it gave. The Go test TestCompat (internal/server)
# opens the data dir with the code of today and checks the same answers.
#
#   test/compat/make-fixture.sh v1.26.0
#
# Run it once per release that must stay readable. It needs git, go, curl, jq.

set -euo pipefail

version="$1"
root="$(git rev-parse --show-toplevel)"
here="$root/test/compat"
out="$here/$version"
work="$(mktemp -d)"

trap 'kill "${pid:-}" 2>/dev/null || true; git -C "$root" worktree remove --force "$work/src" 2>/dev/null || true; rm -rf "$work"' EXIT

git -C "$root" worktree add --detach "$work/src" "$version" >/dev/null
(cd "$work/src" && go build -o "$work/ndb" ./cmd/ndb)

sock="$work/ndb.sock"
data="$work/data"

"$work/ndb" serve -data-dir "$data" -unix "$sock" -tcp "" -log-level quiet &
pid=$!

for _ in $(seq 100); do
  curl -sf --unix-socket "$sock" http://ndb/_health >/dev/null 2>&1 && break
  sleep 0.1
done

send() {
  local req="$1" method path body

  method="$(jq -r .method <<<"$req")"
  path="$(jq -r .path <<<"$req")"

  body="$(jq -c '.body // empty' <<<"$req")"

  curl -s --unix-socket "$sock" -X "$method" -H "Content-Type: application/json" -w '\n%{http_code}' \
    --data-binary "$body" "http://ndb$path"
}

answers="$work/answers.ndjson"

while IFS= read -r req; do
  reply="$(send "$req")"
  status="$(tail -n1 <<<"$reply")"
  body="$(sed '$d' <<<"$reply")"

  if [[ "$(jq -r .phase <<<"$req")" == "write" ]]; then
    [[ "$status" -lt 300 ]] || { echo "write failed ($status): $req -> $body" >&2; exit 1; }

    continue
  fi

  jq -cn --argjson s "$status" --arg b "$body" '{status: $s, body: ($b | fromjson? // $b)}' >>"$answers"
done < <(jq -c '.[]' "$here/requests.json")

kill -TERM "$pid"
wait "$pid" || true
pid=""

rm -rf "$out"
mkdir -p "$out"
cp -R "$data" "$out/data"
cp "$answers" "$out/answers.ndjson"
rm -f "$out/data/LOCK" "$out/data/"*.lock

echo "fixture: $out ($(du -sh "$out" | cut -f1))"
