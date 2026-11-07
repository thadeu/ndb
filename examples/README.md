# ndb examples

Five runnable examples. The same scenario runs in Go, Ruby, TypeScript and `curl`, and prints the same text. The docs site (the "Examples" tab) imports these files, so the code you read there is the code that runs here.

| Example | What it shows | Languages |
|---|---|---|
| `crud` | insert with an `_id`, get by `_id`, update with a `_v` guard (a stale `_v` matches nothing), delete, count | go, ruby, rails, ts, curl |
| `logs` | direct mode: another process appends NDJSON files (lines without `_id` are found by queries but have no `_doc/{id}` routes); filters, `$regex`, sort, cursor pages, a new line appears | go, ruby, ts, curl |
| `reports` | aggregate: `$match`, `$group` by hour (`$dateTrunc`), `$sum`, `$avg`, `$sort`, filtered count | go, ruby, ts, curl |
| `outbox` | a producer, and a consumer with `watch` + `query` + `ack` that resumes after a restart (at least once); a receipt per event `_id` makes the work safe to repeat | go, ruby, ts, curl |
| `ttl-sessions` | `expires_field`: expired documents are hidden at once | go, ruby, ts, curl |

Not every SDK has every feature. The pages say where one is missing: the TypeScript SDK has no blobs (so there is no blobs example); the Rails example is only for `crud`.

## Run

You need Go, Ruby 3.2 or newer with bundler, Node 24 with pnpm, `curl` and `jq`.

```sh
make run-crud LANG=go        # LANG is go, ruby, ts or curl (crud also has rails)
make run-logs                # no LANG: every language
make check                   # everything; the output must be <example>/output.txt
make stop                    # stop the daemon
make clean                   # stop it and delete its data
```

If no daemon answers, `make` starts `../bin/ndb` (built when missing) with `ndb.yml`: data in `examples/.data`, socket `/tmp/ndb-examples.sock` (a unix socket path must stay under 104 bytes on macOS). It runs with `nice -n 10`.

Every program reads `NDB_URL` and `NDB_TOKEN`. Without them it uses the socket of `ndb.yml`. To use another daemon:

```sh
NDB_URL=tcp://127.0.0.1:7070 NDB_TOKEN=secret make run-crud LANG=ts
```

Every program can run again and again: it removes what it created, or checks first (`reports` seeds its orders once).

## Layout

```
ndb.yml              one config for all examples (collections, socket, data dir)
lib/ndb.sh           curl helper: NDB_URL, NDB_TOKEN, jq-friendly output
<example>/go/        main.go, go.mod (replace => ../../../sdk/go)
<example>/ruby/      main.rb, run with the Gemfile of this folder
<example>/ts/        main.ts, Node 24 runs TypeScript directly
<example>/curl.sh
crud/rails/          Ndb::Record (ndb-rails) as a plain script, no Rails app
logs/append.sh       the "other process" of the direct mode example
```

Code between `region` / `endregion` comments is what the docs show as an excerpt.
