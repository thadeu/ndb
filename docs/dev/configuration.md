# Configuration

`ndb.yml` is optional. Without it, the defaults apply: data dir `/var/lib/ndb`, unix socket `/run/ndb.sock`, no TCP. A flag overrides the file, even when empty (`-unix ""` turns the unix listener off).

## Flags

```
ndb [-config ndb.yml] [-data-dir DIR] [-unix PATH] [-tcp ADDR] [-unix-mode 0660] [-token-file FILE] [-version]
ndb backup [-config f] [-data-dir d] [-unix sock] <dest>
```

| Flag | Meaning |
|---|---|
| `-config` | config file. Default `ndb.yml` in the working dir. A missing file is fine. |
| `-data-dir` | overrides `data_dir`. |
| `-unix` | overrides `listen.unix`. |
| `-tcp` | overrides `listen.tcp`, for example `10.10.0.1:7070`. |
| `-unix-mode` | permission of the socket file, octal (`0660`). Default: the process umask. |
| `-token-file` | file with the bearer token that the TCP listener requires. See [Authentication](#authentication). |
| `-version` | print the version and exit. |

The daemon stops cleanly on SIGTERM and SIGINT: it stops new requests, waits up to 30 s for running ones, then closes the collections. Use a stop timeout above 30 s.

## ndb.yml

```yaml
data_dir: /var/lib/ndb
listen:
  unix: /run/ndb/ndb.sock
  tcp: 10.10.0.1:7070
memory_limit: 1GiB
group_limit: 256MiB
blob_max_size: 1GiB
allow_implicit: true

collections:
  calls:
    write: direct
    path: /data/fsw-esl/calls/**/*.ndjson
    id: call_id
    time: ts
    indexes: [status, dnis]

  outbox:
    write: engine
    id: event_id
    time: created_at
    indexes: [status]
    durability: strict

  sessions:
    write: engine
    id: sid
    time: created_at
    ttl: 24h
```

An unknown key is an error at start (the file is read in strict mode). Sizes are `512`, `64KiB`, `2MiB`, `1GiB` (1024-based).

### Daemon keys

| Key | Meaning |
|---|---|
| `data_dir` | where all data lives. Never put it on NFS, EFS or another network volume. A relative path starts at the folder of `ndb.yml`. |
| `listen.unix` | unix socket path. A relative path starts at the folder of `ndb.yml`. |
| `listen.tcp` | TCP address. Bind to a private address only, and set `-token-file`. |
| `memory_limit` | Go heap soft limit (`GOMEMLIMIT`). |
| `group_limit` | memory limit of one aggregate group table. Over it: `group_limit_exceeded`. |
| `blob_max_size` | largest blob. Default 1 GiB. |
| `allow_implicit` | `false` forbids creating a collection by the first `insert`. Default `true`. |

### Collection keys

| Key | Meaning |
|---|---|
| `write` | `engine` (default): only ndb writes, through its WAL. `direct`: apps append to the files in `path`; ndb only reads. |
| `path` | glob of source files. Direct mode only. `**` is allowed. A relative glob such as `./logs/*.ndjson` starts at the folder of `ndb.yml`, not at the folder where you start the daemon. |
| `id` | field that identifies a document. Default `_id`. Direct mode without `id`: `file:offset`. |
| `time` | time field. Used for zone maps, `$dateTrunc`, TTL and retention. |
| `indexes` | fields with a secondary index (dot paths allowed). |
| `unique` | fields with a unique constraint (engine mode). |
| `durability` | `strict` or `relaxed`. Engine mode only. See below. |
| `wal_retention` | engine mode: delete a WAL file once it is older than this, the checkpoint is past it, and no `watch` consumer still needs it. Default: keep forever. See [operations.md](operations.md#compaction-retention-and-ttl). |
| `retention` | hide and drop derived data older than this (by `time`). The source NDJSON is never deleted. |
| `ttl` | each document expires this long after its `time` field. Needs `time`. |
| `expires_field` | each document expires at the time in this field (RFC 3339 or unix seconds). No field: never expires. |
| `max_line_size` | largest line or request body. Default 1 MiB, maximum 16 MiB. |
| `blob_threshold` | engine mode: string values over this size go to the blob store. Default off. |

Durations use Go syntax (`90m`, `1h30m`) plus `d` and `w` (`24h`, `30d`, `2w`).

## TTL like Redis

A collection with `expires_field` (engine mode) expires documents one by one, like `SET key value EX` in Redis. Reads hide an expired document at once; a sweeper deletes it within about 10 seconds; `watch` shows the delete with `"_expired": true`.

```yaml
collections:
  sessions:
    id: sid
    expires_field: exp
```

| Redis | ndb | Notes |
|---|---|---|
| `SET k v EX 3600` | `insert` with `"expire_in": "1h"` (or `3600`) | also on `update` |
| `EXPIRE k 1800` | `POST /v1/c/sessions/expire {"id","in":"30m"}` | `"at": "<RFC 3339>"` is `EXPIREAT` |
| `PERSIST k` | `POST /v1/c/sessions/persist {"id"}` | |
| `TTL k` | `GET /v1/c/sessions/ttl/{id}` | `-1` = never expires; `404` = missing or expired |

- `expire_in` wins over a value of `expires_field` that the same call carries.
- `ttl: 24h` + `time` (every document expires a fixed time after its `time` field) is a derived expiry: `expire` and `persist` fail with `ttl_derived`.
- Each call is a normal write: new version, WAL record, `watch` event, and a `_v` filter works.
- CLI: `ndb expire sessions s1 30m`, `ndb persist sessions s1`, `ndb ttl sessions s1`.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/sessions/insert -d '{"insert":[{"sid":"s1"}],"expire_in":"1h"}'
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/sessions/ttl/s1
```

```go
sessions.InsertWith(ctx, []Session{{ID: "s1"}}, ndb.ExpireIn(time.Hour))
sessions.Expire(ctx, "s1", 30*time.Minute)
left, expires, err := sessions.TTL(ctx, "s1")
sessions.Persist(ctx, "s1")
```

```ruby
client.insert("sessions", [{ sid: "s1" }], expire_in: 3600)
client.expire("sessions", "s1", in: 1800)
client.ttl("sessions", "s1")   # 1799, -1, or nil when missing
client.persist("sessions", "s1")

Session.create!({ sid: "s1" }, expire_in: 1.hour)   # ndb-rails
session.expire(30.minutes)
session.ttl
session.persist_ttl!
```

```ts
await sessions.insert({ sid: 's1' }, { expireIn: '1h' })
await sessions.expire('s1', '30m')
await sessions.ttl('s1') // 1799, -1, or null
await sessions.persist('s1')
```

### Freeing disk

An expired document is deleted in the WAL, but its insert is still in older WAL files, and in older segments until compaction. Space comes back in two steps:

1. **Compaction** drops the expired rows from the segments. It runs by itself, or with `ndb compact <collection>`.
2. **WAL files** are deleted only with `wal_retention`. The default keeps every WAL file forever, so without it the data of expired documents stays on disk (hidden from reads, but present).

For a TTL collection, set `wal_retention` to the longest time a `watch` consumer can be offline, for example:

```yaml
collections:
  sessions:
    expires_field: exp
    wal_retention: 1d
```

A WAL file is never deleted while a stored `watch` position still needs it. See [operations.md](operations.md#compaction-retention-and-ttl).

## Authentication

`-token-file /etc/ndb/token` makes the TCP listener require `Authorization: Bearer <token>` on every request, `/health` and `/metrics` included. A missing or wrong token is `401 unauthorized`. The file holds one token; ndb trims the spaces around it, and an empty file stops the start. The token is compared in constant time.

The unix socket never asks for a token: file permissions guard it (`-unix-mode`). `POST /v1/backup` stays unix-only.

The token is defense in depth. Keep the listener on a private address (for example WireGuard) as well. SDKs take a `token` option or `NDB_TOKEN`; see [clients.md](clients.md).

```bash
head -c 32 /dev/urandom | base64 > /etc/ndb/token
chown ndb:ndb /etc/ndb/token && chmod 600 /etc/ndb/token
ndb -config /etc/ndb/ndb.yml -token-file /etc/ndb/token
```

## Code and file together

An app can also declare its collection from code (`ensure_collection`: the SDKs do it on boot). Merge rules: `indexes` are united; a different `id`, `write`, `path` or `time` is `catalog_conflict`; for `durability` and `retention`, `ndb.yml` wins.

## Durability

| Mode | Behavior |
|---|---|
| `strict` (default) | A write is answered after the WAL is fsynced. Concurrent writes share one fsync (group commit). A crash loses nothing that was answered. |
| `relaxed` | A write is answered after `write()`. A background fsync runs every 200 ms. A crash can lose the last 200 ms, but never corrupts data. |

If an fsync fails, the daemon exits. It does not retry: after a failed fsync, Linux can drop the dirty pages. The start-up recovery then makes the state right.

In direct mode, durability is the job of the app that appends the file.
