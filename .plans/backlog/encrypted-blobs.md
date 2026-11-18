# P2 — Encrypted blobs

Found in 1billion (2026-11-17), with ndb-rails 1.24.0. Security: data at rest in clear text. `encrypted: true` and `blind:` cover string fields, but a blob
(`{"$blob":"sha256:..."}`, `Ndb::Blob` in ndb-rails) is stored in clear text in `blobs/`.

1billion now keeps the raw mail of each request for 30 days (`MailCopy`, collection
`mail_copies`). A raw mail holds login links, 2FA codes, invoices and personal data
(LGPD). With `NDB_ENCRYPTION_KEY` set, a person expects these bytes to be sealed too.

## What is needed

- An option on a blob field (`attribute :raw, :blob, encrypted: true`, and the same in the
  Go SDK) that makes the daemon encrypt the bytes with the key of the data dir before it
  writes them, and decrypt them on read.
- Blobs are content-addressed (sha256 of the bytes), so 2 equal bodies share one file. With
  encryption, the address must come from the clear bytes (or from a keyed hash) so the
  dedup keeps working and the address does not leak the content.
- The blob GC and PITR keep working on encrypted blobs.

## Until then

1billion shows the body only to the owner and the admins of a workspace, takes the
attachments out of the copy, and the TTL deletes it after 30 days. When this ships,
`MailCopy.raw` gets `encrypted: true` and the note in its comment goes away.
