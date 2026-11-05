# Operations runbook

## Run

- systemd: [deploy/systemd/ndb.service](../../deploy/systemd/ndb.service) and [ndb.yml](../../deploy/systemd/ndb.yml).
- Docker: [docker.md](docker.md).
- Run one daemon per data dir. A second one fails on the lock.
- Keep `data_dir` on a local disk. Never on NFS, EFS or another network volume: `flock` and `fsync` are not reliable there.
- Stop with SIGTERM and wait at least 30 s.
- Configuration and durability: [configuration.md](configuration.md).

## CLI

The `ndb` binary is the daemon and the CLI. `ndb` with only flags still runs the daemon; `ndb serve` is the same, written out. The data dir always lives on the host, even when the daemon runs in Docker (see [docker.md](docker.md#cli)). Common flags: `-config FILE` and `-data-dir DIR`.

| Command | What it does |
| --- | --- |
| `ndb serve [flags]` | Run the daemon. |
| `ndb backup <dest>` | Copy the data dir (see above). |
| `ndb restore <backup-dir>` | Put a backup in a data dir. |
| `ndb stats` | Per collection: rows, size, segments, WAL files, indexes, quarantined lines. |
| `ndb size` | Disk usage per collection and per part. |
| `ndb version` | Print the version. `ndb -version` still works. |
| `ndb help` | List the commands. |

### restore

`ndb restore <backup-dir> [-data-dir DIR] [-force]`

1. It takes the data dir lock. A running daemon holds it, so the command refuses.
2. It refuses a data dir that is not empty, unless `-force`.
3. It copies the backup to `<data-dir>/.restore-stage` and opens it as a daemon would: the WAL is replayed and segments and indexes are rebuilt. A backup that does not recover stops here and the data dir stays as it was.
4. Only then it removes the old contents (`-force`) and moves the checked copy into place.

### stats

`ndb stats [-data-dir DIR | -url URL] [-json]`

- `-url unix:///run/ndb/ndb.sock` or `tcp://host:7070` (default `NDB_URL`; `NDB_TOKEN` is the bearer token) asks the daemon: `GET /v1/c` and `GET /v1/c/{coll}/stats` give rows, write mode, indexes and quarantined lines. Size, segment and WAL file counts are added from the data dir when it is readable.
- With no flag, the `listen.unix` socket of `ndb.yml` is tried; when nothing answers, it falls back to the data dir.
- `-data-dir` reads the data dir only. It takes no lock, so it works with the daemon running. Rows, indexes and quarantine show `-` (they need the daemon).

### size

`ndb size [-data-dir DIR] [-json]` prints bytes per collection split in `wal`, `segments` and `meta` (`meta.bolt`), plus the shared `blobs`, `cursors.bolt` and catalog (`meta` of the `(shared)` row). Units are KiB, MiB, GiB. It only stats files, so it takes no lock.

## Health

`GET /health` is `200 {"status":"ok"}` after recovery and `503` before. Use it for readiness. Clients should retry `not_ready` and reconnect after a daemon restart.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/health
```

## Install

```bash
curl -fsSL https://ndb.clowk.in/install.sh | bash                    # binary only
curl -fsSL https://ndb.clowk.in/install.sh | sudo bash -s -- --systemd  # + user, /var/lib/ndb, /etc/ndb/ndb.yml, ndb.service
```

The script checks the sha256 of the release. `NDB_VERSION=0.1.0` pins a version; `NDB_INSTALL_DIR` sets the binary dir (default `/usr/local/bin`, else `~/.local/bin`). It never overwrites an existing `/etc/ndb/ndb.yml`. Remove with `uninstall.sh` (`--systemd` stops and removes the unit and keeps the data; add `--purge` to delete `/var/lib/ndb`, `/etc/ndb` and the user).

## Backup and restore

`ndb backup <dest>` works with the daemon stopped or running. `dest` must not exist or must be empty.

```bash
ndb backup -config /etc/ndb/ndb.yml /backups/ndb-2026-11-05
```

- Daemon stopped: it copies the whole data dir.
- Daemon running: it asks the daemon, on the unix socket, for a consistent copy. The copy has the catalog, the blobs and the WAL up to a mark taken between two commits. Segments and `meta.bolt` are left out, because they are derived.
- Direct-mode collections: only their definition is saved. The data is in the apps' own files; back those up yourself.
- Online backup needs `listen.unix`. It is refused over TCP.

Restore with `ndb restore` (see [CLI](#cli)). Stop the daemon first; the command refuses when the data dir is locked.

```bash
systemctl stop ndb
sudo -u ndb ndb restore /backups/ndb-2026-11-05 -data-dir /var/lib/ndb
systemctl start ndb
```

Copy the backup to other storage (for example S3) from there; ndb does not upload.

## Metrics

`GET /metrics` is Prometheus text.

| Metric | Type | Meaning |
|---|---|---|
| `ndb_ready` | gauge | 1 after recovery |
| `ndb_collections` | gauge | number of collections |
| `ndb_requests_total`, `ndb_request_errors_total`, `ndb_requests_canceled_total` | counter | requests, HTTP errors (4xx and 5xx), client-canceled |
| `ndb_rows_returned_total`, `ndb_writes_total` | counter | rows read, writes done |
| `ndb_query_seconds{type="find\|aggregate"}` | histogram | query latency |
| `ndb_memtable_bytes{collection}` | gauge | unsealed rows in memory |
| `ndb_segments{collection}` | gauge | number of segments |
| `ndb_tailer_lag_bytes{collection}` | gauge | direct mode: bytes not read yet |
| `ndb_quarantined_total{collection}` | counter | lines in quarantine |
| `ndb_watch_subscribers{collection}` | gauge | open watch streams |
| `ndb_index_build_progress{collection,field}` | gauge | 0 to 1 |
| `ndb_wal_records_total`, `ndb_commit_seconds`, `ndb_fsync_seconds` | counter, histograms | engine-mode write path |
| `ndb_blob_store_blobs`, `ndb_blob_store_bytes` | gauge | blob store size |

Alert on: `ndb_ready == 0`, a growing `ndb_tailer_lag_bytes`, an increase of `ndb_quarantined_total`, and `ndb_fsync_seconds` high percentiles.

## Quarantine

In direct mode, a line that is not a JSON object, has a bad id, or is over `max_line_size` is not applied. ndb writes it to `<data_dir>/collections/<name>/quarantine/<name>.ndjson` and goes on. It never stops on a bad line.

```json
{"ts":"2026-11-05T16:00:18Z","file":"/data/logs/app-1.ndjson","offset":63,"reason":"invalid_doc: a line must be a JSON object","line":"not json"}
```

Find the writer that produced the line with `file` and `offset`, fix it, and watch `ndb_quarantined_total`. The quarantine file is not read again; delete it when you are done. `stats` shows the count as `quarantined`.

## Recovery

Recovery runs at every start, and needs no command. After a crash (SIGKILL, power loss) the daemon:

1. takes the lock (a stale `LOCK` file is not a problem; the `flock` is released when the process dies);
2. replays the WAL after the checkpoint and drops a cut last line;
3. continues each source file from its saved cursor;
4. removes orphan temporary files and segments.

While it runs, `/health` is `503`. For a large WAL this takes seconds, not minutes. A corrupt line in the middle of a sealed WAL file stops the start with an error (`recovery: corrupt line in a sealed WAL segment`): restore from backup. If the derived data looks wrong, stop the daemon, delete `segments/` and `meta.bolt` of the collection, and start again: they are rebuilt from the NDJSON. In direct mode, deleting `meta.bolt` makes ndb read the source files again, so bad lines are added to quarantine again.

If an `fsync` fails, the daemon exits on purpose, and recovery gives the right state when it is started again.

## Compaction, retention and TTL

- **Compaction** runs by itself. It merges adjacent small segments and drops superseded versions, tombstones, and expired or past-retention rows. Blobs that no live document uses are deleted after a grace period.
- **`wal_retention: 7d`** (engine mode) deletes a sealed WAL file when the checkpoint is past it, every saved `watch` cursor is after it, no open stream reads it, and it is older than 7 days. Files go from the oldest and the walk stops at the first one that stays. Without it the WAL is kept forever. A `watch` that needs a deleted file fails with `history_truncated` (410).
- **`retention: 30d`** hides rows whose `time` is older than 30 days, at read time, and drops them from the derived data when segments are compacted. The source NDJSON is never deleted by ndb.
- **`ttl: 24h`** or **`expires_field`**: expired documents are hidden from every read at once. In engine mode a sweeper (every 10 s) deletes them in the WAL, and `watch` shows the delete with `"_expired": true`. In direct mode the documents are only hidden; the files are not touched.
- Disk use: watch the size of `data_dir`, and `ndb_segments`.

## Version and token

`ndb -version` prints the version. To protect the TCP listener, start with `-token-file`; rotate the token by changing the file, restarting the daemon, and updating `NDB_TOKEN` in the apps. See [configuration.md](configuration.md#authentication).

## Upgrade

Replace the binary and restart. Take a backup first. API paths start with `/v1`, so a newer daemon serves older SDKs.
