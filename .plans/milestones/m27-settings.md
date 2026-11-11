# M27 — Server settings and the encryption key

Status: not started · Depends on: nothing · Needed by: [M28](m28-encrypted-fields.md)

## Goal

Some settings live in the daemon and are read and changed over the API,
with no file and no restart, as in Elasticsearch (`_cluster/settings`).
The first one is the **encryption key** of the daemon: M28 uses it to
encrypt fields and to make blind indexes.

```
GET /_settings        the settings, with no secret in the answer
PUT /_settings        change the settings that can change
```

## Name

`/_settings`, not `/_config` or `/_server`:

- Elasticsearch calls them settings (`GET /_cluster/settings`, and
  `GET /{index}/_settings` for one index). A later `GET /{coll}/_settings`
  fits the same word.
- "config" is `ndb.yml` in every page of the docs. A route `_config` would
  read as "the file over HTTP", and it is not: a setting is stored in the
  data dir and changed while the daemon runs.

`ndb.yml` and the flags stay as they are: where the daemon listens, the
data dir and the limits. A setting that both have (later) is decided by
the file when the file sets it.

## The encryption key

One master key of 32 bytes per daemon. The daemon looks for it in this
order, once, at start:

1. the env `NDB_ENCRYPTION_KEY` (or `encryption_key_file` of `ndb.yml`);
2. the key stored by the daemon;
3. none: the daemon makes one and stores it.

So a user who does nothing has a key, and a user who wants to hold the key
gives it. M28 derives two keys from it (one to encrypt, one for the blind
indexes), so the master key never touches a document.

### Where the stored key lives, and why

The stored key is a file of its own, `<data-dir>/keys/master.key`, mode
`0600`. It is **not** in the WAL, in `catalog.bolt` or in a segment.

- **`ndb backup` does not hold the key.** A backup that leaks then has
  encrypted fields and no key. This is the main thing the encryption is
  for. `ndb backup` says so each time, and `ndb restore` on a machine with
  no key needs `-encryption-key-file`.
- **`GET /_settings` never gives the key.** It gives its fingerprint (the
  first bytes of its SHA-256), where it came from (`env`, `file`, `stored`)
  and when it was made.
- **The user can take the key out, to keep it:** `ndb key export`, on the
  machine of the daemon only (the unix socket; refused over TCP, as
  `POST /_backup` is). Until the key was exported once, `GET /_settings`
  and `ndb stats` say `"exported": false`.

### What this protects, and what it does not

| Leak | A key the daemon made | A key in the env (not on the disk) |
|---|---|---|
| a backup file | protected | protected |
| a copy of the WAL or of the segments, without `keys/` | protected | protected |
| the whole data dir, or the disk of the VM | **not protected**: the key is next to the data | protected |
| a caller of the API with a read token | sees what its token allows (M28) | the same |
| the memory of the daemon | not protected | not protected |

The honest rule for the docs: a key the daemon made protects the backups
and the files that leave the machine. To protect the disk too, give the
key in the env from a secret store.

### The risk of a key nobody saved

A daemon that made its key, a disk that dies, and a backup: the encrypted
fields of that backup cannot be read, ever. So:

- the first start that makes a key logs a WARN with what to run
  (`ndb key export`), never the key itself;
- `/_health` stays `ok` (the data is served), but `ndb stats` and
  `GET /_settings` show `"exported": false` until the export;
- `ndb backup` prints the same warning while the key was never exported.

## `PUT /_settings`

Only what can change at run time. In this milestone:

- `{"encryption": {"exported": true}}` is not settable (the export sets
  it).
- Key rotation is **not** in this milestone (a new key needs every
  encrypted field written again); the body has a version for it:
  `"key_id": 1`.

So the route exists and answers `400` for a key that cannot change. The
log level keeps its route, `PUT /_log`; `GET /_settings` shows it too, so
one call tells the state of the daemon.

## Decisions to confirm

1. The name `/_settings`.
2. The key is a file in `<data-dir>/keys/`, out of the backup. (The other
   way, the key inside the stored settings and inside the backup, makes a
   restore need nothing, and makes a leaked backup readable.)
3. `GET /_settings` never returns the key; `ndb key export` on the unix
   socket is the only way out.
4. The env is `NDB_ENCRYPTION_KEY`: 64 hex characters, or base64 of 32
   bytes.
5. No rotation yet.

## Epics

### E27.1 Daemon
- [ ] tests first: the order env, file, stored, made; the stored key survives a restart; a wrong size is a start error; `GET /_settings` has no secret (a test greps the answer for the key); `ndb key export` over the unix socket, refused over TCP and to a scoped token; the backup has no key; restore with and without the key file; the fingerprint
- [ ] `internal/keys`: load, make, store (`0600`, fsync, atomic), derive (HKDF) the keys of M28
- [ ] `server`: `GET` and `PUT /_settings`, scope (admin)
- [ ] `cmd/ndb`: `ndb key export`, `ndb key fingerprint`; the warnings of start, stats and backup

### E27.2 SDKs and docs
- [ ] Go, Ruby, TypeScript: `settings()` (read)
- [ ] `docs/dev/security.md` (the table above), `configuration.md`, `operations.md` (backup and restore with a key), `http/system.md`, `llms-agents.md`, CHANGELOG
- [ ] the console: the fingerprint and the "not exported" warning

## Exit criteria

- A daemon started with nothing has a key; a second start has the same one.
- No answer of the API and no log line has the key (tested).
- A backup restored on another machine works with the key file, and says what is missing without it.

## Out of scope

- Encrypted fields and blind indexes ([M28](m28-encrypted-fields.md)).
- Key rotation, more than one key, a KMS.
- Encryption of the whole WAL or of the segments.
