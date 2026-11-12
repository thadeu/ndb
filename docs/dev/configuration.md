# Configuration

`ndb.yml` is optional. Without it, the defaults apply, and they depend on the install:

- **Server** (ndb runs as root, or `/var/lib/ndb` exists, as with `--systemd` and the Docker image): data dir `/var/lib/ndb`, unix socket `/run/ndb/ndb.sock` with mode `0660`, no TCP.
- **User** (any other case, such as a laptop): data dir `~/.ndb/data`, unix socket `~/.ndb/ndb.sock`, TCP on `127.0.0.1:7070`. So `ndb serve` with no flags works with no root.

The CLI commands use the same rule to find the daemon.

Without `-config`, the daemon and the CLI look for the file in this order: `NDB_CONFIG`, `./ndb.yml`, `/etc/ndb/ndb.yml`. So in Docker, mounting the file at `/etc/ndb/ndb.yml` is enough: no `command:` is needed. A flag overrides the file, even when empty (`-unix ""` turns the unix listener off).

## Flags

```
ndb [-config ndb.yml] [-data-dir DIR] [-unix PATH] [-tcp ADDR] [-unix-mode 0660] [-token-file FILE] [-cors-origin URL]... [-allowed-host NAME]... [-max-conns N] [-max-watches N] [-log-level info] [-log-format text] [-version]
ndb backup [-config f] [-data-dir d] [-url unix://sock] <dest> [-gzip]
```

| Flag | Meaning |
|---|---|
| `-config` | config file. Default: `NDB_CONFIG`, then `./ndb.yml`, then `/etc/ndb/ndb.yml`. A missing file is fine. |
| `-data-dir` | overrides `data_dir`. |
| `-unix` | overrides `listen.unix`. |
| `-tcp` | overrides `listen.tcp`, for example `10.10.0.1:7070`. |
| `-unix-mode` | overrides `listen.unix_mode`: permission of the socket file, octal. Default `0660` (owner and group). |
| `-token-file` | file with the bearer token that the TCP listener requires. See [Authentication](#authentication). |
| `-cors-origin` | overrides `cors_origins`. Give it once for each origin. |
| `-allowed-host` | overrides `allowed_hosts`. Give it once for each name. |
| `-max-conns` | overrides `max_conns`. |
| `-max-watches` | overrides `max_watches`. |
| `-log-level` | overrides `NDB_LOG_LEVEL` and `log_level`: `debug`, `info`, `warn`, `error` or `quiet`. See [Logs](#logs). |
| `-log-format` | overrides `NDB_LOG_FORMAT` and `log_format`: `text` or `json`. |
| `-version` | print the version and exit. |

The daemon stops cleanly on SIGTERM and SIGINT: it stops new requests, waits up to 30 s for running ones, then closes the collections. Use a stop timeout above 30 s.

## ndb.yml

```yaml
data_dir: /var/lib/ndb
listen:
  unix: /run/ndb/ndb.sock
  unix_mode: "0660"        # default; owner and group can connect
  tcp: 10.10.0.1:7070
memory_limit: 1GiB
write_buffer: 32MiB
group_limit: 256MiB
blob_max_size: 1GiB
allow_implicit: true
log_level: info            # debug, info, warn, error, quiet
log_format: text           # text or json
log_body_max: 2048         # bytes of a body in a log line; 0 = no bodies
slow_ms: 1000          # a request over this is a warn line; 0 = off

collections:
  calls:
    write: direct
    path: /data/fsw-esl/calls/**/*.ndjson
    time: ts
    indexes: [status, dnis]
    unique: [call_id]

  outbox:
    write: engine
    time: created_at
    indexes: [status]
    durability: strict

  sessions:
    write: engine
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
| `memory_limit` | Go heap soft limit (`GOMEMLIMIT`). Default 384 MiB: with it the daemon stays under about 512 MB of RSS. The heap grows toward the limit before the GC works hard, so a higher limit is also a higher RSS: in `ndb bench`, 512 MiB loaded 6% faster and read at the same speed, with 125 to 180 MB more RSS in every phase. Raise it for very large aggregations or bulk loads. |
| `read_cache` | memory for the decoded row groups of the segments, shared by every query (each group holds 1 024 documents). Default 32 MiB. In `ndb bench`, 64 MiB read at the same speed with 40 to 80 MB more RSS. |
| `write_buffer` | memory for the documents not sealed yet, shared by every collection and partition. Over it, the largest memtable is written to a segment. Default 32 MiB. |
| `group_limit` | memory limit of one aggregate group table. Over it: `group_limit_exceeded`. |
| `blob_max_size` | largest blob. Default 1 GiB. |
| `allow_implicit` | `false` forbids creating a collection by the first `insert`. Default `true`. |
| `pitr` | the [point in time recovery](pitr.md) window of every engine collection: `off`, or a duration up to `7d`. Default `1d`. |
| `idle_close` | how long an engine collection goes without a call before it is closed: its memtable is sealed, and its files, memory and disk reserve are given back. The next call opens it again (a few ms). A collection with a `_watch` consumer stays open. `off` keeps every collection open. Default `10m`. |
| `log_level` | `debug`, `info`, `warn`, `error` or `quiet` (nothing at all, errors too). Default `info`. See [Logs](#logs). |
| `log_format` | `text` (key=value) or `json` (one object per line). Default `text`. |
| `log_body_max` | the largest body or document in a log line, in bytes. A longer one is cut and ends with `...[cut, N bytes]`. `0` logs no bodies. Default `2048`. |
| `slow_ms` | a request that takes longer is a `warn` line. `0` turns it off. Default `1000`. |
| `cors_origins` | browser origins that may call the daemon directly, such as `https://app.example.com` (scheme, host, optional port; no path). Default: none, so each request with an `Origin` header is `403 forbidden`. See [Security](security.md#browser-attacks-csrf-and-dns-rebinding). |
| `allowed_hosts` | `Host` names that a TCP request may use when no token is configured, more than an IP, `localhost` and a name with no dot. Default: none. A `Host` that is not allowed is `403 forbidden` (DNS rebinding guard). |
| `trust_proxy` | `true` takes the client IP of the token lockout from the last `X-Forwarded-For` hop. Set it only behind a proxy. Default `false`. |
| `auth_failures` | failed token checks of one IP in `auth_window` before a lockout. `0` turns the lockout off. Default `10`. |
| `auth_window` | the time in which the failures count. Default `1m`. |
| `auth_lockout` | how long a locked-out IP gets `429 too_many_requests`. Default `5m`. |
| `max_conns` | the most open TCP connections. Over it, a new connection waits. `0` = no limit (default). |
| `max_watches` | the most open `_watch` streams. Over it: `429 too_many_requests`. `0` = no limit (default). |

### Document id

Every collection has `_id`, the primary key. It is not configurable.

- **Engine mode:** an insert without `_id` gets a generated one: a UUIDv7 such as `0192f3a4-7c1e-7b2a-9f3d-1a2b3c4d5e6f`. Its text order is its creation order, so `sort: {"_id": 1}` is insertion order. An insert with `_id` keeps it: any non-empty string, such as `"user:42"`. `_id` is always in the stored document and cannot change (`immutable_id`).
- **Direct mode:** a line with `_id` is a document with that id; a later line with the same `_id` is its next version. A line without `_id` is a document without `_id`: `find`, `aggregate` and `watch` see it, but the `/{_id}` routes cannot reach it. It is never updated. To give it a stable id, write `_id` in the line (`ndb.NewID()`, `Ndb.new_id`, `newId()` make the same UUIDv7).
- A natural key (`email`, `call_id`, `sku`) is a `unique` field, not the id.
- `id` in a definition is `400 catalog_invalid`: use `unique: [field]`, or name the field `_id`.

### Collection keys

| Key | Meaning |
|---|---|
| `write` | `engine` (default): only ndb writes, through its WAL. `direct`: apps append to the files in `path`; ndb only reads. |
| `path` | glob of source files. Direct mode only. `**` is allowed. A relative glob such as `./logs/*.ndjson` starts at the folder of `ndb.yml`, not at the folder where you start the daemon. |
| `unique` | fields whose value can belong to one live document only (a natural key such as `email` or `call_id`). Dot paths allowed. A duplicate is `409 unique_violation`. An entry with commas is a compound key: `account_id,email` allows one document per pair. |
| `encrypted` | top-level fields that the daemon encrypts before the WAL. See [Encrypted fields](security.md#encrypted-fields). |
| `blind` | encrypted fields that a filter finds by value, with their normalization. See [Blind fields](security.md#blind-fields). |
| `time` | time field. Used for zone maps, `$dateTrunc`, TTL and retention. |
| `indexes` | fields with a secondary index (dot paths allowed). |
| `durability` | `strict` or `relaxed`. Engine mode only. See below. |
| `wal_retention` | engine mode: delete a WAL file once it is older than this, the checkpoint is past it, and no `watch` consumer still needs it. Default: keep forever. See [operations.md](operations.md#compaction-retention-and-ttl). |
| `retention` | hide and drop derived data older than this (by `time`). The source NDJSON is never deleted. |
| `ttl` | each document expires this long after its `time` field. Needs `time`. |
| `expires_field` | each document expires at the time in this field (RFC 3339 or unix seconds). No field: never expires. |
| `max_line_size` | largest line or request body. Default 1 MiB, maximum 16 MiB. |
| `blob_threshold` | engine mode: string values over this size go to the blob store. Default off. |
| `pitr` | engine mode: the [PITR](pitr.md) window of this collection (`off`, `6h`, at most `7d`). Default: the daemon `pitr`. A partition uses the window of its logical collection. |
| `partition_by` | `day`, `week`, `month` or `year`: split the collection into one partition per period of `time` (`calls-2026-11`); with `retention`, old partitions are dropped whole. See [Partitioned collections](http/collections.md#partitioned-collections). |

Durations use Go syntax (`90m`, `1h30m`) plus `d` and `w` (`24h`, `30d`, `2w`).

## TTL like Redis

A collection with `expires_field` (engine mode) expires documents one by one, like `SET key value EX` in Redis. Reads hide an expired document at once; a sweeper deletes it within about 10 seconds; `watch` shows the delete with `"_expired": true`.

```yaml
collections:
  sessions:
    expires_field: exp
```

| Redis | ndb | Notes |
|---|---|---|
| `SET k v EX 3600` | `insert` with `"expire_in": "1h"` (or `3600`) | also on `update` |
| `EXPIRE k 1800` | `POST /sessions/_expire/{_id} {"in":"30m"}` | `"at": "<RFC 3339>"` is `EXPIREAT` |
| `PERSIST k` | `POST /sessions/_persist/{_id}` | |
| `TTL k` | `GET /sessions/_ttl/{_id}` | `-1` = never expires; `404` = missing or expired |

- `expire_in` wins over a value of `expires_field` that the same call carries.
- `ttl: 24h` + `time` (every document expires a fixed time after its `time` field) is a derived expiry: `expire` and `persist` fail with `ttl_derived`.
- Each call is a normal write: new version, WAL record, `watch` event, and a `_v` filter works.
- CLI: `ndb expire sessions s1 30m`, `ndb persist sessions s1`, `ndb ttl sessions s1`.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/sessions/_bulk -d '{"docs":[{"_id":"s1"}],"expire_in":"1h"}'
curl --unix-socket /run/ndb/ndb.sock http://ndb/sessions/_ttl/s1
```

```go
sessions.InsertWith(ctx, []Session{{ID: "s1"}}, ndb.ExpireIn(time.Hour))
sessions.Expire(ctx, "s1", 30*time.Minute)
left, expires, err := sessions.TTL(ctx, "s1")
sessions.Persist(ctx, "s1")
```

```ruby
client.insert("sessions", [{ _id: "s1" }], expire_in: 3600)
client.expire("sessions", "s1", in: 1800)
client.ttl("sessions", "s1")   # 1799, -1, or nil when missing
client.persist("sessions", "s1")

Session.create!({ id: "s1" }, expire_in: 1.hour)   # ndb-rails
session.expire(30.minutes)
session.ttl
session.persist_ttl!
```

```ts
await sessions.insert({ _id: 's1' }, { expireIn: '1h' })
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

## Logs

The daemon writes its logs to stderr. The level comes from, first to last: the `-log-level` flag, `NDB_LOG_LEVEL`, `log_level` of `ndb.yml`, then `info`. The format follows the same order (`-log-format`, `NDB_LOG_FORMAT`, `log_format`, `text`). A bad value stops the start with an error that names its source.

```sh
ndb serve -log-level debug -log-format json
NDB_LOG_LEVEL=warn ndb serve
```

`PUT /_log` changes the level, `log_body_max` and `slow_ms` at run time, until the daemon stops. `PUT /_cluster/settings` changes them and stores them in the data dir: they hold across a restart, unless a flag, the env or `ndb.yml` sets the same one ([System](http/system.md#put-cluster-settings)). What each level writes, the size guard and rotation: [Operations, Logs](operations.md#logs).

## Encryption key

`NDB_ENCRYPTION_KEY` (64 hex characters) gives the encryption key of the data dir. Without it the daemon makes one at the first start and keeps it in the data dir. Never in `ndb.yml`. See [The encryption key](security.md#the-encryption-key).

```sh
NDB_ENCRYPTION_KEY=$(cat /run/secrets/ndb_key) ndb serve
```

## Authentication

`-token-file /etc/ndb/token` makes the TCP listener require `Authorization: Bearer <token>` on every request, `/_health` and `/_metrics` included. A missing or wrong token is `401 unauthorized`. The file holds one token; ndb trims the spaces around it, and an empty file stops the start. The token is compared in constant time.

The unix socket never asks for a token: file permissions guard it (`listen.unix_mode`, default `0660`). `POST /_backup` stays unix-only.

The token is defense in depth. Keep the listener on a private address (for example WireGuard) as well. SDKs take a `token` option or `NDB_TOKEN`; see [clients.md](clients.md).

Ten failed token checks from one IP in one minute lock that IP out for five minutes (`429 too_many_requests`). The browser guards (`cors_origins`, `allowed_hosts`), the lockout and the limits are on [Security](security.md).

```bash
head -c 32 /dev/urandom | base64 > /etc/ndb/token
chown ndb:ndb /etc/ndb/token && chmod 600 /etc/ndb/token
ndb -config /etc/ndb/ndb.yml -token-file /etc/ndb/token
```

### Scoped tokens

`tokens:` in `ndb.yml` gives each app its own token, limited to some collections. The token of `-token-file` keeps every right; both can be set.

```yaml
tokens:
  - name: esl
    file: /etc/ndb/esl.token      # relative paths start at the folder of ndb.yml
    write: [outbox, "calls*"]
  - name: dashboards
    file: /etc/ndb/dash.token
    read: ["calls*"]
  - name: ops
    file: /etc/ndb/ops.token
    admin: ["*"]
  - name: kyc
    file: /etc/ndb/kyc.token
    read: [users]
    decrypt: [users]              # reads the encrypted fields as plain values, with ?decrypt=1
```

| Level | Routes |
|---|---|
| `read` | `GET /{coll}`, `_search`, `_count`, `_aggregate`, `_watch`, `GET _doc/{id}`, `_ttl`, `_stats`, `_quarantine`; the system reads (`/_health`, `/_metrics`, `/_stats`, `/_collections`, `/_size`, `/_pitr`, `GET /_blobs/{ref}`) with any read right |
| `write` | `POST _doc`, `_bulk`, `_update`, `_update_by_query`, `_delete_by_query`, `DELETE _doc/{id}`, `_expire`, `_persist`, `_ack`; `POST /_blobs` with any write right; and all of `read` |
| `decrypt` | read the plain value of the [encrypted fields](security.md#encrypted-fields). Not part of `admin`: give it on its own. A read gets the plain value with this right and `?decrypt=1`; else `"ndb:e1:k1:..."` |
| `admin` | `PUT /{coll}` (declare), `DELETE /{coll}` (drop), `_truncate`, `_compact`, `_indexes`; and all of `write`. `GET` and `PUT /_log` need admin of `*` |

- Each list holds collection names or globs: `calls*` covers `calls` and its partitions (`calls-2026-11`), `*` covers every collection. A pattern read (`/logs-2026-*/_search`) needs a glob that covers the whole pattern (`logs*` or `*`).
- The system reads do not filter by scope: a token with any read right sees every collection name in `/_collections`, `/_stats`, `/_size` and `/_pitr`.
- An unknown token is `401 unauthorized`; a token without the right is `403 forbidden`, with the token name in the message.
- `name` and `file` are required, names are unique, and a token needs at least one list. A missing or empty file stops the start.
- An insert (`_doc`, `_bulk`) to a collection that does not exist creates it (with `allow_implicit`): the write right is enough. The other writes (`_update/{id}` with `upsert` too, `_expire`, `_persist`) do not create it: they answer `404 unknown_collection`.

## TLS

ndb serves plain HTTP. For TLS, put a proxy in front of the TCP listener; Caddy gets and renews the certificate:

```
ndb.example.com {
  reverse_proxy 127.0.0.1:7070
}
```

Keep `listen.tcp` on `127.0.0.1` (or a private address), and give the apps `NDB_URL=https://ndb.example.com` with their token. The SDKs take `https://host[:port]`; see [clients.md](clients.md).

## Code and file together

An app can also declare its collection from code (`ensure_collection`: the SDKs do it on boot). Merge rules: `indexes` are united; a different `write`, `path` or `time` is `catalog_conflict`; for `durability` and `retention`, `ndb.yml` wins.

## Durability

| Mode | Behavior |
|---|---|
| `strict` (default) | A write is answered after the WAL is fsynced. Concurrent writes share one fsync (group commit). A crash loses nothing that was answered. |
| `relaxed` | A write is answered after `write()`. The fsync runs at most 200 ms after the first write it covers. A crash can lose the last 200 ms, but never corrupts data. |

If an fsync fails, the daemon exits. It does not retry: after a failed fsync, Linux can drop the dirty pages. The start-up recovery then makes the state right.

In direct mode, durability is the job of the app that appends the file.
