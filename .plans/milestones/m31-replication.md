# M31 — Replication: one owner, local reads

Status: not started · Depends on: M30 (databases in the URL) · Plan refs: the talk of 2026-11-24 to 26 (it replaces `../backlog/replication.md`)

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

- [ ] Test: a client with a `replicate` token reads every write of a
      database, in WAL order per collection, from a position; any other
      route with that token is `403`.
- [ ] Token scope `replicate` (`internal/server/scope.go`).
- [ ] `GET /_replication/stream?from=<position>` (database by the
      `Ndb-Database` header): NDJSON, one line per WAL record, plus the
      catalog changes (collection create, drop, truncate, rename, index
      and settings of a collection) and the blobs. A heartbeat line when
      there is no write.
- [ ] The position is per collection (each collection has its own WAL).
- [ ] The TTL sweep marks its tombstones; the stream skips them.
- [ ] `410 position_gone` when the WAL of a position is compacted.
- [ ] Snapshot for a resync: the backup code with no settings section
      (no key), and the position of each collection at the snapshot.

### E31.2 — The copy

- [ ] Test: a copy has the same documents as the owner after writes,
      deletes, `_tx`, `_bulk`, `_update_by_query`, catalog changes and
      blobs; a restart of either side loses nothing acknowledged.
- [ ] `NDB_REPLICA_OF` (env and `ndb.yml`): list of URLs, `?as=`, start
      errors (two sources into one database, a bad URL, another key
      fingerprint).
- [ ] Apply each record with the normal write path (WAL, fsync, indexes),
      then keep the cursor on disk. A record applied twice after a crash
      gives the same state (the `_v` of each document).
- [ ] Reconnect with backoff and jitter. On `410 position_gone`: a full
      resync from a snapshot, then the stream from its position.
- [ ] An empty copy (the first start) does the full resync alone, with no
      step by hand. Until it has caught up (resync done, lag under 1 s),
      it forwards reads to the owner too, so an app never reads a half
      copy. `GET /_replication` shows the progress (bytes, collections).
- [ ] A replicated database refuses a local write that is not forwarded
      (no write path around the owner).

### E31.3 — Forwarded writes and read your writes

- [ ] Test: through a copy, each write route gives the same answer as on
      the owner (shared spec cases run against a copy), and a read just
      after a write sees it.
- [ ] Forward every write route (`_doc`, `_bulk`, `_update`, `_expire`,
      `_persist`, `_lock`, `_unlock`, `_tx`, `*_by_query`, `PUT` and
      `DELETE` of a collection, the stream writes) to the owner, with the
      same token and `Ndb-Database`.
- [ ] The owner answers a forwarded write with its position (header
      `Ndb-Position`). The copy waits until it has applied that position,
      then answers. Time limit: the request timeout; on timeout the copy
      answers the owner's answer with a `Ndb-Stale: true` header.
- [ ] `watch`, `_ack`, stream group reads, `pending`, `claim`: forwarded.
- [ ] The owner is down: a forwarded write is `503 owner_unavailable`
      (new error code, in every SDK). Reads still work on the copy.

### E31.4 — Promote

- [ ] Test: promote a copy, write to it, start the old owner as a copy of
      the new one; no acknowledged write is lost.
- [ ] `POST /_replication/promote` (unix socket only, as `POST
      /_cluster/key`) and `ndb replication promote`: the copy stops
      pulling and accepts writes. The env `NDB_REPLICA_OF` must go at the
      next start (the log says so).
- [ ] A term number: promote adds 1. A copy refuses a stream with a lower
      term. An old owner that comes back as a copy with writes the new
      owner does not have: a start error that tells to resync
      (`ndb replication resync`), never a silent merge.

### E31.5 — Monitoring

- [ ] `GET /_replication`: the role of each database (`owner`, `copy`),
      and for a copy its source (token redacted), state (`streaming`,
      `resync`, `down`), lag in records and in seconds, last error. For an
      owner: the copies connected.
- [ ] Metrics: `ndb_replication_lag_seconds`, `ndb_replication_state`.
- [ ] `ndb replication status` (CLI).

### E31.6 — Docs, bench and the 1billion runbook

- [ ] `docs/dev/replication.md` (new, in `nav.ts`), `configuration.md`,
      `http/system.md` (the routes), `http/errors.md`, `security.md` (the
      `replicate` token, the key), `architecture.md` (no more "no
      replication"), `docs-site/llms-agents.md`, the deploy pages.
- [ ] `ndb bench`: a check for the lag of a copy under the `_bulk` load,
      and `get.id_us` on a copy (the same budget as on an owner).
- [ ] Runbook for 1billion, with no downtime:
      1. Start a copy on voodu (`NDB_REPLICA_OF` = the ndb of runa). Web
         and mta point to it: reads local, writes forwarded to runa.
      2. Promote the copy on voodu. Restart the ndb of runa as a copy of
         voodu (resync if needed). The mx points to it.
      3. Later, a VM in SP: one more copy, the same steps as 1.

## Exit criteria

- [ ] A copy on the same machine has the same documents as the owner after
      the shared spec cases and a random write mix (a compare test).
- [ ] Kill loops (`kill -9` of the owner, of the copy, a network cut) lose
      no acknowledged write and end with the same data on both.
- [ ] A read just after a forwarded write sees it (a test in each SDK).
- [ ] `get.id_us` on a copy is within the budget of an owner.
- [ ] Lag of a copy on the same host under the bench `_bulk` load: p99
      under 100 ms (measured; a miss is recorded).
- [ ] No `NDB_REPLICA_OF`: TestCompat passes and nothing on disk changes.
- [ ] Coverage of the new packages ≥ 90%.
- [ ] The runbook was run on 1billion.

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
