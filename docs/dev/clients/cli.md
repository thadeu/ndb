# CLI

The CLI is a client too: most commands call the same HTTP API as the SDKs, so a script can use `ndb` where it would use `curl`.

## Commands

The `ndb` binary is the daemon and the CLI. `ndb` with only flags still runs the daemon; `ndb serve` is the same, written out. The data dir always lives on the host, even when the daemon runs in Docker (see [docker.md](../deploy/docker.md#cli)). Common flags: `-config FILE` and `-data-dir DIR`.

| Command | What it does |
| --- | --- |
| `ndb serve [flags]` | Run the daemon. |
| `ndb backup <dest\|->` | Write a `.ndb` backup file (`-gzip` for `.ndb.gz`). See [Backup and restore](../operations.md#backup-and-restore). |
| `ndb restore <file\|->` | Restore a `.ndb` or `.ndb.gz` file into a data dir. |
| `ndb stats` | Per collection: rows, size, segments, WAL files, indexes, quarantined lines. |
| `ndb size` | Disk usage per collection and per part. |
| `ndb collections [name]` | The catalog, or the definition of one collection. |
| `ndb compact <collection>` | Seal and merge the segments now. |
| `ndb quarantine <collection>` | The bad lines of a direct collection. |
| `ndb query <collection> ['<where>']` | Find rows (`-aggregate` for a pipeline). |
| `ndb get <collection> <_id>` | One document by `_id`. |
| `ndb update <collection> '<json>'` | [Atomic update by filter](../query.md#update-by-filter): the JSON mixes the filter and the write operators. `--many`, `--upsert`, `--expire-in 60s`. `ndb update <collection> <_id> '<ops>'` updates one by `_id`. |
| `ndb delete <collection> '<json>'` | Delete the documents that the JSON filter matches. `--many`. `ndb delete <collection> <_id>` deletes one by `_id`. |
| `ndb health` | Exit 0 and print `ok` when the daemon is ready; exit 1 otherwise. For container health checks. |
| `ndb version` | Print the version. `ndb -version` still works. |
| `ndb help` | List the commands. |

## Connect and flags

Every command except `backup`, `restore`, `serve`, `version` and `help` is a thin client of the [HTTP API](../http-api.md#stats-size-and-admin). They share these flags:

- `-url unix:///run/ndb/ndb.sock` or `tcp://host:7070` picks the daemon. The default is `NDB_URL`, then the `listen.unix` socket of `ndb.yml` when something answers on it. `NDB_TOKEN` is the bearer token for TCP.
- `-config FILE` and `-data-dir DIR`. `-data-dir` and `-url` together are an error.
- `-json` prints the JSON of the API (`query`: NDJSON rows).
- Only `stats` and `size` have an offline mode: with `-data-dir`, or when no daemon answers, they read the data dir (no lock, so a running daemon is fine). `collections`, `compact`, `quarantine` and `query` need a daemon and say so when none answers.

## Output

Tables use box-drawing characters. On a terminal they fit its width: the widest text columns (more than 8 cells) shrink first, in proportion, down to 10 cells and then further, and their cells end in `…`; short text such as `direct` or `engine`, numbers and ids are cut last. Headers are lowercase, like the field names that `ndb query` prints. The width is the terminal size, then `COLUMNS`, then 100. Piped output is never cut. ASCII borders are used when the locale (`LC_ALL`, `LC_CTYPE`, `LANG`) is not UTF-8 or `TERM=dumb`. Color (bold header, dim borders) needs a terminal and no `NO_COLOR`. A CJK or emoji character counts as two cells; a ZWJ emoji sequence is counted per character, so it can look a little wide.

## backup

`ndb backup <dest|-> [-gzip] [-no-blobs] [-force] [-url unix://...] [-data-dir DIR] [-config FILE]`

- Writes one NDJSON file. `.ndb` (or `.ndb.gz` with `-gzip`) is added when `dest` does not end with it. `-` writes to stdout.
- With a running daemon it asks the daemon on the unix socket; with none it opens the data dir under the lock.
- The format is in [Backup and restore](../operations.md#backup-and-restore).

```
$ ndb backup /backups/daily -gzip
backup: online written to /backups/daily.ndb.gz
```

## restore

`ndb restore <file|-> [-data-dir DIR] [-force]`

1. It takes the data dir lock. A running daemon holds it, so the command refuses.
2. It refuses a data dir that is not empty, unless `-force`.
3. It reads the file into `<data-dir>/.restore-stage` and checks it: header, every line CRC, transaction order, blob sha256, the `end` line. Gzip is found by the first bytes. `-` reads stdin.
4. It opens the stage as a daemon would: the WAL is replayed, segments and indexes are rebuilt.
5. Only then it moves the checked copy into place. On any error the data dir stays as it was.

```
$ ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb
restore: /backups/daily.ndb.gz restored to /var/lib/ndb (1 collections, 2 rows)

$ head -c 300 daily.ndb | ndb restore - -data-dir /tmp/x
restore: the backup is cut in the middle of line 4 (offset 240); /tmp/x is untouched
```

## stats

`ndb stats [-data-dir DIR | -url URL] [-json]` calls `GET /v1/stats`.

```
┌────────────┬────────┬──────┬───────────┬──────────┬─────┬─────────────┬────────────────┐
│ collection │ mode   │ rows │      size │ segments │ wal │ quarantined │ indexes        │
├────────────┼────────┼──────┼───────────┼──────────┼─────┼─────────────┼────────────────┤
│ calls      │ direct │    3 │ 128.1 KiB │        0 │   0 │           1 │ status (ready) │
│ posts      │ engine │    4 │ 131.1 KiB │        1 │   1 │           0 │ author (ready) │
├────────────┼────────┼──────┼───────────┼──────────┼─────┼─────────────┼────────────────┤
│ total      │        │    7 │ 515.2 KiB │        1 │   1 │           1 │                │
└────────────┴────────┴──────┴───────────┴──────────┴─────┴─────────────┴────────────────┘
ndb dev · up 12s · blobs 0 B · cursors 128.0 KiB
```

At 80 columns the stats table cuts `indexes` and keeps `direct` and `engine` whole:

```
┌────────────┬────────┬──────┬───────────┬──────────┬─────┬─────────────┬──────┐
│ collection │ mode   │ rows │      size │ segments │ wal │ quarantined │ ind… │
├────────────┼────────┼──────┼───────────┼──────────┼─────┼─────────────┼──────┤
│ calls      │ direct │    3 │ 128.1 KiB │        0 │   0 │           1 │ sta… │
│ posts      │ engine │    4 │ 131.1 KiB │        1 │   1 │           0 │ aut… │
├────────────┼────────┼──────┼───────────┼──────────┼─────┼─────────────┼──────┤
│ total      │        │    7 │ 515.2 KiB │        1 │   1 │           1 │      │
└────────────┴────────┴──────┴───────────┴──────────┴─────┴─────────────┴──────┘
ndb dev · up 3s · blobs 0 B · cursors 128.0 KiB
```

Offline (`-data-dir`, or no daemon) the table shows `-` for rows, mode, quarantined and indexes: they live in the daemon.

## size

`ndb size [-data-dir DIR | -url URL] [-json]` calls `GET /v1/size`: bytes per collection split in `wal`, `segments` and `meta` (`meta.bolt`), plus the shared `blobs`, `cursors.bolt` and catalog (`meta` of the `(shared)` row). It only stats files, so it takes no lock.

```
┌──────────┬───────┬──────────┬───────────┬───────┬───────────┬───────────┐
│ name     │   wal │ segments │      meta │ blobs │   cursors │     total │
├──────────┼───────┼──────────┼───────────┼───────┼───────────┼───────────┤
│ calls    │   0 B │      0 B │ 128.1 KiB │   0 B │       0 B │ 128.1 KiB │
│ posts    │ 524 B │  2.6 KiB │ 128.0 KiB │   0 B │       0 B │ 131.1 KiB │
│ (shared) │   0 B │      0 B │ 128.0 KiB │   0 B │ 128.0 KiB │ 256.0 KiB │
├──────────┼───────┼──────────┼───────────┼───────┼───────────┼───────────┤
│ total    │ 524 B │  2.6 KiB │ 384.1 KiB │   0 B │ 128.0 KiB │ 515.2 KiB │
└──────────┴───────┴──────────┴───────────┴───────┴───────────┴───────────┘
```

## collections

`ndb collections [name] [-json]` calls `GET /v1/c`, or `GET /v1/c/{name}` for one collection (every definition field and the index state).

```
┌───────┬────────┬─────────┬──────┬─────────┬─────┬───────────┬─────────────────────────┐
│ name  │ write  │ id      │ time │ indexes │ ttl │ retention │ path                    │
├───────┼────────┼─────────┼──────┼─────────┼─────┼───────────┼─────────────────────────┤
│ calls │ direct │ call_id │ -    │ status  │ -   │ -         │ /tmp/ndbd/logs/*.ndjson │
│ posts │ engine │ post_id │ -    │ author  │ -   │ -         │ -                       │
└───────┴────────┴─────────┴──────┴─────────┴─────┴───────────┴─────────────────────────┘
```

## compact

`ndb compact <collection> [-json]` calls `POST /v1/c/{coll}/compact`: it seals the memtable and merges all segments into one. It waits for the end (up to 10 minutes). A direct collection fails with `not_supported`.

```
┌────────────┬──────────┬────────────────────┬──────┐
│ collection │ segments │               size │ time │
├────────────┼──────────┼────────────────────┼──────┤
│ posts      │   1 -> 1 │ 2.6 KiB -> 2.6 KiB │ 24ms │
└────────────┴──────────┴────────────────────┴──────┘
```

## quarantine

`ndb quarantine <collection> [-limit N] [-json]` calls `GET /v1/c/{coll}/quarantine`. The default `-limit` is 20. See [Quarantine](#quarantine).

```
┌─────────────────────────┬────────┬───────────────────────────────────────────┬──────────┐
│ file                    │ offset │ error                                     │ line     │
├─────────────────────────┼────────┼───────────────────────────────────────────┼──────────┤
│ /tmp/ndbd/logs/a.ndjson │     62 │ invalid_doc: a line must be a JSON object │ not json │
└─────────────────────────┴────────┴───────────────────────────────────────────┴──────────┘
showing 1 of 1 quarantined lines (newest last)
```

## query

`ndb query <collection> ['<where-json>'] [-sort JSON] [-fields a,b] [-limit N] [-json|-ndjson]` calls `POST /v1/c/{coll}/find`. `ndb query <collection> -aggregate '<pipeline-json>'` calls `aggregate`. Flags may come before or after the positional arguments. The table has one column per top-level key of the first 200 rows, in order of appearance; nested values are compact JSON; number columns are right-aligned. With `-json` (or `-ndjson`) the output is the NDJSON of the API, ready for `jq`.

```
$ ndb query posts '{}' -sort '{"likes":-1}'
┌─────────┬──────────┬───────┬─────────────────────────────────────────────────┐
│ post_id │ author   │ likes │ title                                           │
├─────────┼──────────┼───────┼─────────────────────────────────────────────────┤
│ p3      │ author-3 │  4500 │ A fairly long post title number 3, to test shr… │
│ p2      │ author-2 │  3000 │ A fairly long post title number 2, to test shr… │
│ p1      │ author-1 │  1500 │ A fairly long post title number 1, to test shr… │
└─────────┴──────────┴───────┴─────────────────────────────────────────────────┘
3 rows · plan scan · examined 3
```

(The example is at 80 columns. At 160 columns, or piped, the title is whole.)

## get

`ndb get <collection> <_id>` calls `GET /v1/c/{coll}/{_id}` and prints the document with `_v`. A missing id exits with `not_found`.

```
$ ndb get calls 0192f3a4-7c1e-7b2a-9f3d-1a2b3c4d5e6f
```

## update

`ndb update <collection> '<json>' [--many] [--upsert] [--expire-in 60s]` calls `POST /v1/c/{coll}/update`. The JSON mixes the filter and the write operators, as in [Update by filter](../query.md#update-by-filter).

```
$ ndb update stock '{"sku":"a","qty":{"$gte":1},"$inc":{"qty":-1}}'
$ ndb update calls '{"status":"failed","$set":{"status":"retry"}}' --many
$ ndb update hits '{"_id":"ip:1.2.3.4","$inc":{"n":1}}' --upsert --expire-in 60s
$ ndb update stock sku-a '{"$inc":{"qty":-1}}'          # by _id
```

## delete

`ndb delete <collection> '<json>' [--many]` calls `POST /v1/c/{coll}/delete` with the JSON as the filter, and prints how many documents it deleted.

```
$ ndb delete sessions '{"status":"expired"}' --many
$ ndb delete sessions s1                                 # by _id
```
