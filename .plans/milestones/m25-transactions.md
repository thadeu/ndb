# M25 — Transactions (one collection)

Status: in progress (E25.1 done) · Depends on: locks (done) · Docs: `docs/dev/concurrency.md`

## Scope (decided)

This milestone is the transaction of **one collection**. It is low risk:
many ops in one WAL record are atomic today, also after a crash, so
recovery and the file formats do not change, and a daemon that does not
use it is not touched.

The transaction of **many collections** is not in this milestone. It needs
a commit log and a redo at recovery, and a bug there can hurt every user.
Its design is kept at the end of this file ("Later"). It comes back when a
real case needs it and one document + `watch` does not solve it.

For reference: MongoDB had no multi-document transaction from 2009 to 4.0
(2018), and has it only on a replica set. Redis `MULTI`/`EXEC` has no
rollback (a command that fails does not undo the others). `_tx` on one
collection is all or nothing.

## Goal

A block of writes (create, update, delete) on one collection is one step:
all of them are written, or none. A failed block writes nothing
(rollback). It holds after a `kill -9` at any point.

```ruby
Account.transaction do
  accounts.each(&:lock!)

  raise Ndb::Rollback if from.balance < amount   # ends the block, writes nothing, no error outside

  from.update!(balance: from.balance - amount)
  to.update!(balance: to.balance + amount)
end
```

### Two ways to write it

Both end in the same commit. Pick by taste.

**1. Locks declared with the block** (two requests for any number of
documents, and no lock order to remember):

```ruby
Account.transaction(lock: [from, to], ttl: 5, wait: 2) do
  raise Ndb::Rollback if from.balance < amount

  from.update!(balance: from.balance - amount)
  to.update!(balance: to.balance + amount)
end
```

**2. Locks taken inside the block**, as in ActiveRecord (the example
above: one request per `lock!`, and the caller sorts the records to avoid
a deadlock).

`ttl` is the time limit of the transaction: the commit carries the lock
tokens, so a lock that expired before the commit refuses all of it
(`lock_expired`) and nothing is written. `wait` is how long to wait for a
document that another caller holds. Only the writes are undone: an effect
outside ndb made inside the block (a call to a gateway) is not.

The other clients have the same two ways (names are a proposal):

```go
err := accounts.Tx(ctx, []string{"a1", "a2"}, func(tx *ndb.Tx[Account], docs map[string]*Account) error {
	from, to := docs["a1"], docs["a2"]

	if from.Balance < amount {
		return ndb.ErrRollback
	}

	from.Balance -= amount
	to.Balance += amount

	tx.Update(from)
	tx.Update(to)

	return nil
}, ndb.LockTTL(5*time.Second))
```

```ts
await accounts.transaction(['a1', 'a2'], (tx, { a1, a2 }) => {
  if (a1.balance < amount) throw new Rollback()

  a1.balance -= amount
  a2.balance += amount

  tx.update(a1)
  tx.update(a2)
}, { ttl: 5 })
```

```ruby
accounts.transaction(lock: %w[a1 a2], ttl: 5) do |tx, docs|
  raise Ndb::Rollback if docs["a1"]["balance"] < amount

  docs["a1"]["balance"] -= amount
  docs["a2"]["balance"] += amount

  tx.update(docs["a1"])
  tx.update(docs["a2"])
end
```

With no ids (`accounts.Tx(ctx, nil, fn)`, `transaction { |tx| }`), the
block takes no lock: it is an atomic batch of creates, updates and
deletes, each update with its own `_v` guard if it wants one.

A block that writes a record of another collection is refused with a clear
error (`Ndb::TransactionError`), before anything is sent.

## Decisions

Taken as proposed when E25.1 started; change them before E25.2 if needed.

1. A read inside the block sees the stored data, not the writes of the
   block (the objects in memory have the new values).
2. An `update` of the transaction that matches nothing (its guard failed,
   or no such id) fails the whole transaction (`409 tx_conflict`). Outside
   a transaction it is `matched: 0`.
3. Rails: `before_*` callbacks run at `update!`; `after_save` runs after
   the commit, at the end of the block.

## Design

### Three new routes

| Route | Body | Answer |
|---|---|---|
| `POST /{coll}/_lock` | `{"ids": [...], "ttl", "wait"}` | `{"token", "expires_at", "docs": [...]}` |
| `POST /{coll}/_unlock` | `{"token"}` | `{"unlocked": n}` |
| `POST /{coll}/_tx` | `{"ops": [...], "locks": [...], "unlock"}` | `{"results": [...]}` |

`_lock` and `_unlock` with no id in the path are the many-document forms
of `_lock/{id}` and `_unlock/{id}`, which stay as they are.

- **`_lock`** takes the locks of all the ids or of none, in one step of
  the writer, and gives the documents as they are then (with `_v`). One
  token holds the whole set. When any id is held by another caller it
  takes nothing and waits (`wait`), then `423 locked`. So two callers can
  never hold half of each other's set: no deadlock. A missing id is
  `404 not_found` and nothing is locked. Limit: 100 ids.
- **`_unlock`** releases every lock of the token: the rollback of a block
  is one request.
- **`_tx`** is the commit, below. With the token of a set in `locks` and
  `"unlock": true`, the commit and the release are one request.

So a transaction with declared locks is two requests: `_lock`, then `_tx`
(or `_unlock` on a rollback).
### Client side: the block only collects

Inside the block, `create`, `update`, `destroy` (and `save`) check the
record (validations, callbacks before the write) and change the object in
memory. They send nothing. At the end of the block the SDK sends every
write in one request, `POST /{coll}/_tx`. So:

- any exception in the block: nothing was sent, the locks are released,
  the exception goes on to the caller;
- `Ndb::Rollback`: the same, but the block returns `nil` and no exception
  leaves it (as `ActiveRecord::Rollback`);
- a commit that the daemon refuses (a guard, a unique key, a lost lock)
  raises, and nothing was written.

A read inside the block sees the stored data, not the writes of the block
(the objects in memory have the new values).

### `POST /{coll}/_tx`

```json
{
  "ops": [
    {"op": "update", "id": "a1", "query": {"_v": 7}, "update": {"$set": {"balance": 50}}},
    {"op": "update", "id": "a2", "update": {"$inc": {"balance": 50}}},
    {"op": "insert", "doc": {"_id": "a3", "balance": 0}},
    {"op": "delete", "id": "a9"}
  ],
  "locks": ["<token of the set>", "<token of a lock! inside the block>"],
  "unlock": true
}
```

- Answer: `{"results": [{"_id", "_v"} | {"deleted": 1}, ...]}`, in the order of `ops`.
- An `update` that matches nothing (its guard failed, or no such id) fails
  the whole transaction: `409 tx_conflict`, with the index of the op.
  Other errors keep their codes (`unique_violation`, `duplicate_id`,
  `lock_expired`, `invalid_doc`), also with the index.
- `locks`: the tokens the transaction holds: the one of a set (`_lock`), and
  one per `lock!` inside the block. An op on a locked document needs the
  token that holds it here.
  `"unlock": true` releases them after the commit.
- A document locked by another caller: the transaction waits, as a plain
  write does.
- Limits: engine collections only (no direct, no stream, no pattern); the
  body under `max_line_size`; one partition per op, as `_bulk`.

### One WAL line

All ops become one WAL record (`ops` already holds many puts and dels).
That is atomic today, also after a crash. No new file, no change in
recovery.

## Epics

### E25.1 Daemon: the three routes
- [x] tests for `_lock` (many): all or none; the documents of that instant; one token for the set; a held id waits, then `locked`; two callers with crossed sets never deadlock; a missing id locks nothing; the ttl ends every lock of the set
- [x] tests for `_unlock` (many): every lock of the token; a stale token is `lock_expired`
- [x] tests for `_tx`: mixed insert + update + delete in one record; any failing op writes nothing; several tokens; `unlock`; an expired token refuses all; waits for a foreign lock; the index of the failing op; a `kill -9` loop (the record is whole or absent)
- [x] `locks`: a token that holds many ids; release by token
- [x] `collection`: the lock of a set and a request of mixed ops in `prepare`
- [x] `server`: the routes, scope (write), errors, the partitioned case (one partition per transaction, else `not_supported`), docs, error table

### E25.2 SDKs
- [ ] Rails: `Model.transaction { }` and `Model.transaction(lock:, ttl:, wait:) { }`, the writes collected, `Ndb::Rollback`, `lock!` inside the block joins the transaction, locks released at the end, another collection refused, the callback order of decision 3
- [ ] Go `coll.Tx(ctx, ids, fn)`, TypeScript `coll.transaction(ids, fn)`, Ruby `coll.transaction(lock:) { |tx, docs| }`: `tx.insert / update / delete`, the rollback error, `ttl` and `wait`
- [ ] a shared case file in `spec/` for the commit and the refusals

### E25.3 Docs
- [ ] `docs/dev/concurrency.md` (level 4: transaction, and its limit of one collection), `http/documents.md`, client pages, `llms-agents.md`, CHANGELOG

## Exit criteria

- The transfer example passes in Rails, and a raise at each line leaves no change.
- `make bench`: every check PASS; `live write p99` budget unchanged (a write outside a transaction is not slower).

## Out of scope

- Many collections (see "Later").
- Reads inside the transaction that see its own writes.
- Direct collections, streams, patterns.
- Savepoints and nested transactions (a nested block joins the outer one).

## Later: many collections

Not planned. The design, kept so the work does not start from zero. The
route would be `POST /_tx`, with `"coll"` in each op and in each lock.

### An intent log, then redo

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

Work it would need: tests that kill the daemon at each step (before the
intent, after it, between two applies, before `done`); `internal/txlog`;
the hold of the writers in name order; the redo in recovery;
`wal_retention` keeping a file with a pending `_gtx`; a `tx` part in
`make bench` (acked transactions lost or half written by a `kill -9`: 0).
