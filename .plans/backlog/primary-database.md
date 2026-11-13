# Backlog: ndb as the primary database

Ideas from the review "can ndb hold users, payments and invoices?". Locks
were built (`with_retry` and the pessimistic lock, see
`docs/dev/query.md`). The items below were looked at and left for later;
each one has the reason, so the talk does not start from zero.

## 1. Transactions across collections

One collection is done (M25: `POST /{coll}/_tx`, `Model.transaction`). Many collections (a commit log and a redo at recovery) is high risk and waits for a real case; its design is at the end of `.plans/milestones/m25-transactions.md` in the git history (removed in 45755db).

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
(`docs/dev/encryption.md`). Still left out: TLS (a proxy, the unix socket or
WireGuard give it; Postgres in Docker runs the same way), encryption at rest
of the whole data dir (disk encryption gives it) and encrypted backup files
(`age` or `gpg` on the `.ndb` file).

## 5. Locks: what is not done

- Waiters of a lock are not served in arrival order (no fair queue).
- A `many` write that meets a locked document stops with `locked`; it
  does not wait.
- No shared case in `spec/` (the cases there are reads).
