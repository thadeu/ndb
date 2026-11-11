# Backlog: ndb as the primary database

Ideas from the review "can ndb hold users, payments and invoices?". Locks
were built (`with_retry` and the pessimistic lock, see
`docs/dev/query.md`). The items below were looked at and left for later;
each one has the reason, so the talk does not start from zero.

## 1. Transactions across collections

Planned: [M25](../milestones/m25-transactions.md).

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

No TLS (a proxy, the unix socket or WireGuard give it; Postgres in Docker
runs the same way). No encryption at rest (disk encryption gives it).
Encrypted backups and encrypted fields (with a blind index for `$eq`) were
looked at and are not needed now.

## 5. Locks: what is not done

- Waiters of a lock are not served in arrival order (no fair queue).
- A lock is one document: no lock of many documents in one call.
- A `many` write that meets a locked document stops with `locked`; it
  does not wait.
- No shared case in `spec/` (the cases there are reads).
