# Configuration

`ndb.yml` is optional. Without it, the defaults apply: data dir `/var/lib/ndb`, unix socket `/run/ndb.sock`, no TCP. A flag overrides the file, even when empty (`-unix ""` turns the unix listener off).

## Flags

```
ndb [-config ndb.yml] [-data-dir DIR] [-unix PATH] [-tcp ADDR] [-unix-mode 0660] [-version]
ndb backup [-config f] [-data-dir d] [-unix sock] <dest>
```

| Flag | Meaning |
|---|---|
| `-config` | config file. Default `ndb.yml` in the working dir. A missing file is fine. |
| `-data-dir` | overrides `data_dir`. |
| `-unix` | overrides `listen.unix`. |
| `-tcp` | overrides `listen.tcp`, for example `10.10.0.1:7070`. |
| `-unix-mode` | permission of the socket file, octal (`0660`). Default: the process umask. |
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
| `data_dir` | where all data lives. Never put it on NFS, EFS or another network volume. |
| `listen.unix` | unix socket path. |
| `listen.tcp` | TCP address. Bind to a private address only. There is no authentication yet. |
| `memory_limit` | Go heap soft limit (`GOMEMLIMIT`). |
| `group_limit` | memory limit of one aggregate group table. Over it: `group_limit_exceeded`. |
| `blob_max_size` | largest blob. Default 1 GiB. |
| `allow_implicit` | `false` forbids creating a collection by the first `insert`. Default `true`. |

### Collection keys

| Key | Meaning |
|---|---|
| `write` | `engine` (default): only ndb writes, through its WAL. `direct`: apps append to the files in `path`; ndb only reads. |
| `path` | glob of source files. Direct mode only. `**` is allowed. |
| `id` | field that identifies a document. Default `_id`. Direct mode without `id`: `file:offset`. |
| `time` | time field. Used for zone maps, `$dateTrunc`, TTL and retention. |
| `indexes` | fields with a secondary index (dot paths allowed). |
| `unique` | fields with a unique constraint (engine mode). |
| `durability` | `strict` or `relaxed`. Engine mode only. See below. |
| `retention` | hide and drop derived data older than this (by `time`). The source NDJSON is never deleted. |
| `ttl` | each document expires this long after its `time` field. Needs `time`. |
| `expires_field` | each document expires at the time in this field (RFC 3339 or unix seconds). No field: never expires. |
| `max_line_size` | largest line or request body. Default 1 MiB, maximum 16 MiB. |
| `blob_threshold` | engine mode: string values over this size go to the blob store. Default off. |

Durations use Go syntax (`90m`, `1h30m`) plus `d` and `w` (`24h`, `30d`, `2w`).

## Code and file together

An app can also declare its collection from code (`ensure_collection`: the SDKs do it on boot). Merge rules: `indexes` are united; a different `id`, `write`, `path` or `time` is `catalog_conflict`; for `durability` and `retention`, `ndb.yml` wins.

## Durability

| Mode | Behavior |
|---|---|
| `strict` (default) | A write is answered after the WAL is fsynced. Concurrent writes share one fsync (group commit). A crash loses nothing that was answered. |
| `relaxed` | A write is answered after `write()`. A background fsync runs every 200 ms. A crash can lose the last 200 ms, but never corrupts data. |

If an fsync fails, the daemon exits. It does not retry: after a failed fsync, Linux can drop the dirty pages. The start-up recovery then makes the state right.

In direct mode, durability is the job of the app that appends the file.
