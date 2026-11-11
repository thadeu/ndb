# M27 — Server settings and the encryption key

Status: not started · Depends on: nothing · Needed by: [M28](m28-encrypted-fields.md)

## Goal

Some settings live in the daemon and are read and changed over the API,
with no file and no restart, as in Elasticsearch. The first one is the
**encryption key** of the daemon: M28 uses it to encrypt fields and to
make blind indexes.

```
GET /_cluster/settings     the settings, the key masked
PUT /_cluster/settings     change the settings that can change
```

## Decisions (confirmed)

1. **`/_cluster/settings`**, the path of Elasticsearch, so a cluster later
   keeps it. A later `GET /{coll}/_settings` is the settings of one
   collection. `ndb.yml` and the flags stay as they are.
2. **The settings are in the WAL, and so in the backups**, the key with
   them. A restore needs nothing more.
3. **The key is shown masked:** the first 4 and the last 4 hex characters
   (`a1b2****…****9f0e`), never the whole key. That shows 32 of its 256
   bits.
4. **`NDB_ENCRYPTION_KEY`**: 64 hex characters.
5. **No rotation yet.** Later: a setting with the active key, and the old
   keys kept to read old values. The stored format has the version for it
   (`enc:v1:`).

## The encryption key

The daemon looks for it in this order, at start:

1. the env `NDB_ENCRYPTION_KEY`;
2. the key in the settings;
3. none: the daemon makes one (32 random bytes) and writes it to the
   settings.

An env key is used as it is and is not written to the settings. An env key
that is not the one of the settings, when the settings have one, is a
start error (the encrypted values would not be readable).

### Where the settings live

A system log of its own, `<data-dir>/_system/wal/`, with the same format
and the same recovery as the WAL of a collection: one line per change,
CRC, fsync before the answer. `ndb backup` copies it with the collections,
and `ndb restore` brings it back first, so the key is there before any
collection opens.

### What this protects, and what it does not

Because the key is in the WAL and in the backups:

| Leak | The key in the settings (default) | A key only in the env |
|---|---|---|
| a backup file | **not protected**: the key is in it | protected |
| a copy of the data dir or the disk | **not protected** | protected |
| a caller of the API with a read token | sees what its token allows (M28) | the same |
| the memory of the daemon | not protected | not protected |

So the encryption of M28 protects the files only with an env key, and the
docs say it in these words. With the default, it limits who reads a value
through the API (the token right of M28), and keeps the value out of the
logs and of the console. To protect the backups with the default key, the
docs show `ndb backup | age -r ...` (encrypt the backup file).

## `PUT /_cluster/settings`

Only what can change at run time. In this milestone nothing of the key can
change (no rotation), so a `PUT` of it is `400`. `GET` also shows the log
level, so one call tells the state of the daemon; the log level keeps its
route `PUT /_log`.

## Epics

### E27.1 Daemon
- [ ] tests first: the order env, stored, made; the stored key survives a restart and a `kill -9` while it is written; an env key that differs from the stored one is a start error; a key that is not 64 hex is a start error; `GET /_cluster/settings` has the key masked only (a test greps the whole answer and every log line for the key); backup and restore bring the key; scope (admin)
- [ ] `internal/settings`: the system log (`_system/wal`), load, make, mask, derive (HKDF) the keys of M28
- [ ] `server`: `GET` and `PUT /_cluster/settings`
- [ ] backup and restore: the system log first

### E27.2 SDKs and docs
- [ ] Go, Ruby, TypeScript: `settings()` (read)
- [ ] `docs/dev/security.md` (the table above), `configuration.md`, `operations.md` (backup and restore with a key), `http/system.md`, `llms-agents.md`, CHANGELOG
- [ ] the console: the masked key and where it came from

## Exit criteria

- A daemon started with nothing has a key; a second start, and a restore of its backup on another machine, have the same one.
- No answer of the API and no log line has the whole key (tested).

## Out of scope

- Encrypted fields and blind indexes ([M28](m28-encrypted-fields.md)).
- Key rotation, more than one key, a KMS.
- Encryption of the whole WAL or of the segments.
