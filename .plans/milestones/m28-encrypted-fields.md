# M28 — Encrypted fields and blind indexes

Status: not started · Depends on: [M27](m27-settings.md) (the key)

## Goal

A field whose value must not be read from the files of the daemon: a
`.pem`, a `.pfx`, a token, a CPF. The daemon encrypts it before the WAL
and decrypts it for a caller that is allowed. A field that must be found
by its value (a CPF) also gets a blind index, so the filter works and the
value is in no file.

```json
PUT /servers   {"encrypted": ["private_key"]}
PUT /users     {"encrypted": ["cpf"], "blind": ["cpf"], "unique": ["cpf"]}
```

```json
POST /users/_search   {"query": {"cpf": "12345678909"}}
```

## Why in the daemon

The key is in the daemon (M27), so every client gets the feature with no
code: Go, Ruby, Rails, TypeScript and curl send and read plain JSON. It is
the rule of this repo: the daemon owns meaning, the SDKs only build JSON.

What it costs: the daemon sees the value while it handles the request. A
caller of the API is limited by its token, not by the encryption (see
"Who reads the value").

A stricter mode, where the app holds the key and the daemon never sees
the value, is possible later with the same stored format. It needs the
crypto in every SDK.

## Design

- **Stored value:** `"ndb:e1:k1:<base64url(nonce + ciphertext + tag)>"`, AES-256-GCM
  with a random nonce, the key derived from the master key. It is what the
  WAL, the segments, the backups and `watch` over a token with no right
  hold (`e1` is the format, `k1` the key: a later rotation writes `k2`). With the default key (in the settings, so in the backups) this
  keeps the value out of the API, the logs and the console; to protect the
  files too, the key goes in `NDB_ENCRYPTION_KEY` (see M27).
- **Blind index:** for a field in `blind`, the daemon also stores
  `<field>_bidx`: `ndb:h1:k1:<hex>`, HMAC-SHA256 of the normalized value,
  with a second derived key. An index, a unique key or a compound index on the field
  uses `<field>_bidx`. A filter with `$eq` or `$in` on the field is
  rewritten to it.
- **Normalization** per field, declared with it: none (default), `digits`
  (a CPF with or without dots), `lower` (an email).
- **What a filter can do on an encrypted field:** `$eq`, `$in`, `$ne`,
  `$nin`, `$exists` with `blind`; nothing else (`400 invalid_query` names
  the field). No sort, no range, no regex.
- **Aggregate:** `$group` by the field groups by the hash and gives the
  value back decrypted; `$distinct` counts the hashes. `$sum`, `$min`,
  `$max`, `$avg` of an encrypted field are refused.
- **Who reads the value:** the unix socket and a token with the right
  `decrypt` get the value; any other token gets `"ndb:e1:k1:..."`. The
  console shows "encrypted" and never the value.
- **Cost, measured** (AES-256-GCM + base64, one core): 0.1 µs for a CPF,
  3 µs for a 2 KB `.pem`, 11 µs for an 8 KB `.pfx`; the HMAC of a CPF
  0.5 µs. The stored value is about 35% bigger.

## Decisions to confirm

1. In the daemon (this plan), not in the SDKs.
2. `encrypted` and `blind` are two lists of the collection definition,
   as `indexes` and `unique` are; `blind` needs `encrypted`.
3. The right `decrypt` of a token; the unix socket has it.
4. A field that becomes encrypted later: old documents stay as they are
   until they are written again; `ndb encrypt <coll>` rewrites them.

## Epics (short; detailed when M27 is done)

- E28.1 `encrypted`: write and read paths, `_tx`, `_bulk`, updates with
  operators (`$set` only on an encrypted field), `watch`, backup, lookup.
- E28.2 `blind`: the hash field, indexes and unique, the filter rewrite,
  aggregate.
- E28.3 Tokens (`decrypt`), console, SDK model options (Rails
  `attribute :cpf, encrypted: true, blind: :digits`), docs, a bench check
  (a write and a read of an encrypted field are not slower by more than
  the measure above).

## Out of scope

- Range, sort, prefix or math on an encrypted field.
- Key rotation (M27 leaves the version for it).
- The app-held key mode.

## The key on the hot path

No client reads the key: `/_cluster/settings` never gives it. The daemon encrypts and decrypts.
The two derived keys (`ndb:e1`, `ndb:h1`) and the AES-GCM cipher are made once, at open, and
kept in the `DB`. A read or a write does no HKDF, no settings read and no lock.
