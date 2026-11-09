# M21: Streams in the SDKs

Goal: the streams of M20 in the Go, Ruby and TypeScript SDKs, with the calls
one by one and a worker loop that does the hard parts.

Out of scope: a Redis adapter (a type with the go-redis / redis-rb method
names over ndb). It comes when an app moves from Redis Streams to ndb.

## E21.1 Go (sdk/go) — done

- `StreamOf[T]`: `Append`, `AppendMany`, `Trim`, `Info`, `Groups`, `Range`,
  `RevRange`, `Delete`, `Create`; options `WithEntryID`, `WithTrim`,
  `NoCreate`.
- `Group`: `Create`, `Ensure`, `SetID`, `Destroy`, `CreateConsumer`,
  `DeleteConsumer`, `Consumers`, `Read`, `Ack`, `Pending`, `PendingList`,
  `Claim`, `AutoClaim`.
- `Group.Work`: own pending first, new entries with `Block`, one ack call
  per batch, claim of idle entries every `ClaimEvery`, poison messages to a
  dead-letter stream after `MaxDeliveries`.
- A blocked read uses the watch client: it does not count against
  `WithMaxConns`.
- Tests on the real daemon; README section.

## E21.2 Ruby (sdk/ruby)

The same surface with Ruby names (`stream.append`, `group.read`,
`group.work { |entry| ... }`), and the same tests.

## E21.3 TypeScript (sdk/ts)

The same surface (`stream.append`, `group.read`, `group.work(handler)`),
with an `AbortSignal` to stop the worker.

## E21.4 Docs

A Clients section in `docs/dev/streams.md` with the worker of each SDK; the
CHANGELOG entry.
