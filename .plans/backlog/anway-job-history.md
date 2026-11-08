# Anway job history on ndb

> Status: proposal (2026-11-08). For the anway repo: copy it to its local
> `.plans/` as `NNN-job-history.md` (anway's `.plans/` is not in git).

## Why

Anway keeps a job until it ends, then removes it (`ACK`), and keeps only
per-minute counters (`metrics`, 24 h to 60 d). Nobody can answer "what
happened to job X", "which `email.send` failed yesterday for this account",
or "how long does `invoice.render` take", once the jobs are gone.

The queue stays on bbolt: a queue needs an atomic, ordered "take the next
job and reserve it" across buckets, embedded, with no network hop. ndb has
no ordered pop and no transaction across collections. ndb is the place for
what comes **after** a job: the searchable history.

## Decisions

1. **Anway writes NDJSON files; ndb tails them (direct mode).** Anway never
   waits for ndb and does not need it to run. If ndb is down, the files
   grow; ndb reads them when it comes back (it goes on from its last
   checkpoint, see ndb 1.2 direct mode). A delay of seconds is fine for a
   history.
2. **One document per job, `_id` = `jid`.** In direct mode the last line of
   an `_id` wins, so a job that retries and then succeeds has one document
   in its final state, with the number of attempts.
3. **Only end states make a line:** `done` (ACK), `retry` (failed, will run
   again), `dead`, `discarded` (`retry: 0`), `killed` (MUTATE or the HTTP
   API), `expired` (reservation expired, counted as a failure). Not PUSH or
   FETCH: that is the queue's job and doubles the volume.
4. **The history never slows the job path.** Lines go through a bounded
   channel to one writer goroutine. When the channel is full the line is
   dropped and counted (`history_dropped_total`), never blocking an ACK.
5. **No fsync per line.** The history is not the source of truth: a crash
   may lose the last flush interval (200 ms). The queue keeps NFR1.
6. **Files rotate by day, anway deletes them after the retention.** ndb
   never deletes the files of an app.
7. **Read path: the anway HTTP API proxies to ndb** (`/api/v1/history`), so
   the web UI keeps one auth surface. ndb's address is config (`NDB_URL`);
   with it unset the History tab is hidden.

## Line format

One JSON object per line, ≤ 64 KiB (`args` and `custom` over 8 KiB are
replaced by `{"truncated": true, "bytes": N}`).

```json
{"_id":"j_8f2c","jid":"j_8f2c","jobtype":"email.send","queue":"mailers",
 "state":"done","attempts":2,"args":[42,"welcome"],"custom":{"account":"a1"},
 "created_at":"2026-11-08T10:00:00Z","started_at":"2026-11-08T10:00:03Z",
 "ended_at":"2026-11-08T10:00:04.120Z","duration_ms":1120,"wid":"w-17",
 "error":null}
```

- `ended_at` is the time field (RFC 3339, UTC).
- `error` on `retry`, `dead` and `expired`: `{"type","message"}`, no
  backtrace (size).
- `attempts` is `retry_count + 1` at the end.

## Anway changes

| Part | Change |
|---|---|
| `internal/history` (new) | `Writer`: bounded channel, one goroutine, `bufio` flushed every 200 ms and at close, file `history-YYYY-MM-DD.ndjson` in `HISTORY_DIR`, rotation at UTC midnight, deletion of files older than `HISTORY_RETENTION`, `dropped` counter |
| `internal/storage` | `Ack`, `Fail`, the reservation reaper and the kill path return the ended job (jobtype, args, custom, timestamps, reserve start, wid), not only the queue |
| `internal/manager` | after each end state, `history.Record(event)`; never in the bolt transaction |
| config | `HISTORY_DIR` (empty = off), `HISTORY_RETENTION` (default `30d`), `NDB_URL` (read path) |
| `internal/httpapi` | `GET /api/v1/history?...` and `GET /api/v1/history/{jid}`, forwarded to ndb `_search` / `_doc` |
| metrics | `history_written_total`, `history_dropped_total` in `/api/v1/overview` |
| docs | `docs/dev/history.md`, configuration table, docker compose with ndb |

No env var or code name contains the product name (NFR5).

## ndb collection

```yaml
collections:
  job_history:
    write: direct
    path: /data/history/*.ndjson
    time: ended_at
    retention: 30d
    indexes: [jobtype, queue, state]
    max_line_size: 64KiB
```

- Memory stays bounded (direct mode is disk first since ndb 1.2).
- `_watch` with `{"query":{"state":"dead"}}` can drive alerts later.

## Web UI (anway-webui)

- **History** tab: filters (jobtype, queue, state, time range, a field of
  `custom`), newest first, 50 a page (ndb cursor).
- Job page: `GET /api/v1/history/{jid}`, when the job is no longer in the
  queue.
- Charts over the range: done and failed per hour per jobtype
  (`$dateTrunc` + `$group`), p50 and max `duration_ms` per jobtype.

## Anway Cloud

Open decision. Each server is a single-tenant pod: an ndb sidecar per
server costs ~100–200 MB of RAM, which a Hobby size may not pay for. Start
self-hosted; for Cloud, offer the history on paid sizes only, or one shared
ndb per node with one collection per server.

## Order of work (tests first)

1. `internal/history` writer: rotation, retention delete, drop counter,
   complete lines after a kill -9 (a test reads the file back).
2. Storage returns the ended job; manager records every end state (one test
   per state: done, retry then done, dead, discarded, killed, expired).
3. Config and docs; docker compose anway + ndb.
4. Integration test: push and run jobs, find them in ndb by `jid` and by
   `state` within 5 s.
5. `/api/v1/history` proxy (with `NDB_URL` unset: 404 `history_off`).
6. Web UI History tab and job page.
7. Cloud decision.

## Exit criteria

- `cmd/loadtest` at 5,000 jobs/s: no measurable drop with the history on;
  `history_dropped_total` stays 0.
- A finished job is in ndb within 1 s (fsnotify), 5 s at worst (poll).
- ndb down for an hour, then up: every line of that hour is in ndb.
- ndb RSS stays under 512 MB with 30 days of history.
