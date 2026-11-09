# Operations

The day-to-day runbook for a running ndb: is it healthy, how to back it up, what to do after a crash, how data ages, and how to upgrade. To install it first, see [Deploy](deployment.md). Every command is in [CLI](clients/cli.md).

## Check

### Health

`ndb health` (exit 0 when ready) and `GET /_health` answer the same question. `GET /_health` is `200 {"status":"ok"}` after recovery and `503` before. Use it for readiness. Clients should retry `not_ready` and reconnect after a daemon restart.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/_health
```

### Metrics

`GET /_metrics` is Prometheus text (set `metrics_path: /_metrics` in the Prometheus scrape config).

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
| `ndb_quarantined_total{collection}` | counter | distinct lines in the quarantine file (file and offset), not events |
| `ndb_watch_subscribers{collection}` | gauge | open watch streams |
| `ndb_index_build_progress{collection,field}` | gauge | 0 to 1 |
| `ndb_wal_records_total`, `ndb_commit_seconds`, `ndb_fsync_seconds` | counter, histograms | engine-mode write path |
| `ndb_blob_store_blobs`, `ndb_blob_store_bytes` | gauge | blob store size |

Alert on: `ndb_ready == 0`, a growing `ndb_tailer_lag_bytes`, an increase of `ndb_quarantined_total`, and `ndb_fsync_seconds` high percentiles.

## Protect the data

### Backup and restore

A backup is **one NDJSON file** with the `.ndb` extension, optionally gzip-compressed (`.ndb.gz`). It works with the daemon stopped or running.

```bash
ndb backup /backups/daily -gzip                  # → /backups/daily.ndb.gz
ndb backup - | aws s3 cp - s3://bucket/daily.ndb # stdout
```

The file is readable with `head`, `grep` and `jq`:

```json
{"ndb_backup":1,"version":"0.2.0","created_at":"2026-11-06T14:13:52Z","collections":["users"]}
{"_section":"catalog","name":"users","definition":{"name":"users","write":"engine","unique":["email"]}}
{"_section":"cursor","collection":"users","consumer":"mailer","pos":"e2.1"}
{"_section":"wal","collection":"users"}
{"_tx":1,"_ts":"...","ops":[{"_op":"put","_id":"u1","_v":1,"doc":{...}},{"_op":"put","_id":"u2","_v":1,"doc":{...}}],"_crc":"c764fece"}
{"_tx":2,"_ts":"...","_op":"put","_id":"u1","_v":2,"doc":{...,"tags":["vip"]},"_crc":"8dd99733"}
{"_section":"blob","ref":"sha256:ab12...","type":"audio/wav","size":48213,"data":"<base64>"}
{"_section":"end","lines":7,"crc":"5b24d8b2"}
```

| Line | Meaning |
|---|---|
| header | the first line; marks the file as an ndb backup, with the format and daemon versions |
| `catalog` | the definition of each collection (indexes, unique, TTL, write mode) |
| `cursor` | the saved position of each `watch` consumer |
| `wal` | the WAL lines of one collection, as stored, each with its `_crc` |
| `snapshot` | only when `wal_retention` deleted the first WAL files: one line per live document at the backup point, then an empty `wal` section |
| `blob` | the blobs that the saved documents use, in base64 (1 MiB per line for big ones). `-no-blobs` leaves them out. |
| `end` | the line count and CRC of everything before it. A file without it is cut and is refused. |

- **Daemon running:** the CLI asks the daemon on the unix socket (`-url unix://...`, `NDB_URL`, or `listen.unix` of `ndb.yml`). The daemon takes a mark between two commits, so the file never holds half a transaction. Online backup is refused over TCP.
- **Daemon stopped:** the CLI opens the data dir itself, under the lock, like a short daemon start.
- Segments and indexes are not saved: they are derived, and restore rebuilds them.
- Direct-mode collections: only their definition is saved. The data is in the apps' own files; back those up yourself.
- The file is written as `<name>.part` and renamed at the end. An existing file is refused without `-force`.

**Restore** needs the daemon stopped:

```bash
systemctl stop ndb
sudo -u ndb ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb
systemctl start ndb
```

1. It takes the data dir lock, and refuses a data dir that is not empty unless `-force`.
2. It reads the file in one pass into `<data-dir>/.restore-stage`. Gzip is found by the first bytes, not by the name. It checks the header, the CRC of every WAL line, the transaction order, the sha256 of every blob, and the `end` line.
3. It opens the stage like a daemon: replays the WAL, rebuilds segments, indexes and unique keys.
4. Only then it moves the checked copy into place. On any error the data dir stays as it was.

After a restore, documents keep their `_v`, and `watch` consumers resume after their last ack. A consumer whose position is older than a `snapshot` gets `history_truncated`.

### Point in time recovery

A copy of a collection as it was at an instant of the last day: `ndb restore calls -at 15m -to calls_restored`, then `ndb rename calls_restored calls -replace`. See [PITR](pitr.md).

### Recovery after a crash

Recovery runs at every start, and needs no command. After a crash (SIGKILL, power loss) the daemon:

1. takes the lock (a stale `LOCK` file is not a problem; the `flock` is released when the process dies);
2. replays the WAL after the checkpoint and drops a cut last line;
3. continues each source file from its saved cursor;
4. removes orphan temporary files and segments.

While it runs, `/_health` is `503`. For a large WAL this takes seconds, not minutes. A corrupt line in the middle of a sealed WAL file stops the start with an error (`recovery: corrupt line in a sealed WAL segment`): restore from backup. If the derived data looks wrong, stop the daemon, delete `segments/` and `meta.bolt` of the collection, and start again: they are rebuilt from the NDJSON. In direct mode, deleting `meta.bolt` makes ndb read the source files again, so bad lines are added to quarantine again.

If an `fsync` fails, the daemon exits on purpose, and recovery gives the right state when it is started again.

## Data over time

### Compaction, retention and TTL

- **Compaction** runs by itself. It merges adjacent small segments and drops superseded versions, tombstones, and expired or past-retention rows. Blobs that no live document uses are deleted after a grace period.
- **`wal_retention: 7d`** (engine mode) deletes a sealed WAL file when the checkpoint is past it, every saved `watch` cursor is after it, no open stream reads it, and it is older than 7 days. Files go from the oldest and the walk stops at the first one that stays. Without it the WAL is kept forever. A `watch` that needs a deleted file fails with `history_truncated` (410).
- **`retention: 30d`** hides rows whose `time` is older than 30 days, at read time, and drops them from the derived data when segments are compacted. The source NDJSON is never deleted by ndb.
- **`ttl: 24h`** or **`expires_field`**: expired documents are hidden from every read at once. In engine mode a sweeper (every 10 s) deletes them in the WAL, and `watch` shows the delete with `"_expired": true`. In direct mode the documents are only hidden; the files are not touched.
- **Freeing disk after expiry:** deleting an expired document does not shrink the files. Compaction removes it from the segments, and only `wal_retention` removes it from the WAL, so a TTL collection should set `wal_retention` (for example `1d`). See [configuration.md](configuration.md#freeing-disk).
- Disk use: watch the size of `data_dir`, and `ndb_segments`.

### Truncate and drop

Collections split by time (`calls_2026_11`, `logs_2026_w45`) are emptied or removed whole when their period ends. A `delete` cannot do that: an empty filter is `empty_filter`, and it writes one tombstone for each document.

| | Truncate | Drop |
|---|---|---|
| Documents | removed | removed |
| Definition and indexes | kept | removed |
| Watch consumers | kept; each gets one `truncate` event | removed |
| HTTP | `POST /{coll}/_truncate?confirm={coll}` | `DELETE /{coll}?confirm={coll}` |
| CLI | `ndb truncate <coll>` | `ndb drop <coll>` |
| Go, Ruby, Rails, TS | `Truncate(ctx)`, `truncate(coll)`, `Model.truncate!`, `truncate()` | `Drop(ctx)`, `drop(coll)`, `Model.drop!`, `drop()` |

- **Cost:** both take a few ms for 10 or 10 million documents. ndb swaps the collection dir: the old dir goes to `<data_dir>/trash/`. With a [PITR window](pitr.md) (the default, 1 day) it stays there for the window, so the collection can be restored; then, or right away with `pitr: off`, a background job deletes it. The disk space returns when that job ends.
- **Other calls:** calls on the collection that are running finish first. New calls wait for the swap (a few ms); they do not fail. A backup or a blob GC that is running finishes before the swap, so it sees the collection before or after it, never half.
- **Watch:** a truncate writes one marker in the new WAL, with the next tx. A consumer gets one `truncate` event and then the new changes, however far behind it was. Open streams end; the consumer reconnects from its last ack.
- **Crash:** a crash during a truncate leaves the collection as it was before or after it. The next start finishes or removes `<name>~new`, and empties what `trash/` keeps past its window.
- **`ndb.yml`:** a collection declared there comes back, empty, at the next start after a drop. Remove it from `ndb.yml` too.
- **Direct mode:** both are refused (`409 direct_collection`). The files belong to the app.
- **Downgrade:** a WAL with a truncate marker cannot be read by ndb 0.3.2 or older.
- **Reading many periods:** a [collection pattern](query.md#collection-patterns) such as `logs-2026-*` reads every partition in one call.

### Quarantine (direct mode)

In direct mode, a line that is not a JSON object, has a bad id, or is over `max_line_size` is not applied. ndb writes it to `<data_dir>/collections/<name>/quarantine/<name>.ndjson` and goes on. It never stops on a bad line.

```json
{"ts":"2026-11-05T16:00:18Z","file":"/data/logs/app-1.ndjson","offset":63,"reason":"invalid_doc: a line must be a JSON object","line":"not json"}
```

Find the writer that produced the line with `file` and `offset`, fix it, and watch `ndb_quarantined_total`. ndb does not read the quarantine file back to recover; delete it when you are done. `stats` shows the count as `quarantined`, and `ndb quarantine <collection>` (or `GET /{coll}/_quarantine`) lists the last lines. After a restart a direct collection reads again the lines after its last checkpoint, but a line already in the file (same `file` and `offset`) is not written again, so the file has one entry per bad line however often the daemon restarts. The count (`quarantined`, `ndb_quarantined_total`) is the number of distinct lines in the file: it is read from the file at start, so a restart does not raise it, and it drops if you delete the file. A file written by an older version may still hold repeats; the API shows each line once.

## Measure

`ndb bench` runs the ESL outbox scenario (6 events and a `finishcall` per call) on a daemon of its own, from the same binary, in a temp dir. It has six parts; with no flag it runs all of them, in about three minutes, and then times the first call on the loaded partition after it sleeps (the daemon runs with `idle_close: 20s`):

| Flag | Measures |
|---|---|
| `-stress-load` | the writes: 30 s of live writes at 400 docs/s (`strict`) with a `_watch` consumer, a `kill -9` with every acked write read back, a bulk load of 30k calls into a daily partition |
| `-query` | lookups by `_id`, indexed searches, sorted pages, a scan of a field with no index, counts |
| `-aggs` | the dashboards: calls per customer, events per type, duration per hour, talk time, hangup causes |
| `-lookup` | the lookups: finishcalls with their customer (`belongs_to`), customers with their last events (`has_many`, `has_one`), 2 levels, and `$lookup` at the end of an aggregate |
| `-concurrent` | apps that share the daemon: 1, 3 and 10 clients read at the same time for 15 s each (the queries, aggregations and lookups above, in a random order), with reads per second, p50, p99, errors and the RSS peak |
| `-direct` | direct mode (`write: direct`): 500k cdrs (`-direct-rows`) and a tenth of users in NDJSON files. Cold: the files exist before ndb starts, and the start time is the load time. Live: ndb starts on empty folders while the app appends in 64 KiB writes (lines cut between writes) and a new file every 100k lines; the catch-up is the time after the last write until every line is a document. Both check the counts and 1000 ids read back; then the reads of the cdrs and their users |
| `-idle` | what idle collections cost: 300 collections of 10 documents (`-collections`) and a partitioned collection with 365 daily partitions, on a daemon of their own with `idle_close: 2s`; once they sleep, disk, CPU, RSS and open files over 10 s, the first call on a sleeping collection, and a `kill -9` while writes wake collections |

`-query` and `-aggs` load the calls first without measuring it. Each read runs 5 times: the table shows the median, the min and the max, and the RSS peak of each part. Copy the binary to a machine and run it there to compare machines (arm64, x86) or versions.

```bash
ndb bench                      # everything
ndb bench -query -aggs         # only the reads
ndb bench -stress-load -calls 100000
ndb bench -yml 'memory_limit: 512MiB'   # compare a setting of ndb.yml
```

While it runs, the bench logs each step to stderr: a line when it starts, one every 5 s with where it is, and one when it ends with what it found, each with the time since the start and the ETA of the run. The report goes to stdout, so `ndb bench > report.md` keeps the log on the terminal:

```
[00:30] ✓ live writes in 31s: 12001 docs, 0 failed, p99 2.7 ms · ETA 1m22s
[00:35] … crash 5s, writing, 1998 docs · ETA 1m17s
[00:52] … bulk load 5s, 78400 of 210000 docs · ETA 1m0s
```

After the parts, each measurement is checked against a budget and the bench prints a table of checks with PASS or FAIL. A FAIL makes it exit 1:

```
| Check | Measured | Budget | Result |
|---|---|---|---|
| acked writes lost by a kill -9 | 0 | ≤ 0 | PASS |
| live write p99 | 3.1 ms | ≤ 15 ms | PASS |
| disk per idle collection | 4112.4 KB | ≤ 256 KB | **FAIL** |
```

A bigger load (`-calls 285715` is 2 million documents) reads more, so the time budgets of the reads are shown but not judged; memory, lost writes and errors are judged at any size.

The budgets (in `internal/bench/checks.go`) are about 3 times what the benchmark machine measures, so a slower machine passes and a regression does not: lost writes, a slower write p99, load, query or aggregation, more memory, or more disk, CPU or files per idle collection.

`-data <dir>` keeps the loaded calls for the next runs: the first run loads them and writes `bench.json` (the calls and their day), the next ones read them again without loading. It goes with the read parts only (`-query`, `-aggs`, `-lookup`, `-concurrent`, all of them by default), the kept partition has a ten-year retention and PITR is off. `make bench-read` reads 2 million documents kept in `.bench/2m`: about 8 minutes the first time, then about 3. `-partition month` spreads the calls over six monthly partitions (five months ago up to now) and adds reads of a time window: up to the previous month, by the collection and by the `events-*` alias, and one month; `make bench-read-month` keeps them in `.bench/2m-month`.

From a checkout, `make bench` runs it on `bin/ndb`. Run it for any change to storage, the WAL, memory, or the read and write paths.

## Change

### Upgrade

Replace the binary and restart. Take a backup first. Within a major version the API only grows, so a newer daemon serves older SDKs. A major release (such as 1.0) changes routes or keys: upgrade the SDKs with the daemon, and read the migration table in the CHANGELOG.

### Version and token

`ndb -version` prints the version. To protect the TCP listener, start with `-token-file`; rotate the token by changing the file, restarting the daemon, and updating `NDB_TOKEN` in the apps. See [configuration.md](configuration.md#authentication).
