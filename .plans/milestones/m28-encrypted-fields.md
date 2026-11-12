# M28 — Encrypted fields and blind indexes

Status: done (but `ndb encrypt`, left out of this round) · Depends on: [M27](m27-settings.md) (done: the key, `settings.Key.Derive`)

## Goal

A field whose value must not be read from the files of the daemon: a `.pem`, a `.pfx`, a token,
a CPF. The daemon encrypts it before the WAL and decrypts it for a caller that is allowed. A
field that must be found by its value (a CPF) also gets a blind index, so a filter works and the
value is in no file.

```json
PUT /servers   {"encrypted": ["private_key"]}
PUT /users     {"encrypted": ["cpf"], "blind": {"cpf": "numeric"}, "unique": ["cpf"]}
POST /users/_search   {"query": {"cpf": "123.456.789-09"}}
```

## What M27 gives

- `db.Key()` (`settings.Key`): the master key, `ID` = `k1`, `Derive(purpose)` (HKDF-SHA256).
- The key is in the settings and the backups, or only in `NDB_ENCRYPTION_KEY`.
- The format names, fixed in M27: `ndb:e1:k1:` (value) and `ndb:h1:k1:` (blind hash).

## Design

- **Keys, made once.** At open the engine derives `Derive("ndb:e1")` and `Derive("ndb:h1")`
  and makes the AES-256-GCM `cipher.AEAD`. They live in a new `internal/crypt` type `Box`
  held by the `DB` and given to each collection. No HKDF, settings read or lock per call.
- **Stored value:** `"ndb:e1:k1:<base64url(nonce12 + ciphertext + tag)>"`, random nonce. Any JSON
  value (string, number, object) is encrypted as its JSON bytes, so the type comes back.
  The additional data of GCM is `<collection>/<field>`: a value copied to another field or
  collection does not decrypt.
- **Blind hash:** for a field in `blind`, the doc also has `<field>_bidx` =
  `"ndb:h1:k1:<hex(HMAC-SHA256(collection/field + 0x00 + normalized value))[:32]>"`. The
  collection and field are in the HMAC, so equal CPFs in two collections do not match.
- **Normalization** per field in `blind`: `exact` (default), `lower`, `upper`, `numeric`. `blind` is also a list of exact fields.
- **Where it runs:** one place, the write path of the collection, before the writer sees the
  doc (insert, `_bulk`, `_tx`, upsert, the result of an update). Decrypt on the way out of the
  server (`_search`, get, `watch`, `_aggregate`, lookup), for a caller with `decrypt`.
- **Direct mode:** the app writes the files, so the daemon cannot encrypt first. `encrypted` on a
  direct-mode collection is `400 invalid_request`.
- **Updates:** `$set`, `$unset` on an encrypted field; `$inc`, `$push` and the other operators
  are `400 invalid_query` that names the field. `_update_by_query` the same.
- **Filters:** with `blind`: `$eq`, `$in`, `$ne`, `$nin`, rewritten to `<field>_bidx`;
  `$exists` on the field itself. Any other operator, a sort or a range: `400 invalid_query`.
  Without `blind`: only `$exists`.
- **Indexes:** `indexes`, `unique` and compound indexes name the field; the catalog maps them to
  `<field>_bidx`. An index on an encrypted field without `blind` is refused.
- **Aggregate:** `$match` as filters; `$group` by a blind field groups by the hash, the `_id`
  is decrypted when the caller may; `$sum` `$avg` `$min` `$max` and `$sort` of it are refused.
- **Who reads the value:** the unix socket, the full token and a token with the new list
  `decrypt` (collection globs, as `read`) get the plain value. Any other caller gets the stored
  `"ndb:e1:k1:..."`. `<field>_bidx` is never in an answer.
- **A field that becomes encrypted later:** old docs stay plain until written again;
  `ndb encrypt <coll>` rewrites them (E28.3). A filter on a blind field reads both forms
  until then: not in this milestone, `ndb encrypt` is the answer.

## Cost (measured, one core)

AES-256-GCM + base64: 0.1 µs a CPF, 3 µs a 2 KB `.pem`, 11 µs an 8 KB `.pfx`; the HMAC 0.5 µs.
The stored value is about 35% bigger.

## Epics

### E28.1 `encrypted` (core)
- [x] tests first: a doc written over HTTP has no plain value in the WAL, a segment or a backup;
  a get and a `_search` give it back; a token with no `decrypt` gets `ndb:e1:k1:`; the value
  holds across restart, seal and compaction; a restore with the key reads it; a bad prefix or tag
  is an error, not a crash
- [x] `internal/crypt`: `Box{Seal, Open, Hash}`, made from `settings.Key`
- [x] catalog: `encrypted` (list), checks (no `_id`, time, expires field, indexed field, direct
  mode, stream; top-level fields only), the def merge as `unique`
- [x] write path: insert, `_bulk`, `_tx`, upsert, updates (`$set` / `$unset` only)
- [x] read path: get, `_search`, `_aggregate`, `watch`, `_update` and `_lock` docs; the `decrypt`
  list of tokens. Not yet: the docs that lookup attaches (nested), they stay sealed
- [x] no body or doc of an encrypted collection in a log line
- [x] a filter on an encrypted field without `blind`: `400 invalid_query` but `$exists`

### E28.2 `blind`
- [x] the `<field>_bidx` field, the filter rewrite (`query.Options.Field`, one hook for every
  parse), indexes, `unique`, compound
- [x] aggregate: `$match` rewritten; `$group` by `"$cpf"` (or a key of a compound `_id`) groups by
  the hash and gives the `_id` back from a hidden `$first`, decrypted for a caller with the right
  (`server/blindgroup.go`, so plain, partitioned and pattern reads get it); `$first` / `$last` of
  the field allowed; other stages that read it refused
- [x] `_count`, `_delete_by_query`, `_update_by_query` with the rewrite
- [x] cases in `spec/cases` (`"daemon": true`: the collection runner skips them)

### E28.3 Clients and tools
- [x] SDKs: collection option `encrypted` / `blind` (Go, Ruby, TS); Rails
  `attribute :cpf, encrypted: true, blind: :numeric`
- [x] the console shows "[encrypted]", never the value (it never sends `decrypt=1`)
- [ ] `ndb encrypt <coll>`: out of this round. To encrypt old documents, write them again
  (any write does it: `_update_by_query` with `{"query":{"_id":{"$exists":true}},"update":{"$set":{"encrypted_at":"..."}}}`)
- [x] docs: `collections.md`, `security.md`, `http.md`, `http/`, `clients/`, `llms-agents.md`, CHANGELOG
- [x] bench check: the extra time of a write and a read with an encrypted 2 KB `.pem` is at
  most 25% (measured about 10%; the first read was 30%, from a JSON decoder walk, now a
  byte scanner)

### E28.4 Decrypt on request (done)
- [x] answers are sealed by default; `?decrypt=1` and the right give the plain value (no 403 without
  the right: the stored value)
- [x] SDKs send `decrypt=1`; `encrypted` (Go `Encrypted()`, `KeepEncrypted()`) sends `decrypt=0` (a vault list, then a show)
- [x] the documents of a lookup follow the right on their own collection; no `_bidx` in them
- [x] `upper` normalization; `digits` renamed `numeric`; `X-Ndb-Decrypt` header removed

## Exit criteria

- `grep` for a CPF in the data dir and in a backup finds nothing, with the key in the env.
- A filter by CPF uses the index (explain) and gives the doc.
- Every SDK passes the spec case.

## Out of scope

- Range, sort, prefix or math on an encrypted field.
- Key rotation (`k2`): the prefix is ready for it.
- The app-held key mode (the daemon never sees the value).
