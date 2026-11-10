# M24 — Streams: delayed retry

Status: not started · Depends on: M20 (streams), M21 (stream SDKs), M23 Rails streams (for E24.2d only) · Asked by: the fsw-esl
post-call outbox (move it from RabbitMQ to an ndb stream)

## Goal

A worker that fails an entry can say **when** the entry comes back, and the wait can grow with
the number of deliveries (15 s, 1 min, 10 min, 1 h, 6 h). Today the only way back is `claim`
after `min_idle_ms`, which is one fixed wait for every attempt:

- a short `ClaimIdle` (1 min) hits a broken entry 1 440 times a day;
- a long `ClaimIdle` (6 h) makes the first retry, which usually works after a few seconds,
  wait 6 h.

RabbitMQ does this with one TTL queue per wait plus a dead-letter back to the main queue
(fsw-esl `internal/jobworker/outbox_retry.go`). ndb does it in the PEL, where the entry already
is: no second queue, no mover process, and the entry is never in two places.

What it does not try: a scheduled **append** (an entry that is first delivered in the future).
Out of scope, see below.

## Decisions (2026-11-10, to confirm)

- **The wait lives in the PEL entry.** `pending` gets one field, `RetryAt` (unix ms, 0 = none),
  on disk in the same bbolt transaction as the rest of the PEL.
- **ndb only, not Redis.** Redis has no delayed retry. `XCLAIM ... TIME <future>` is not one:
  Redis clamps a future time to now (to verify in E24.0). The new route and fields are listed
  under "ndb only" in `docs/dev/streams.md`; an app that never uses them sees Redis behavior.
- **One route: `nack`.** Clear name, one action per route (M20 rule), access = write.
- **The SDK picks the wait, the daemon stores it.** The daemon knows nothing of tiers; the
  worker loop of each SDK (Go `Work`, Ruby `worker`, TS `work`) computes `delay(deliveries)`
  and sends it.
- **Go, Ruby and TypeScript ship together**, with the same options in each one's units (M21
  rule: one SDK vocabulary). Rails follows when M23 Rails streams is done.
- **A retry entry has no owner while it waits.** So the consumer's own pending read after a
  restart does not take it early, and any worker of the group can take it when it is due.

## Epics

### E24.0 Parity check of `idle_ms` (before any new code)

Today `claim` by ids accepts a negative `idle_ms` and sets the delivery time in the future
(`p.Delivered = now - *o.IdleMs`, `internal/streams/service.go`). That already delays the entry,
by accident, and Redis likely does not do it.

- [ ] Spec case `spec/streams/claim_idle_negative.json`: `XCLAIM ... IDLE -60000` and
      `TIME <now + 60 s>`; run it on Redis 7.4 (`TestStreamCasesOnRedis`) and record the answer.
- [ ] Make ndb give the same answer (clamp to now, or `400` if Redis errors). The delayed
      retry must come from `nack`, never from this side door.

### E24.1 Daemon: `nack` and `RetryAt`

Tests first: `internal/streams` unit tests and new `spec/streams/nack_*.json` cases.

- [ ] `POST /{s}/_streams/groups/{g}/nack`

      { "consumer": "w1", "ids": ["1731163600000-5"], "delay_ms": 60000 }
      → { "nacked": 1 }

  - Only ids pending **for this consumer** count (as `ack` counts only pending ids); others
    count 0, not an error.
  - The entry stays in the PEL: owner cleared (`""`), `RetryAt = now + delay_ms`, delivery count
    unchanged (the next delivery adds 1).
  - `delay_ms` 0 = due at once (a plain "give it back"). Max: 30 days (`400` above).
  - Many ids in one call, one fsync.
- [ ] **`claim` honors `RetryAt`.** An entry with `RetryAt > now` is never claimed, by ids or by
      scan, whatever `min_idle_ms` or `force`. An entry with `RetryAt <= now` is claimable at once
      (it ignores `min_idle_ms`: the wait was already chosen). A claim clears `RetryAt`.
- [ ] **`read` with `pending: true`** does not give entries without an owner (they are no one's).
- [ ] **Due entries reach a worker fast.** Choose one (open question 1):
  - (a) the SDK claims due entries on a short tick (1 s), with a scan that only looks at
    entries with `RetryAt` set (`"due": true` on `claim`, so the scan does not touch the
    in-flight entries of live workers); or
  - (b) `read` of new entries gives due retries first, then new entries; the blocking read
    wakes when the earliest `RetryAt` passes (one timer per group).
- [ ] `pending` list answers `retry_at` (null when none) and accepts `"due": true|false`.
      Summary: `waiting` count (entries with `RetryAt > now`).
- [ ] `ack` of a waiting entry works (removes it), so an app can cancel a retry.
- [ ] `DELETE consumer` does not drop waiting entries (they have no owner).
- [ ] Trim: a waiting entry whose entry is trimmed is dropped at its next claim, in `deleted`,
      as today.
- [ ] Metric: `ndb_stream_group_waiting{stream,group}`.
- [ ] Backup keeps `RetryAt`. A restore after the due time makes the entry due at once.
- [ ] Bench: `ndb bench -streams` gets a nack phase (10 workers, 50 % nack, 1 s delay); check:
      no entry lost, none delivered before its `RetryAt`, `kill -9` loses no nack.

### E24.2 SDKs (Go, Ruby, TypeScript, Rails)

The three clients ship together, in one release with the daemon. Each gets the same calls and the
same worker options, in its own units: Go `time.Duration`, Ruby seconds (as `claim_idle:` today),
TypeScript milliseconds (as `claimIdleMs` today). Tests first in each SDK; the same three cases
in all three:

- fail twice, see the entry come back after each wait and not before;
- `MaxAge` passed: the entry goes to the dead-letter stream and is acked, no retry;
- a worker restart while an entry waits: the restarted worker does not take it early, and any
  worker takes it when it is due.

#### E24.2a Go (`sdk/go`)

- [ ] `g.Nack(ctx, consumer string, delay time.Duration, ids ...string) (int, error)`.
- [ ] `PendingEntry.RetryAt *time.Time`; `PendingQuery.Due *bool`; `PendingSummary.Waiting`.
- [ ] `ndb.Work`:
  - `Retry func(deliveries int64) time.Duration`: on a handler error, nack with
    `Retry(deliveries)` instead of leaving the entry pending. Nil keeps today's behavior.
  - `MaxAge time.Duration`: an entry older than this (`Entry.Time`) goes to `DeadLetter`.
  - the due claim of E24.1 in the loop (`workDue`, beside `workIdle`).
- [ ] `ndb.Tiers(waits ...time.Duration)` (the wait of try n is `waits[min(n, len)-1]`) and
      `ndb.TierSteps(ndb.Step{Wait, UpTo}...)` for bounds like the fsw-esl ones (1, 5, 11, 23).
- [ ] `ndb.DeadEntry` gets `Reason` (`"max_deliveries"` or `"max_age"`).
- [ ] Tests: `sdk/go/stream_test.go`, worker cases with a fake clock where the loop allows it.
- [ ] README of the SDK: the Work table gets the new fields.

#### E24.2b Ruby (`sdk/ruby`, gem `ndb`)

- [ ] `group.nack(consumer, ids, delay:)` (seconds, Float allowed), returns the count.
- [ ] `pending_list(..., due: true|false)`; each row has `retry_at` (Time or nil); summary
      `waiting`.
- [ ] `group.worker(consumer, retry: ..., max_age: ...)` and `group.work(...)`:
  - `retry:` a Proc `->(deliveries) { seconds }` or an Array of waits (`[15, 60, 600, 3600,
    21_600]`, the last one repeats);
  - `max_age:` seconds;
  - the due claim in the loop of `Ndb::Stream::Worker` (beside `work_idle`).
- [ ] `Ndb::Stream.tiers(15, 60, 600, ...)` and the step form with `up_to:`.
- [ ] The dead entry Hash gets `"reason"`.
- [ ] Errors: none new from the daemon expected; if E24.1 adds a code, add its class in
      `errors.rb` (the error code test of M22 fails otherwise).
- [ ] Tests: `sdk/ruby/test/stream_test.rb`.
- [ ] README of the gem and `docs/dev/clients/ruby.md`.

#### E24.2c TypeScript (`sdk/ts`, package `@thadeu/ndb`)

- [ ] `group.nack(consumer: string, ids: string[], delayMs: number): Promise<number>`.
- [ ] `PendingEntry.retryAt: Date | null`; `pendingList({ due })`; summary `waiting`.
- [ ] `WorkOptions` (`src/stream-worker.ts`):
  - `retry?: (deliveries: number) => number` (ms) or `number[]` of waits;
  - `maxAgeMs?: number`;
  - the due claim in the loop.
- [ ] `tiers(15_000, 60_000, ...)` exported from `src/index.ts`, with the step form.
- [ ] `DeadEntry.reason: 'max_deliveries' | 'max_age'`.
- [ ] `ErrorCode` union and error classes for any new code.
- [ ] Tests: `sdk/ts/test/stream.test.ts` (Node 24 and Bun).
- [ ] README of the package and `docs/dev/clients/typescript.md`.

#### E24.2d Rails (`ndb-rails`, after M23 Rails streams)

- [ ] `stream_from :name, group: "...", retry: [15, 60, 600, 3600, 21_600], max_age: 15.days`,
      passed to the Ruby worker of E24.2b.
- [ ] `bundle exec ndb streams` shows the retry schedule of each consumer at boot.
- [ ] Test in the gem's suite; `docs/dev/clients/rails.md`.

### E24.3 Docs

- [ ] `docs/dev/streams.md`: "Delayed retry" section, the worker loop with `nack`, an "ndb only"
      list (nack, `retry_at`, `due`, `waiting`).
- [ ] `docs/dev/http/streams.md`: the `nack` route, the claim and pending changes, error codes.
- [ ] Client pages, each with the same example (the five fsw-esl tiers + 15 days):
      `docs/dev/clients/go.md`, `ruby.md`, `typescript.md`, `rails.md`. The streams page shows
      the worker in the three languages, as it does today.
- [ ] CHANGELOG.

## Exit criteria

- `spec/streams` passes on ndb; the cases that Redis also has still pass on Redis 7.4.
- No entry is delivered before its `RetryAt` (bench check), and a `kill -9` loses no nack.
- In Go, Ruby and TypeScript, a worker with the five tiers (15 s, 1 min, 10 min, 1 h, 6 h) and a
  max age reproduces the fsw-esl RabbitMQ retry schedule in a test (fake clock where the SDK
  allows it, short waits otherwise).
- The three SDK suites pass the three shared cases of E24.2; the Rails test passes.
- `make bench` checks pass.

## Out of scope

- Scheduled append (`deliver_at` on append): IDs must grow, so a future ID would block every
  later append (`stream_id_too_small`); it needs a separate "scheduled" structure. Later, if
  someone asks.
- Exponential backoff in the daemon: the SDK computes the wait.
- Priority between due retries and new entries beyond "due first".

## Open questions

1. E24.1 due delivery: (a) SDK tick + `claim {"due": true}` (small daemon change, ~1 s
   precision, one extra call per tick) or (b) `read` gives due entries first and wakes on the
   earliest `RetryAt` (no extra call, more daemon work, `read >` then differs from Redis when
   nack was used). Lean: (a) first, (b) if the tick cost shows in the bench.
2. Does `nack` need `consumer`? It guards against a worker that lost the entry to a claim
   nacking it anyway. Lean: yes.

## Use in fsw-esl (after this milestone)

The post-call outbox leaves RabbitMQ: the controller appends one job per call to
`postcall_jobs`; `jobs` runs `Work` with the five tiers, `MaxAge` 15 days and a dead-letter
stream. The `.retry.*` queues, `amqp_snapshot_retry` and probably the outbox sweeper go away
(the PEL is on disk with the job, so a lost message is no longer possible). Plan there:
`fsw-esl/.plans/` (to write when this one starts).

## Code

internal/streams/store.go: pending (add RetryAt)
internal/streams/service.go: Claim, claimCandidates, Read, Pending, Ack
internal/streams/service.go: Claim — `p.Delivered = now - *o.IdleMs` (E24.0)
sdk/go/stream.go: Group (Nack, PendingList)
sdk/go/stream_worker.go: Work, workIdle, handle
sdk/ruby/lib/ndb/stream.rb: Group (nack, pending_list)
sdk/ruby/lib/ndb/stream_worker.rb: Worker (work_idle)
sdk/ts/src/stream.ts: Group (nack, pendingList)
sdk/ts/src/stream-worker.ts: WorkOptions, work
sdk/ts/src/index.ts: exports (tiers)
spec/streams/claim.json, spec/streams/README.md
docs/dev/streams.md, docs/dev/http/streams.md
