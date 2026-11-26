# Backlog: replication

Status: in design (talk of 2026-11-24 and 25). Not a milestone yet. It
needs M30 (databases in the URL) first. Each item has the reason, so the
talk does not start from zero.

## Goal

An app reads from an ndb on its own VM (about 10x faster than over the
network), and the data of each app is also on other VMs.

## Decided

- **One owner per database.** Only the owner accepts writes. Postgres and
  Elasticsearch both do this. Multi-master with last-writer-wins was
  refused as the default: in 1billion it loses a credit (`$inc` in
  `credits.rb`), sends a mail twice (`Store.Claim` of the MTA) and makes
  duplicates (unique keys, `find_or_create_by!`).
- **Pull, from the copy.** The copy has the env; the owner knows nothing
  except a replication token (it can only read the WAL stream).
  ```
  NDB_REPLICA_OF="tcp://:<token>@ndb-0.1billion.voodu:7070?as=1billion,tcp://:<token>@ndb-0.cronota.voodu:7070?as=cronota"
  ```
  Path = the database on the owner (none = `default`); `?as=` = the
  database on the copy. Two sources into one database: a start error.
- **Transport:** HTTP on the TCP listener, one long connection, NDJSON
  stream, heartbeat. The copy keeps its cursor on disk; reconnect with
  backoff + jitter; full resync from a snapshot (the backup code) when the
  WAL it needs is gone. The voodu mesh (WireGuard) is the network; ndb does
  not depend on it.
- **Writes on a copy are forwarded** to the owner. The answer has the WAL
  position; the copy answers the next read of that client only after it
  has applied that position (read your writes). The app does not change.
- **TTL is local** to each node. Expiry makes no replicated delete.
- **Promote** is one call on the copy (`POST /_replication/promote`), never
  automatic: an automatic promote on a short network cut makes two
  owners. It is also the tool to move an owner to another VM.
- **Collections of the copy's own** (`default` or other databases of the
  copy) are not replicated.

## Open

1. **Streams and `watch` on a copy.** A consumer on each node would
   process each event twice. Proposal: `watch` and stream groups work only
   on the owner (a copy forwards, or answers `409 not_owner`).
2. **Distance.** SP to Frankfurt is ~100 ms one way, ~200 ms RTT. Reads
   stay local; a forwarded write costs ~300 ms (RTT + the change back).
   A writer far from the owner (the MX in Germany) should own its own
   database (crossed owners, no loop: one owner per database). It is
   complex; wait for a real case.
3. **Many daemons on one VM** (one per app): memory of each, with
   `memory_limit`.
4. **Per-collection `lww` mode** (step 2) for append-only or cache data,
   if a real case comes.
5. **Monitoring:** `GET /_replication` with the lag per source, in the
   console and in the metrics.

## First step when it starts

One owner, one copy, `default` → `?as=name`, forward of writes, read your
writes, kill loops in tests (`testing.Short` skip).
