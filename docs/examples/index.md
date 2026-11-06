# Examples

Five small programs that run for real. Each one does the same work in Go, Ruby, TypeScript and `curl`, and prints the same text. The code on these pages is imported from the files in `examples/`, so what you read is what runs.

| Example | Shows | Languages |
|---|---|---|
| [CRUD](/examples/crud) | insert, get, atomic update by filter, delete, count | Go, Ruby, Rails, TypeScript, curl |
| [Logs (direct mode)](/examples/logs) | files written by other processes: filters, `$regex`, cursor pages | Go, Ruby, TypeScript, curl |
| [Reports (aggregate)](/examples/reports) | `$match`, `$group`, `$dateTrunc` by hour, `$sum`, `$avg`, `$sort` | Go, Ruby, TypeScript, curl |
| [Outbox (watch and ack)](/examples/outbox) | a consumer with a `where` filter that resumes after a restart | Go, Ruby, TypeScript, curl |
| [Expiring sessions](/examples/ttl-sessions) | `expire_in`, `ttl`, `expire`, `persist`: expired documents disappear | Go, Ruby, TypeScript, curl |

## Run them

You need Go, Ruby 3.2 or newer with bundler, Node 24 with pnpm, `curl` and `jq`.

```sh
git clone https://github.com/thadeu/ndb && cd ndb/examples

make run-crud LANG=go      # LANG is go, ruby, ts or curl (crud also has rails)
make run-logs              # no LANG: every language
make check                 # all examples, all languages, output must match
```

If no daemon answers, `make` starts `bin/ndb` with `examples/ndb.yml`: data in `examples/.data`, socket `/tmp/ndb-examples.sock` (a unix socket path must stay under 104 bytes on macOS). Every program reads `NDB_URL` and `NDB_TOKEN`, so this also works with a TCP daemon:

```sh
NDB_URL=tcp://127.0.0.1:7070 NDB_TOKEN=secret make run-crud LANG=ts
```

A program can run again and again. It removes what it created, or it checks first.

## The curl helper

Every `curl.sh` loads one small helper, `examples/lib/ndb.sh`. It reads `NDB_URL` and `NDB_TOKEN`, picks `--unix-socket` or TCP, and adds the bearer token. `jq` formats the answers.

<<< @/examples/lib/ndb.sh{bash}

## What each SDK can do

The examples do not hide a missing feature. Where an SDK lacks one, the page says so.

| Feature | Go | Ruby | Rails | TypeScript | curl |
|---|---|---|---|---|---|
| insert, update, delete, `_v` guard | yes | yes | yes | yes | yes |
| find, sort, cursor pages, count | yes | yes | yes | yes | yes |
| `$regex` | yes | yes (also `Regexp`) | yes | yes | yes |
| aggregate | yes | yes | yes (and `group`/`sum`) | yes | yes |
| `watch` with `where`, `ack` | yes | yes | client only | yes | yes |
| declare `expires_field` from code, `expire_in`, `ttl`, `expire`, `persist` | yes | yes | yes | yes | yes |
| blobs | yes | yes | yes | no | yes |

There is no blobs example because the TypeScript SDK has no blobs.

See also: [Clients](/clients) for the SDK guides, [Query language](/query) for the JSON the programs send.
