# Watch

`watch` streams the changes of a collection to a named consumer: first the events after its last ack, then new events as they happen. Use it for outbox work: send a webhook, fill a cache, copy data to another system.

`POST /v1/c/{coll}/watch` opens a long-lived NDJSON stream. `POST /v1/c/{coll}/ack` stores the position that the consumer has processed.

::: code-group

```bash [curl]
curl -N --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/calls/watch \
  -d '{"consumer":"relay","from":"beginning","where":{"status":"failed"}}'
# {"pos":"e1.1","op":"put","id":"c1","v":1,"doc":{"call_id":"c1","status":"failed"}}

curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/calls/ack \
  -d '{"consumer":"relay","pos":"e1.1"}'
```

```go [Go]
for ev, err := range calls.Watch(ctx, "relay", ndb.WatchWhere(ndb.Eq("status", "failed"))) {
	if errors.Is(err, ndb.ErrWatchLagged) {
		break // call Watch again. It continues after the last Ack.
	}

	if err != nil {
		return err
	}

	handle(ev.Op, ev.ID, ev.Doc) // ev.Op is ndb.OpPut or ndb.OpDel

	if err := calls.Ack(ctx, ev); err != nil {
		return err
	}
}
```

```ruby [Ruby]
begin
  ndb.watch("calls", "relay", where: { status: "failed" }) do |event|
    handle(event)   # "pos", "op", "id", "v", "doc"

    ndb.ack("calls", "relay", event["pos"])
  end
rescue Ndb::WatchLagged
  retry # watch continues after the last ack
end
```

```ruby [Rails]
client = Ndb::Rails.client

client.watch(Call.collection_name, "relay", where: { status: "failed" }) do |event|
  handle(event)

  client.ack(Call.collection_name, "relay", event["pos"])
end
```

```ts [TypeScript]
const controller = new AbortController()

for await (const ev of calls.watch('relay', { where: { status: 'failed' }, signal: controller.signal })) {
  await handle(ev) // { consumer, pos, op, id, v, doc }

  await calls.ack(ev)
}
```

:::

## Request

```json
{ "consumer": "relay", "from": "beginning", "limit": 0, "where": { "status": "failed" } }
```

| Key | Meaning |
|---|---|
| `consumer` | the name that owns the saved position. Each consumer has its own position. |
| `from` | `beginning` or `now`. Used only by a consumer with no saved position. A consumer that acked before resumes after its last ack. |
| `limit` | close the stream after that many events. `0` is no limit. |
| `where` | send only the changes whose document matches. Same [filters](query.md#filters) as `find`. |

In the SDKs: Go `ndb.FromNow()`, `ndb.Limit(n)`, `ndb.WatchWhere(...)`; Ruby `from: "now"`, `limit:`, `where:`; TypeScript `from: 'now'`, `limit`, `where`, `signal`. Without a block, the Ruby `watch` returns an Enumerator.

## Events

```json
{"pos":"e1.1","op":"put","id":"c1","v":1,"doc":{"_id":"c1","status":"answered"}}
{"pos":"e1.2","op":"del","id":"c2","v":2}
{"heartbeat":true}
{"error":{"code":"watch_lagged","message":"..."}}
```

- `op` is `put` (an insert or an update) or `del`. A delete by the TTL sweeper has `"_expired": true`.
- `doc` of a `put` is the full new document. An [update with operators](query.md#update-by-filter) sends the resulting document, never the operators.
- A delete has no document.
- A heartbeat line comes after 15 s without events.

## Filters and deletes

- A `put` is sent when its document matches `where`.
- A `del` has no document, so it is sent only if the last `put` of that id seen by this stream matched. The stream remembers the last 65 536 ids.
- Events that are left out still move the saved position.

## Ack and delivery

- `ack` stores the `pos` of an event that the consumer has processed. An older ack is ignored.
- Delivery is **at least once**. After a restart, an event can come again, so make the work safe to repeat.
- The next `watch` with the same consumer name starts after the last ack.

## Errors

- `watch_lagged` (an error line in the stream): the consumer was too slow. Reconnect. The stream resumes from the last ack.
- `history_truncated` (HTTP 410): the WAL files that the watch must read were deleted by `wal_retention`. This happens with no saved position and `from: "beginning"`, or with a position before the oldest kept file. The stream never skips the gap in silence. Start from `now`, or rebuild from `find`. A saved position is safe: ndb keeps the file that holds it.
- `invalid_position` (an error line): the saved position is not valid.
- The SDKs retry only the initial open of the stream, never in the middle. The Go stream ends when `ctx` is cancelled; the TypeScript stream ends with its `AbortSignal`.

A full consumer is in the [outbox example](/examples/outbox).
