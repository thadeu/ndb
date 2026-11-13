# Backlog: ndb as the primary database

Ideas from the review "can ndb hold users, payments and invoices?". Locks
were built (`with_retry` and the pessimistic lock, see
`docs/dev/query.md`). The items below were looked at and left for later;
each one has the reason, so the talk does not start from zero.

## 1. Transactions across collections

One collection is done (M25: `POST /{coll}/_tx`, `Model.transaction`). Many collections (a commit log and a redo at recovery) is high risk and waits for a real case; its design is at the end of `.plans/milestones/m25-transactions.md` in the git history (removed in 45755db).

Looked at again on 2026-11-12: 1billion has no case that needs it. Most cases have another answer first: write the children first and the parent last (an orphan child is harmless), an outbox in one collection with a stream or `watch` for the effect on the others, or keep what changes together in one document.

## 2. Decimal type

Numbers compare as 64-bit floats; integers are exact. A declared decimal
field (`decimal: {amount: 2}`) was refused: it is a schema, and ndb is
schemaless. Money stays in integer cents (`$inc` on integers is exact).

## 3. Continuous archive to S3 or R2

Ship each closed WAL file and a periodic snapshot to a bucket, for a
recovery point of seconds on one VM. Left for later: `ndb backup - | aws
s3 cp -` in a cron does the job with no code of ours. If it comes back, it
must speak the S3 API with a custom endpoint (R2, MinIO, B2).

## 4. Encryption

Encrypted fields, blind indexes and key rotation are done
(`docs/dev/encryption.md`). Still left out: encryption at rest of the whole
data dir (disk encryption gives it) and encrypted backup files (`age` or
`gpg` on the `.ndb` file).

## 5. TLS on the TCP listener

Today: plain HTTP with the bearer token; a proxy, WireGuard or the unix
socket give TLS. The risk: a `0.0.0.0` listener with no proxy sends the
token and the `decrypt=1` values in the clear. Left for a later round on
2026-11-12 (the work is mostly in the SDKs). The design agreed then:

- Daemon: `listen.tls_cert` and `listen.tls_key` in `ndb.yml` (and
  `-tls-cert`/`-tls-key`); HTTPS on the same TCP port; TLS 1.2+; the
  certificate is read again when the file changes or on `SIGHUP` (Let's
  Encrypt renews). The unix socket stays plain.
- `ndb tls init --host db.internal`: a CA and a certificate for a private
  network.
- SDKs: `https://` URLs already work; add a CA file for a private CA, one
  name everywhere (`NDB_CA_FILE`, Go `ndb.WithCA`, Ruby `ca_file:`, TS
  `caFile`), with a shared case in `spec/`.
- Safety rule: a TCP listener off loopback with no TLS and no token does
  not start (`-insecure` to force, for TLS on a proxy); with a token and no
  TLS, a `warn`. This breaks a deploy that listens on `0.0.0.0` with no
  token: say it in the release notes.
- Not now: `autocert` (needs ports 80/443 and a public name), mTLS.

## 6. Locks: what is not done

- Waiters of a lock are not served in arrival order (no fair queue).
- A `many` write that meets a locked document stops with `locked`; it
  does not wait.
- No shared case in `spec/` (the cases there are reads).
