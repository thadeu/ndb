# Operations runbook

## Run

- systemd: [deploy/systemd/ndb.service](../../deploy/systemd/ndb.service) and [ndb.yml](../../deploy/systemd/ndb.yml).
- Docker: [docker.md](docker.md).
- Run one daemon per data dir. A second one fails on the lock.
- Keep `data_dir` on a local disk. Never on NFS, EFS or another network volume: `flock` and `fsync` are not reliable there.
- Stop with SIGTERM and wait at least 30 s.
- Configuration and durability: [configuration.md](configuration.md).

## Health

`GET /health` is `200 {"status":"ok"}` after recovery and `503` before. Use it for readiness. Clients should retry `not_ready` and reconnect after a daemon restart.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/health
```

## Backup and restore

`ndb backup <dest>` works with the daemon stopped or running. `dest` must not exist or must be empty.

```bash
ndb backup -config /etc/ndb/ndb.yml /backups/ndb-2026-11-05
```

- Daemon stopped: it copies the whole data dir.
- Daemon running: it asks the daemon, on the unix socket, for a consistent copy. The copy has the catalog, the blobs and the WAL up to a mark taken between two commits. Segments and `meta.bolt` are left out, because they are derived.
- Direct-mode collections: only their definition is saved. The data is in the apps' own files; back those up yourself.
- Online backup needs `listen.unix`. It is refused over TCP.

Restore: stop the daemon, put the copy where `data_dir` is, start the daemon. It rebuilds segments and indexes from the WAL. Check with `GET /v1/c/{coll}/stats`.

```bash
systemctl stop ndb
cp -a /backups/ndb-2026-11-05/. /var/lib/ndb/
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
