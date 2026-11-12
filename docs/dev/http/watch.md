# Watch routes

Stream the changes of a collection to a named consumer, and store how far it got. The events, the delivery rules and the errors in the stream are on [Watch](../watch.md); this page is the wire contract. The rules that apply to every route are on [HTTP API](../http.md).

## `POST /{coll}/_watch`

Opens a long-lived NDJSON stream: first the events after the last ack of the consumer, then new events as they happen.

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `consumer` | string | required | the name that owns the saved position |
| `from` | string | `beginning` | `beginning` or `now`; used only by a consumer with no saved position |
| `limit` | int | 0 | close the stream after that many events; 0 is no limit |
| `query` | object | none | send only the changes whose document matches, see [Filters](../query.md#filters) |

`?decrypt=1` in the URL gives the `doc` of each event with the plain values of the [encrypted fields](../encryption.md#who-reads-the-plain-value), for a caller with the right.

**Answer:** `200`, `Content-Type: application/x-ndjson`, one line per event:

```json
{"pos":"e1.1","op":"put","_id":"c1","_v":1,"doc":{"_id":"c1","status":"failed"}}
{"pos":"e1.2","op":"del","_id":"c2","_v":2}
{"pos":"e3.1","op":"truncate"}
{"heartbeat":true}
{"error":{"code":"watch_lagged","message":"..."}}
```

- `op` is `put`, `del` or `truncate`. A delete by the TTL sweeper has `"_expired": true`.
- A heartbeat line comes after 15 s without events. Ignore it.
- The stream ends on a truncate or a drop of the collection, on shutdown, after `limit` events, or with an error line. Reconnect: the consumer resumes after its last ack. A stream that a truncate ends can end before it sends the `truncate` line. The `truncate` event always comes on the next connect, so do not depend on it in the open stream.

**Errors before the stream:** `404 unknown_collection`, `410 history_truncated`, `400 invalid_query`, `400 pattern_read_only`. **In the stream:** `watch_lagged`, `invalid_position`.

::: code-group

```bash [curl]
curl -N --unix-socket /run/ndb/ndb.sock http://ndb/calls/_watch \
  -d '{"consumer":"relay","from":"beginning","query":{"status":"failed"}}'
```

```go [Go]
for ev, err := range calls.Watch(ctx, "relay", ndb.WatchQuery(ndb.Eq("status", "failed"))) {
	if err != nil {
		return err
	}

	handle(ev)

	if err := calls.Ack(ctx, ev); err != nil {
		return err
	}
}
```

```ruby [Ruby]
calls.watch("relay", query: { status: "failed" }) do |event|
  handle(event)
  calls.ack(event, "relay")
end
```

```ts [TypeScript]
for await (const ev of calls.watch('relay', { query: { status: 'failed' } })) {
  await handle(ev)
  await calls.ack(ev)
}
```

:::

## `POST /{coll}/_ack`

Stores the `pos` of an event that the consumer has processed. The next `_watch` with that consumer name starts after it. An older ack is ignored. Delivery is at least once: make the work safe to repeat.

| Body key | Type | Meaning |
|---|---|---|
| `consumer` | string | the consumer name |
| `pos` | string | the `pos` of the event |

**Answer:** `{"ok":true}`. **Errors:** `400 invalid_position` (a token that is not valid, or a position after the last change of the collection: an ack of `e99.1` on a collection at tx 5 would skip the changes up to tx 99), `404 unknown_collection`.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_ack -d '{"consumer":"relay","pos":"e1.1"}'
```
