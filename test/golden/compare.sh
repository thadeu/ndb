#!/usr/bin/env bash
# Runs the same query in DuckDB CLI and in ndb, and compares the rows.
# DuckDB is a test tool only, never a Go dependency.
#
#   test/golden/compare.sh <data-glob> <duckdb-sql-file> <ndb-result.ndjson>
#
# The SQL reads the data with read_json_auto('<data-glob>') and must ORDER BY
# so that both outputs have the same order.

set -euo pipefail

if ! command -v duckdb >/dev/null; then
  echo "SKIP: duckdb CLI not installed"
  exit 0
fi

glob="$1"
sql_file="$2"
ndb_rows="$3"

sql="$(sed "s#{{data}}#${glob}#g" "$sql_file")"

expected="$(duckdb -json -c "$sql" | jq -c '.[]' | jq -cS .)"
actual="$(jq -cS . "$ndb_rows")"

if diff <(echo "$expected") <(echo "$actual"); then
  echo "OK: results match"
else
  echo "FAIL: results differ"
  exit 1
fi
