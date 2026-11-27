# M31 — Replication: one owner, local reads

Status: in progress (built on `feat/replication`; the runbook on 1billion is left) · Depends on: M30 (databases in the URL) · Plan refs: the talk of 2026-11-24 to 26 (it replaces `../backlog/replication.md`)

## Goal

An app reads from an ndb on its own VM, and writes from any VM, with the
same data on every VM.

The first case is 1billion:

```
  VM voodu (web, mta)                         VM runa (mx)
┌──────────────────────┐                ┌──────────────────────┐
│ web, mta             │                │ mx                   │
│  │ read   local      │                │  │ read   local      │
│  ▼ write  local      │                │  ▼ write  ──┐        │
│ ndb (owner)          │◄── replication ┤ ndb (copy)  │        │
│                      │    (pull)      │             │        │
│                      │◄───────────────┼─────────────┘        │
│                      │  write forwarded to the owner         │
└──────────────────────┘                └──────────────────────┘
```

- Reads are local on each VM (`GET` by `_id` about 26 µs on the unix
  socket, not a network round trip).
- Writes on the owner are local. A write on a copy is forwarded to the
  owner: it costs one round trip, as all writes do today.
- The app does not change. Its `NDB_URL` points to the ndb of its own VM.
- A new VM (for example a VM in SP for the mx) is one more copy with the
  same env. Only the WireGuard link (`vd wire`) is new.

## Compatibility (rule of this milestone)

1billion runs on ndb as its primary database.

- A daemon with no `NDB_REPLICA_OF` works as today. No new file in the
  data dir until a copy connects.
- The data dir of an owner does not change format. A copy is a normal data
  dir: `ndb serve` with no `NDB_REPLICA_OF` opens it (that is how promote
  works).
- Every route and key of today stays. Replication adds routes and keys
  only (HTTP API 1.x).
- The SDKs of 1.27 work against a copy with no change.
- The golden test (TestCompat) passes.

## Decided

- **One owner per database.** Only the owner accepts writes. Multi-master
  with last-writer-wins is refused: in 1billion it loses a credit (`$inc`
  in `credits.rb`), sends a mail twice (`Store.Claim` of the MTA) and makes
  duplicates (unique keys, `find_or_create_by!`).
- **The copy pulls.** The config is on the copy. The owner knows only a
  token with the `replicate` scope (it can read the change stream and
  nothing else).

  ```
  NDB_REPLICA_OF="tcp://:<token>@ndb-0.1billion.voodu:7070"
  NDB_REPLICA_OF="tcp://:<token>@ndb-0.1billion.voodu:7070/1billion?as=1billion,tcp://:<token>@ndb-0.cronota.voodu:7070?as=cronota"
  ```

  The path is the database on the owner (none = `default`). `?as=` is the
  database on the copy (none = the same name). Two sources into one
  database: a start error.
- **Transport:** HTTP on the TCP listener of the owner, one long request,
  NDJSON stream, heartbeat. The voodu mesh (WireGuard) is the network; ndb
  does not depend on it.
- **Forwarded writes, read your writes.** A copy forwards each write to the
  owner, and answers it only after it has applied the position of that
  write. So the next read of the client sees its write. The SDK does not
  change.
- **TTL is local** to each node. The tombstones of the TTL sweep are not
  replicated: each node runs its own sweep on the same data.
- **`watch` and stream groups work on the owner only.** A copy forwards
  them. One consumer per group in the whole system, so no event is
  processed twice.
- **Promote** is one call on the copy, never automatic: an automatic
  promote on a short network cut makes two owners. It is also how an owner
  moves to another VM.
- **Encryption:** the WAL holds encrypted fields already sealed, so a copy
  gets them sealed. A copy that serves reads needs the same
  `NDB_ENCRYPTION_KEY` as its owner (same app, same key). The owner sends
  the key fingerprint in the handshake; another fingerprint is a start
  error on the copy. The key never goes over the network: a snapshot for
  a resync has no settings section.
- **An owner with the key kept in the data dir** logs a `warn` when a copy
  connects: if its disk is lost, the copies have the data and no key.
  Production uses `NDB_ENCRYPTION_KEY` from a secret store (1billion does).
- **Collections of the copy's own** (the other databases of the copy) are
  not replicated, and they accept writes as usual.

## Epics

Each epic starts with its failing tests. Kill loops and network cuts are
heavy tests (`if testing.Short() { t.Skip(...) }`).

### E31.1 — Change stream on the owner

- [x] Test: a client with a `replicate` token reads every write of a
      database, in WAL order per collection, from a position; any other
      route with that token is `403`.
- [x] Token scope `replicate` (`internal/server/scope.go`).
- [x] `GET /_replication/stream?from=<position>` (database by the
      `Ndb-Database` header): NDJSON, one line per WAL record, plus the
      catalog changes (collection create, drop, truncate, rename, index
      and settings of a collection) and the blobs. A heartbeat line when
      there is no write.
- [x] The position is per collection (each collection has its own WAL).
- [x] The TTL sweep marks its tombstones; the stream skips them.
- [x] `410 position_gone` when the WAL of a position is compacted.
- [x] Snapshot for a resync: the backup code with no settings section
      (no key), and the position of each collection at the snapshot.

Choices (E31.1):

- **One stream per database, a position per collection.** `from` is `a=12,b=3`: the owner tx of the last record the copy applied. A collection that is not in `from` starts at its first record. The stream reuses the watch feed of each collection (history from the WAL, then the live part), grouped back into whole records, so a `_tx` is applied in one step on the copy.
- **The TTL sweep is left out by the flag it has already** (`_exp` of the del op). A record of the sweep is sent with no ops, so the position of the copy still moves.
- **Catalog changes** are a `catalog` line with every definition, sent at the start, when the catalog version moves (any 2xx admin route of a collection on the owner), and on each heartbeat when the definitions changed. The copy diffs it: create or merge, drop the indexes that went, drop the collections that went. A truncate comes as the truncate record of the WAL. A rename is a drop plus a new collection that the copy reads from its first record.
- **Snapshot:** `GET /_replication/snapshot?collection=` uses `WALMarkSnapshot` (the snapshot code of the backup) and sends the live documents as put records, then the tx. It is per collection, not a whole backup file, so a resync of one collection does not copy the others. No settings section.
- **`position_gone`** is answered at the start (`410` with `Ndb-Gone`), and as a `gone` line when a collection loses its history while the stream runs (a restore moved it back: its last tx is under the position).
- **Cost on the owner:** while a copy reads the stream, the collections of the owner keep a feed subscriber, so they do not sleep (`idle_close`).

### E31.2 — The copy

- [x] Test: a copy has the same documents as the owner after writes,
      deletes, `_tx`, `_bulk`, `_update_by_query`, catalog changes and
      blobs; a restart of either side loses nothing acknowledged.
- [x] `NDB_REPLICA_OF` (env and `ndb.yml`): list of URLs, `?as=`, start
      errors (two sources into one database, a bad URL, another key
      fingerprint).
- [x] Apply each record with the normal write path (WAL, fsync, indexes),
      then keep the cursor on disk. A record applied twice after a crash
      gives the same state (the `_v` of each document).
- [x] Reconnect with backoff and jitter. On `410 position_gone`: a full
      resync from a snapshot, then the stream from its position.
- [x] An empty copy (the first start) does the full resync alone, with no
      step by hand. Until it has caught up (resync done, lag under 1 s),
      it forwards reads to the owner too, so an app never reads a half
      copy. `GET /_replication` shows the progress (bytes, collections).
- [x] A replicated database refuses a local write that is not forwarded
      (no write path around the owner).

Choices (E31.2):

- **Apply** is a new writer request (`collection.Apply`): the ops of the owner as they are (same `_id`, `_v`, bytes; no encryption, blob or unique step), in one WAL record. A put is skipped when the live doc has the same or a newer `_v`; a del when the doc is not live or newer. A replay of a run of records from an older position ends in the same state.
- **Position on disk:** `replication.json` in the dir of the database (written only by a copy). It is saved at most every 200 ms, after a truncate, a catalog change and a snapshot; a crash replays from the saved position (idempotent, see above).
- **Blobs:** before a put, the copy fetches each blob that it does not have (`GET /_blobs/{ref}`, allowed to the replicate token) and checks its hash.
- **Caught up** = a heartbeat (every second) shows no lag. It is kept in the state (`synced`), so after a restart reads stay local even with the owner down. Until then, and during a resync, reads go to the owner.
- **Start errors:** `ndb serve` waits up to 5 s for the first hello of each owner. Another key, a lower term or an old owner with its own writes stop the start; an owner that does not answer does not.
- **No local write path around the owner:** the gate of the server sends every write route of a copy database to the owner; `POST /_restore` of a copy database is `403`.

### E31.3 — Forwarded writes and read your writes

- [x] Test: through a copy, each write route gives the same answer as on
      the owner (shared spec cases run against a copy), and a read just
      after a write sees it.
- [x] Forward every write route (`_doc`, `_bulk`, `_update`, `_expire`,
      `_persist`, `_lock`, `_unlock`, `_tx`, `*_by_query`, `PUT` and
      `DELETE` of a collection, the stream writes) to the owner, with the
      same token and `Ndb-Database`.
- [x] The owner answers a forwarded write with its position (header
      `Ndb-Position`). The copy waits until it has applied that position,
      then answers. Time limit: the request timeout; on timeout the copy
      answers the owner's answer with a `Ndb-Stale: true` header.
- [x] `watch`, `_ack`, stream group reads, `pending`, `claim`: forwarded.
- [x] The owner is down: a forwarded write is `503 owner_unavailable`
      (new error code, in every SDK). Reads still work on the copy.

Choices (E31.3):

- **The gate** is a middleware after the token check. A request goes local, to the owner and waits, to the owner with no wait (`_watch`, `_ack`, `_streams`, `/{coll}/_restore`, `POST /_blobs`), or is refused. It is a `httputil.ReverseProxy` (streaming answers flushed), so a watch through a copy works.
- **`Ndb-Position`** is set only when the request has `Ndb-Forwarded: 1`: the last tx of the collection (and of its partitions) after the write, and `_catalog=<n>` after an admin route. The copy waits for every position. Time limit: 5 s (no request timeout exists in the daemon); then `Ndb-Stale: true`.
- **Token of a forwarded write:** the token of the client. A request on the unix socket of the copy has none, so it goes with the token of `NDB_REPLICA_OF`; a replicate token can not write, so a copy with unix-socket apps needs a full token in its URL. Documented.
- Shared spec cases against a copy: not added. The SDK tests write and read through a real copy instead (Go, Ruby, TS), and the server tests run every write route through a copy.

### E31.4 — Promote

- [x] Test: promote a copy, write to it, start the old owner as a copy of
      the new one; no acknowledged write is lost.
- [x] `POST /_replication/promote` (unix socket only, as `POST
      /_cluster/key`) and `ndb replication promote`: the copy stops
      pulling and accepts writes. The env `NDB_REPLICA_OF` must go at the
      next start (the log says so).
- [x] A term number: promote adds 1. A copy refuses a stream with a lower
      term. An old owner that comes back as a copy with writes the new
      owner does not have: a start error that tells to resync
      (`ndb replication resync`), never a silent merge.

Choices (E31.4):

- **Term** lives in `replication.json`. An owner that never was a copy has term 0 and no file. Promote writes role `owner`, term + 1, and for each collection the position of the old owner (`from`) and its own last tx (`at`). The hello of an owner carries them.
- **The old owner as a copy:** for each collection with data, its last tx must be `from`; then it goes on from `at`. Else it is `down` with "run ndb replication resync" (a start error at `ndb serve`). Never a merge.
- **A promoted database** with `NDB_REPLICA_OF` still set logs a warning and does not pull.
- **`ndb replication resync`** (`POST /_replication/resync`, unix socket): drops the collections of the database and its state, then copies again.

### E31.5 — Monitoring

- [x] `GET /_replication`: the role of each database (`owner`, `copy`),
      and for a copy its source (token redacted), state (`streaming`,
      `resync`, `down`), lag in records and in seconds, last error. For an
      owner: the copies connected.
- [x] Metrics: `ndb_replication_lag_seconds`, `ndb_replication_state`.
- [x] `ndb replication status` (CLI).

Choices (E31.5): one more metric, `ndb_replication_lag_records`. `ndb_replication_state` is one series per state with value 1 for the current one. The lag in seconds is the time since the copy first saw that it is behind.

### E31.6 — Docs, bench and the 1billion runbook

- [x] `docs/dev/replication.md` (new, in `nav.ts`), `configuration.md`,
      `http/system.md` (the routes), `http/errors.md`, `security.md` (the
      `replicate` token, the key), `architecture.md` (no more "no
      replication"), `docs-site/llms-agents.md`, the deploy pages.
- [x] `ndb bench`: a check for the lag of a copy under the `_bulk` load,
      and `get.id_us` on a copy (the same budget as on an owner).
- [x] Runbook for 1billion, with no downtime:
      1. Start a copy on voodu (`NDB_REPLICA_OF` = the ndb of runa). Web
         and mta point to it: reads local, writes forwarded to runa.
      2. Promote the copy on voodu. Restart the ndb of runa as a copy of
         voodu (resync if needed). The mx points to it.
      3. Later, a VM in SP: one more copy, the same steps as 1.

Bench (`ndb bench -replica`, darwin/arm64, 15 CPU, 2026-11-27): lag of a copy on the same host under `_bulk` (1000 docs per call, about 84 000 docs/s): p50 0.2 ms, **p99 18.3 ms**, max 19.2 ms (166 probes); `GET` by `_id` on the copy: **p50 29 µs**, p99 121 µs. New checks: `replica.lag_p99_ms` ≤ 100 ms, `replica.get_id_us` ≤ 60 µs: PASS.

## Exit criteria

- [x] A copy on the same machine has the same documents as the owner after
      the shared spec cases and a random write mix (a compare test). Done
      with every write route and a random upsert/delete mix; the spec cases
      themselves are not run against a copy.
- [x] Kill loops (`kill -9` of the owner, of the copy, a network cut) lose
      no acknowledged write and end with the same data on both
      (`TestKillLoopLosesNoAckedWrite`, 30 rounds, real daemons).
- [x] A read just after a forwarded write sees it (a test in each SDK).
- [x] `get.id_us` on a copy is within the budget of an owner.
- [x] Lag of a copy on the same host under the bench `_bulk` load: p99
      under 100 ms (measured; a miss is recorded).
- [x] No `NDB_REPLICA_OF`: TestCompat passes and nothing on disk changes.
- [x] Coverage of the new packages ≥ 90% (`internal/replication`: 91%).
- [ ] The runbook was run on 1billion.

Left: the runbook on 1billion (a person runs it). Known gaps: `watch` positions and stream groups are not replicated, so after a promote a watch consumer starts from the beginning of the history and a stream group must be made again (documented in `docs/dev/replication.md#limits`).

## Out of scope

- Automatic promote (failover).
- Multi-master, and a per-collection last-writer-wins mode for append-only
  data (only if a real case comes).
- Crossed owners (each VM owns one database): it works with this design,
  but waits for a real case.
- One big ndb with copies of many apps and a key per database (a copy
  with no key that keeps sealed values only).
- Direct mode (the tailer) as a source: only engine-mode writes replicate.
- The console view of the replication (later; `GET /_replication` is the
  base).
