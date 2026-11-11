# Concurrency

**ndb is optimistic by default.** Every document has a version, `_v`. The daemon sets it, adds 1 on each write, and gives it back with every read. There is no column to add and nothing to turn on.

With the version, a write can say "only if nobody changed this document since I read it". Nobody waits, and no lock is left behind when a process dies. You get four levels, and you start at the first one without doing anything.

| Level | What it does | When |
|---|---|---|
| 1. The version (`_v`) | a write with a stale version changes nothing, and you get an error | always there |
| 2. `with_retry` | reads again and runs your change again after a conflict | your code only changes the document |
| 3. `lock` | one holder at a time; the others wait | your code has an effect outside ndb |
| 4. `transaction` | many writes of one collection, all or none | a change of many documents (a transfer) |

## 1. The version: nothing to build

In Rails, every model is optimistic. `save`, `update` and `destroy` send the version that the record was read with. It is the `lock_version` of ActiveRecord, without the column:

```ruby
payment = Payment.find("p1")       # reads _v
payment.update!(status: "paid")    # writes only if _v is still the same

# Another process wrote first: Ndb::StaleObjectError. Nothing was written.
```

In Go, TypeScript and plain Ruby, a document is a struct or a hash, not a record with state. The version is in every row, and you pass it as a guard:

::: code-group

```go [Go]
upd, err := payments.Update(ctx, "p1", ndb.Ops{"$set": {"status": "paid"}}, ndb.Guard(ndb.Eq("_v", p.Version)))
// upd.Matched == 0: another process wrote first
```

```ts [TypeScript]
const res = await payments.update('p1', { $set: { status: 'paid' } }, { guard: { _v: p._v } })
// res.matched === 0: another process wrote first
```

```ruby [Ruby]
res = payments.update("p1", { "$set" => { status: "paid" } }, guard: { _v: p["_v"] })
# res["matched"] == 0: another process wrote first
```

```bash [curl]
curl ... /payments/_update/p1 -d '{"query":{"_v":7},"update":{"$set":{"status":"paid"}}}'
# {"matched":0,"modified":0}: another process wrote first
```

:::

An update without a guard is still one atomic step: two `$inc` at the same time both count. The guard is for a change that depends on what you read.

## 2. with_retry: the retry done for you

After a conflict, the usual answer is: read again, decide again, write again. `with_retry` does that loop (5 tries by default). Use it only if you want the retry; the version protects you without it.

::: code-group

```ruby [Rails]
Wallet.find("w1").with_retry do |w|
  raise InsufficientFunds if w.balance_cents < 500

  w.balance_cents -= 500
end
```

```go [Go]
w, err := wallets.WithRetry(ctx, "w1", func(w *Wallet) error {
	if w.Balance < 500 {
		return ErrInsufficientFunds
	}

	w.Balance -= 500

	return nil
})
```

```ts [TypeScript]
await wallets.withRetry('w1', (w) => {
  if (w.balance_cents < 500) throw new InsufficientFunds()

  w.balance_cents -= 500
})
```

```ruby [Ruby]
wallets.with_retry("w1") do |w|
  raise InsufficientFunds if w["balance_cents"] < 500

  w["balance_cents"] -= 500
end
```

:::

Your code can run more than one time. More examples: [with_retry](query.md#with-retry-read-change-write-retry).

## 3. lock: when the code must run once

A retry is wrong when the code does something outside ndb: a refund, an email. Then take a lock. One caller holds the document, the others wait, and the code runs one time.

::: code-group

```ruby [Rails]
Payment.lock.find("p1") do |p|
  next if p.status == "refunded"

  Gateway.refund(p.charge_id)
  p.update!(status: "refunded")
end
```

```go [Go]
_, err := payments.Lock(ctx, "p1", func(p *Payment) error {
	if p.Status == "refunded" {
		return nil
	}

	if err := gateway.Refund(p.ChargeID); err != nil {
		return err
	}

	p.Status = "refunded"

	return nil
})
```

```ts [TypeScript]
await payments.lock('p1', async (p) => {
  if (p.status === 'refunded') return

  await gateway.refund(p.charge_id)
  p.status = 'refunded'
})
```

```ruby [Ruby]
payments.lock("p1") do |p|
  next if p["status"] == "refunded"

  Gateway.refund(p["charge_id"])
  p["status"] = "refunded"
end
```

:::

A lock ends by itself after its `ttl` (30 s by default), so a process that dies holds nothing. The rules are on [Lock](query.md#lock-one-holder-at-a-time).

## 4. transaction: many writes, all or none

A transaction writes many documents of one collection in one step: all of them, or none, also after a crash. Inside the block, the writes are collected; they go to the daemon in one call at the end. An error in the block writes nothing. `Rollback` stops the block with no error.

There are two ways to write it. **Locks declared with the block** take every lock in one call, all or none, so two callers never deadlock:

::: code-group

```ruby [Rails]
Account.transaction(lock: [from, to], ttl: 5) do
  raise Ndb::Rollback if from.balance < amount

  from.update!(balance: from.balance - amount)
  to.update!(balance: to.balance + amount)
end
```

```go [Go]
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

```ts [TypeScript]
await accounts.transaction(['a1', 'a2'], (tx, docs) => {
  const from = docs.a1!
  const to = docs.a2!

  if (from.balance < amount) throw new Rollback()

  from.balance -= amount
  to.balance += amount

  tx.update(from)
  tx.update(to)
}, { ttl: 5 })
```

```ruby [Ruby]
accounts.transaction(lock: %w[a1 a2], ttl: 5) do |tx, docs|
  raise Ndb::Rollback if docs["a1"]["balance"] < amount

  docs["a1"]["balance"] -= amount
  docs["a2"]["balance"] += amount

  tx.update(docs["a1"])
  tx.update(docs["a2"])
end
```

:::

**Locks taken inside the block**, as in ActiveRecord (Rails only). One call per `lock!`; sort the records, so two callers take them in the same order:

```ruby
Account.transaction do
  [from, to].sort_by(&:id).each(&:lock!)

  raise Ndb::Rollback if from.balance < amount

  from.update!(balance: from.balance - amount)
  to.update!(balance: to.balance + amount)
end
```

The rules:

- **`ttl` is the time limit.** The commit carries the lock tokens. When a lock expired before the commit, the daemon refuses all of it (`lock_expired`), and nothing is written.
- **A refused commit writes nothing.** A document that changed since it was read (its `_v`), or that is gone, fails the whole transaction with `tx_conflict`.
- **Reads inside the block see the stored data**, not the writes of the block. The records (or documents) in memory have the new values.
- **One collection.** ndb has no transaction across collections; in Rails, a write of another model in the block raises `Ndb::TransactionError` before anything is sent.
- **Only the writes are undone.** A call to a gateway made inside the block is done. The records keep the values set in the block, as in ActiveRecord.
- **Callbacks** (Rails) run at each call in the block, as in ActiveRecord; the write happens at the commit.
- **Without locks** (`Account.transaction { }`, `Tx(ctx, nil, fn)`, `transaction([], fn)`), a transaction is an atomic batch of creates, updates and deletes. An update of a document read before is guarded by its `_v`.

The wire contract is [`POST /{coll}/_tx`](http/documents.md#post-coll-tx) and [`POST /{coll}/_lock`](http/documents.md#post-coll-lock).

## Which one

- **A counter or a balance with one operator:** no version and no lock. `$inc` is atomic (`increment!` in Rails), and a condition in the query makes it a guarded write: `{"query":{"balance_cents":{"$gte":500}},"update":{"$inc":{"balance_cents":-500}}}`.
- **A change that depends on what you read:** the version. In Rails it is there on every save. Add `with_retry` when a conflict should try again and not fail.
- **Code with an effect outside ndb:** `lock`.
- **A change of many documents of one collection as one step** (a transfer between two accounts): a [transaction](#_4-transaction-many-writes-all-or-none).
- **A change of two collections as one step:** ndb has no transaction across collections. Make the fact one document (an invoice with its items), or write one document and let [watch](watch.md) update the rest.
