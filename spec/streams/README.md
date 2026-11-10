# Stream cases

The contract cases of [streams](../../docs/dev/streams.md). They are not in
`spec/cases` because a stream case is a sequence of steps, not one request:
the runner of `spec/cases` would fail on them. M20 E20.2 adds their runner
(daemon and SDKs); until then they are the contract, written first.

## Format

```json
{
  "name": "what the case proves",
  "stream": "orders",
  "steps": [
    { "op": "append", "body": { "doc": { "n": 1 }, "id": "1-0" }, "expect": { "body": { "_id": "1-0" } } },
    { "op": "read", "group": "g", "body": { "consumer": "c1", "count": 10 }, "expect": { "rows": [{ "_id": "1-0", "n": 1 }] } }
  ]
}
```

Each step runs in order on a new daemon. `op` names a route:

| op | route |
|---|---|
| `create` | `PUT /{s}` with `body` |
| `append`, `trim` | `POST /{s}/_streams/append`, `/trim` |
| `tail` | `POST /{s}/_streams/read` (a read without a group) |
| `info` | `GET /{s}/_streams` |
| `group_create`, `group_destroy` | `PUT`, `DELETE /{s}/_streams/groups/{group}` |
| `setid` | `POST /{s}/_streams/groups/{group}/setid` |
| `consumer_create`, `consumer_delete` | `PUT`, `DELETE /{s}/_streams/groups/{group}/consumers/{consumer}` |
| `groups`, `consumers` | `GET /{s}/_streams/groups`, `/groups/{group}/consumers` |
| `read`, `ack`, `nack`, `pending`, `claim` | `POST /{s}/_streams/groups/{group}/<op>` |
| `search`, `count` | `POST /{s}/_search`, `/_count` |
| `delete` | `DELETE /{s}/_doc/{id}` |
| `put_doc` | `POST /{s}/_doc` (a normal collection, for `not_a_stream`) |

Keys of a case:

- `ndb_only: true`: the case is of a feature that Redis does not have (the
  delayed retry: `nack`, `due`, `retry_at`, `waiting`). It runs on ndb;
  `TestStreamCasesOnRedis` skips it. The other cases must pass on both.
- `note` (optional): text for the reader, for example what Redis does in
  the case. The runners do not read it.

The Redis runner adds the ndb-only fields of a `pending` answer with their
value when nothing is nacked (`"waiting": 0`, `"retry_at": null`), so every
case can expect them.

`expect` (optional; a step with no `expect` must not fail):

- `body`: the JSON answer, compared exactly after `ignore` keys are removed
  at any depth.
- `rows`: the rows of a read or a search, in order.
- `error`: the error code.
- `ignore`: keys whose value changes from run to run (`_ts`, `idle_ms`,
  `inactive_ms`).
