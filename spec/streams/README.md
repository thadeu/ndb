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
| `info` | `GET /{s}/_streams` |
| `group_create`, `group_destroy` | `PUT`, `DELETE /{s}/_streams/groups/{group}` |
| `setid` | `POST /{s}/_streams/groups/{group}/setid` |
| `consumer_create`, `consumer_delete` | `PUT`, `DELETE /{s}/_streams/groups/{group}/consumers/{consumer}` |
| `groups`, `consumers` | `GET /{s}/_streams/groups`, `/groups/{group}/consumers` |
| `read`, `ack`, `pending`, `claim` | `POST /{s}/_streams/groups/{group}/<op>` |
| `search`, `count` | `POST /{s}/_search`, `/_count` |
| `delete` | `DELETE /{s}/_doc/{id}` |
| `put_doc` | `POST /{s}/_doc` (a normal collection, for `not_a_stream`) |

`expect` (optional; a step with no `expect` must not fail):

- `body`: the JSON answer, compared exactly after `ignore` keys are removed
  at any depth.
- `rows`: the rows of a read or a search, in order.
- `error`: the error code.
- `ignore`: keys whose value changes from run to run (`_ts`, `idle_ms`,
  `inactive_ms`).
