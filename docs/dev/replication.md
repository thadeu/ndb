# Replication

Replication keeps the same data on many ndb daemons, one on each VM. An app reads from the ndb of its own VM, and writes from any VM.

- **One owner per database.** Only the owner writes. The other daemons are **copies** of that database.
- **The copy pulls.** The config is on the copy (`NDB_REPLICA_OF`). The owner only knows a token with the `replicate` scope.
- **Reads are local** on a copy: a `GET` by `_id` on the unix socket, not a network round trip.
- **Writes go to the owner.** A copy sends each write to the owner, and answers after it has applied that write. So the next read of the client sees its write (read your writes). The SDKs do not change.
- **No `NDB_REPLICA_OF`, no change.** A daemon without it works as before, and it writes no new file.

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

Replication is not multi-master. Two owners of one database would lose a `$inc`, send a mail twice or make duplicates of a unique key. There is no automatic failover: a [promote](#promote) is one call, by a person.

## Set up

**On the owner**, add a `replicate` token to `ndb.yml`. It reads the change stream and the blobs of every database, and nothing else: any other route with it is `403 forbidden`.

```yaml
tokens:
  - name: copies
    file: /etc/ndb/copies.token
    replicate: true
```

The owner must listen on TCP (`listen.tcp`), on a private network (WireGuard, the voodu mesh). ndb does not depend on the network: it is one HTTP request.

**On the copy**, set `NDB_REPLICA_OF` (or `replica_of` in `ndb.yml`), and the same `NDB_ENCRYPTION_KEY` as the owner:

```bash
NDB_ENCRYPTION_KEY=<the key of the owner>
NDB_REPLICA_OF="tcp://:<token>@ndb-0.1billion.voodu:7070"
```

- The path is the database on the owner (none: `default`). `?as=` is the database on the copy (none: the same name).
- Many sources: a comma list. Each one goes into its own database:

  ```bash
  NDB_REPLICA_OF="tcp://:<token>@ndb-0.1billion.voodu:7070/1billion?as=1billion,tcp://:<token>@ndb-0.cronota.voodu:7070?as=cronota"
  ```

- `https://` is for an owner behind TLS.
- The other databases of the copy are its own: they are not replicated, and they take writes as usual.

**Start errors** (`ndb serve` stops with a message):

- a bad URL, or two sources into one database;
- the owner has another encryption key (the owner sends the fingerprint of its key, never the key);
- the owner has a lower [term](#promote) than the copy knows;
- the database of the copy has writes that the owner does not have (an old owner): run [`ndb replication resync`](#resync).

An owner that does not answer at the start is not an error: the copy starts, and it connects when the owner is back.

## What a copy does

1. **First start.** An empty copy reads the whole history of the owner, with no step by hand. When a WAL file of the owner was deleted (`wal_retention`), it takes a snapshot of that collection, then reads the history after the snapshot.
2. **Until it caught up** (lag under one heartbeat), a copy sends the reads to the owner too, so an app never reads a half copy. After that its reads are local, also when the owner is down.
3. **Each record** of the owner is written with the normal write path of the copy: WAL, `fsync`, indexes. The copy keeps the position of each collection in `replication.json`, in the dir of the database. A record applied twice after a crash gives the same state (the `_v` of each document).
4. **Catalog changes** go to the copy too: create, drop, truncate, rename, indexes and the settings of a collection. Blobs are copied before the document that uses them.
5. **A cut connection** comes back with backoff (100 ms, up to 5 s, with jitter). The copy goes on from its positions.

| On a copy | Where it runs |
|---|---|
| `GET` by `_id`, `_search`, `_count`, `_aggregate`, `GET /{coll}` | local (on the owner until the copy caught up once) |
| `_doc`, `_bulk`, `_update`, `_expire`, `_persist`, `_lock`, `_unlock`, `_tx`, `*_by_query`, `PUT` and `DELETE` of a collection, `_indexes`, `_truncate`, `_rename`, `POST /_blobs` | the owner; the copy answers after it has the write |
| `_watch`, `_ack`, every `_streams` route, `/{coll}/_restore` | the owner (one consumer per group in the whole system) |
| `_compact`, `_stats`, `_pitr`, `_quarantine`, the routes of the daemon | local |
| `POST /_restore` of the database | refused (`403`): restore on the owner |

- **Read your writes.** The owner answers a forwarded write with `Ndb-Position` (the last tx of the collection, and the catalog version after a change of a definition). The copy waits until it has applied that position, up to 5 s. After 5 s it gives the answer of the owner with `Ndb-Stale: true`.
- **The owner is down:** a write on the copy is `503 owner_unavailable`. Reads still work. Retry the write.
- **Tokens.** A write keeps the token of the client. A write on the unix socket of the copy (no token) goes with the token of `NDB_REPLICA_OF`: then that token must be a full token, or the owner refuses the write. Over TCP, give the copy the same `tokens` as the owner.
- **TTL is local.** The deletes of the TTL sweep are not replicated: each node runs its own sweep on the same data.
- **Idle collections.** While a copy reads the stream, the collections of the owner do not sleep (`idle_close`).

## Promote

A promote makes a copy the owner. It is never automatic: a promote on a short network cut would make two owners.

```bash
ndb replication promote                 # the default database
ndb replication promote -database cronota
```

- It runs on the unix socket only (`POST /_replication/promote`, with `Ndb-Database`).
- The copy stops the pull and takes writes. Remove the database from `NDB_REPLICA_OF` before the next start: until then the daemon logs a warning and does not pull again.
- The **term** goes up by one. A copy refuses an owner with a lower term, so an old owner can not take its copies back.
- **The old owner comes back as a copy** of the new one: set `NDB_REPLICA_OF` on it to the new owner. If it has no write that the new owner does not have, it goes on from the position of the promote. Else it does not start the pull, and it tells to resync. It never merges.

Stop the old owner before the promote, so it takes no write after it. See [Limits](#limits) for `watch` consumers and stream groups.

## Resync

```bash
ndb replication resync [-database NAME]
```

It drops every collection of the database on the copy and its positions, then copies the owner again from the start. Unix socket only (`POST /_replication/resync`).

## Encryption

The WAL holds encrypted fields already sealed, so a copy gets them sealed. A copy that serves reads needs the same `NDB_ENCRYPTION_KEY` as its owner (same app, same key). The key never goes over the network: a snapshot has no settings.

An owner whose key is kept in its data dir (no `NDB_ENCRYPTION_KEY`) logs a warning when a copy connects: if its disk is lost, the copies have the data and no key. Use `NDB_ENCRYPTION_KEY` from a secret store.

## Monitoring

`GET /_replication` (and `ndb replication status`) gives the role of each database:

```json
{"databases":[
  {"database":"default","role":"owner","term":0,"copies":[{"database":"default","addr":"10.0.0.2","since":"2026-11-27T10:00:00Z"}]},
  {"database":"1billion","role":"copy","term":0,"source":"tcp://:***@ndb-0.1billion.voodu:7070/1billion","state":"streaming","synced":true,"lag_records":0,"lag_seconds":0}
]}
```

- `state` of a copy: `starting`, `streaming`, `resync` (with `resync`: `collections`, `done`, `bytes`), `down` (with `last_error`).
- The token of the source is never shown.
- Metrics: `ndb_replication_lag_seconds`, `ndb_replication_lag_records` and `ndb_replication_state` (one series per state, `1` for the current one), per database.

## Routes

| Route | Who | What |
|---|---|---|
| `GET /_replication` | admin token, unix socket | the role of each database |
| `GET /_replication/stream?from=a=12,b=3` | `replicate` or full token | the change stream of the database of `Ndb-Database` |
| `GET /_replication/snapshot?collection=a` | `replicate` or full token | the live documents of one collection at one tx |
| `POST /_replication/promote` | unix socket | make this copy the owner |
| `POST /_replication/resync` | unix socket | copy the owner again |

See [System routes](http/system.md#replication) for the lines of the stream.

## Runbook: 1billion with no downtime

Today the owner is the ndb of runa.

1. **A copy on voodu.** Start an ndb on voodu with `NDB_REPLICA_OF` = the ndb of runa, and the same `NDB_ENCRYPTION_KEY`. Wait for `synced: true` in `ndb replication status`. Point web and mta to it: reads are local, writes go to runa.
2. **Move the owner to voodu.** Stop the ndb of runa (writes are `503 owner_unavailable` for some seconds; the apps retry). Run `ndb replication promote` on voodu. Start the ndb of runa with `NDB_REPLICA_OF` = the ndb of voodu (a resync if it tells to). Point the mx to it.
3. **Later, a VM in SP:** one more copy, the same steps as 1.

## Limits

- Only engine-mode writes replicate. A direct-mode collection (the tailer) stays on its node.
- `watch` positions (`_ack`) and stream groups (the PEL) live on the owner, and they are not replicated. After a promote, a `watch` consumer has no position on the new owner, so it reads from the start of the history (delivery is at least once), and a stream group does not exist there: make it again (`PUT .../groups/{g}` with `id`). Do the promote when the consumers are idle.
- The console view of replication is not there yet: use `GET /_replication`.
