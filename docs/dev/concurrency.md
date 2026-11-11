# Concurrency

**ndb is optimistic by default.** Every document has a version, `_v`. The daemon sets it, adds 1 on each write, and gives it back with every read. There is no column to add and nothing to turn on.

With the version, a write can say "only if nobody changed this document since I read it". Nobody waits, and no lock is left behind when a process dies. You get three levels, and you start at the first one without doing anything.

| Level | What it does | When |
|---|---|---|
| 1. The version (`_v`) | a write with a stale version changes nothing, and you get an error | always there |
| 2. `with_retry` | reads again and runs your change again after a conflict | your code only changes the document |
| 3. `lock` | one holder at a time; the others wait | your code has an effect outside ndb |

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

## Which one

- **A counter or a balance with one operator:** no version and no lock. `$inc` is atomic (`increment!` in Rails), and a condition in the query makes it a guarded write: `{"query":{"balance_cents":{"$gte":500}},"update":{"$inc":{"balance_cents":-500}}}`.
- **A change that depends on what you read:** the version. In Rails it is there on every save. Add `with_retry` when a conflict should try again and not fail.
- **Code with an effect outside ndb:** `lock`.
- **A change of two documents as one step:** ndb has no transaction across documents. Make the fact one document (an invoice with its items), or write one document and let [watch](watch.md) update the rest.
