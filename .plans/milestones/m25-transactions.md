# M25 — Transactions

Status: not started · Depends on: locks (done) · Docs: `docs/dev/concurrency.md`

## Goal

A block of writes (create, update, delete) on one or many collections is
one step: all of them are written, or none. A failed block writes nothing
(rollback). It holds after a `kill -9` at any point.

```ruby
Account.transaction do
  accounts.each(&:lock!)

  raise Ndb::Rollback if from.balance < amount   # ends the block, writes nothing, no error outside

  from.update!(balance: from.balance - amount)
  to.update!(balance: to.balance + amount)
  Transfer.create!(from: from.id, to: to.id, amount: amount)   # another collection
end
```

## Design

### Client side: the block only collects

Inside the block, `create`, `update`, `destroy` (and `save`) check the
record (validations, callbacks before the write) and change the object in
memory. They send nothing. At the end of the block the SDK sends every
write in one request, `POST /_tx`. So:

- any exception in the block: nothing was sent, the locks are released,
  the exception goes on to the caller;
- `Ndb::Rollback`: the same, but the block returns `nil` and no exception
  leaves it (as `ActiveRecord::Rollback`);
- a commit that the daemon refuses (a guard, a unique key, a lost lock)
  raises, and nothing was written.

A read inside the block sees the stored data, not the writes of the block
(the objects in memory have the new values).

### `POST /_tx`

```json
{
  "ops": [
    {"coll": "accounts", "op": "update", "id": "a1", "query": {"_v": 7}, "update": {"$set": {"balance": 50}}},
    {"coll": "accounts", "op": "update", "id": "a2", "update": {"$inc": {"balance": 50}}},
    {"coll": "transfers", "op": "insert", "doc": {"_id": "t1", "from": "a1", "to": "a2", "amount": 50}},
    {"coll": "sessions", "op": "delete", "id": "s9"}
  ],
  "locks": [{"coll": "accounts", "id": "a1", "token": "…"}, {"coll": "accounts", "id": "a2", "token": "…"}],
  "unlock": true
}
```

- Answer: `{"results": [{"_id", "_v"} | {"deleted": 1}, ...]}`, in the order of `ops`.
- An `update` that matches nothing (its guard failed, or no such id) fails
  the whole transaction: `409 tx_conflict`, with the index of the op.
  Other errors keep their codes (`unique_violation`, `duplicate_id`,
  `lock_expired`, `invalid_doc`), also with the index.
- `locks`: the N tokens. Each op on a locked document needs its token here.
  `"unlock": true` releases them after the commit.
- A document locked by another caller: the transaction waits, as a plain
  write does.
- Limits: engine collections only (no direct, no stream, no pattern); the
  body under `max_line_size`; one partition per op, as `_bulk`.

### One collection: one WAL line

All ops on one collection become one WAL record (`ops` already holds many
puts and dels). That is atomic today, also after a crash. No new file.

### Many collections: an intent log, then redo

Each collection has its own WAL and writer, so two WAL lines are two
fsyncs. The commit point is a new file, `<data-dir>/tx.log`:

1. **Prepare.** In the order of the collection names, each writer prepares
   its ops (versions, unique keys, guards, locks) and holds: no other write
   of these collections goes on. A failure here releases every writer and
   nothing is written.
2. **Commit.** One line in `tx.log` with the id of the transaction and the
   prepared ops of every collection (final documents and versions), then
   `fsync`. This is the commit point.
3. **Apply.** Each writer appends its record to its WAL (tagged
   `"_gtx": "<id>"`), syncs, and publishes it (reads, watch). The writers
   are released. Then a `done` line goes to `tx.log`.

Recovery, before the collections open for writes: for each transaction of
`tx.log` with no `done` line, each of its collections that does not have
the `_gtx` record gets it now (redo). A prepared op is a final document
with its version, so the redo cannot fail and is idempotent.

What this gives:

- A crash before step 2: nothing in any WAL. A crash after it: every
  collection gets the write, at once or at the next start.
- A WAL never holds a record that did not commit. So `watch`, backup, PITR
  and anything that reads the WAL files need no change.
- A WAL record of a transaction is a normal record plus one key (`_gtx`).

What it does not give:

- **No snapshot across collections.** Between the commit and the end of
  step 3 (microseconds to one fsync), a reader can see the write in
  `accounts` and not yet in `transfers`. Each collection alone is always
  consistent. This is the rule ndb already has for reads of many
  collections.
- A transaction of many collections costs one more fsync (`tx.log`) and
  stops the writers of its collections for its duration.

`tx.log` is cut when it has only `done` transactions (at start, and when it
passes 16 MiB). A collection does not delete a WAL file (`wal_retention`)
that holds a `_gtx` with no `done` line.

## Epics

### E25.1 Daemon: `_tx` on one collection
- [ ] tests: mixed insert + update + delete in one record; any failing op writes nothing; N lock tokens; `unlock`; waits for a foreign lock; error index
- [ ] `collection`: a request of mixed ops in `prepare`
- [ ] `server`: `POST /_tx`, scope (write on every collection of the body), errors, docs, error table

### E25.2 Daemon: many collections
- [ ] tests first: kill at each step (before the intent, after it, between two applies, before `done`) and restart; then every collection has the write or none has
- [ ] `internal/txlog`: append, fsync, read, cut
- [ ] hold and release of the writers in name order; `_gtx` in the WAL record
- [ ] recovery: redo before the collections accept writes
- [ ] `wal_retention` keeps a file with a pending `_gtx`
- [ ] `make bench`: a `tx` part (p99 of a 2-collection transaction; acked transactions lost or half written by a `kill -9`: 0), with budgets

### E25.3 SDKs
- [ ] Rails: `Model.transaction { }`, the writes collected, `Ndb::Rollback`, locks released at the end, `after_commit`-style order of callbacks
- [ ] Go `db.Tx(ctx, fn)`, TypeScript `db.transaction(fn)`, Ruby `client.transaction { |tx| }`: `tx.insert / update / delete` per collection
- [ ] a shared case file in `spec/` for the commit and the refusals

### E25.4 Docs
- [ ] `docs/dev/concurrency.md` (level 4: transaction), `http/` page, client pages, `llms-agents.md`, CHANGELOG, the backlog item closed

## Exit criteria

- The transfer example above passes in Rails, with a raise at each line leaving no change.
- The kill tests pass 200 rounds (`-short` runs 5).
- `make bench`: every check PASS, with the new `tx` checks.
- A write outside a transaction is not slower (`live write p99` budget unchanged).

## Out of scope

- Reads inside the transaction that see its own writes.
- A snapshot across collections (see above).
- Direct collections, streams, patterns.
- Savepoints and nested transactions (a nested block joins the outer one).
