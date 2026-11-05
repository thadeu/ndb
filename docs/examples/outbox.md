# Outbox (watch and ack)

A producer inserts events. A consumer reads them with `watch`, a `where` filter and `ack`. After a restart it continues from its stored position.

## When to use

You need a reliable queue next to your data, without Kafka or Redis. Delivery is at least once, so the consumer must be safe to run twice on the same event.

## Collection

Declared in `examples/ndb.yml`:

<<< @/examples/ndb.yml#outbox{yaml}

## Produce

Each insert is one event. The topic is indexed.

::: code-group

<<< @/examples/outbox/go/main.go#produce [Go]

<<< @/examples/outbox/ruby/main.rb#produce [Ruby]

<<< @/examples/outbox/ts/main.ts#produce [TypeScript]

<<< @/examples/outbox/curl.sh#produce [curl]

:::

## The work, safe to repeat

The receipt id is the event id. A second delivery fails with `duplicate_id`, so the work is not done twice.

::: code-group

<<< @/examples/outbox/go/main.go#work [Go]

<<< @/examples/outbox/ruby/main.rb#work [Ruby]

<<< @/examples/outbox/ts/main.ts#work [TypeScript]

<<< @/examples/outbox/curl.sh#work [curl]

:::

## Consume, and crash before the ack

`where` leaves out other topics, but the stored position still moves over them. The program acks after the work. For `evt-3` it "crashes" after the work and before the ack.

::: code-group

<<< @/examples/outbox/go/main.go#consume [Go]

<<< @/examples/outbox/ruby/main.rb#consume [Ruby]

<<< @/examples/outbox/ts/main.ts#consume [TypeScript]

<<< @/examples/outbox/curl.sh#consume [curl]

:::

## Restart

The same consumer name resumes after the last ack. `evt-3` comes again, the receipt shows it was done, and the consumer acks it.

::: code-group

<<< @/examples/outbox/go/main.go#restart [Go]

<<< @/examples/outbox/ruby/main.rb#restart [Ruby]

<<< @/examples/outbox/ts/main.ts#restart [TypeScript]

<<< @/examples/outbox/curl.sh#restart [curl]

:::

## Output

The four programs print the same text.

<<< @/examples/outbox/output.txt{text}

## Notes

- The consumer name (`billing`) is the stored cursor. Keep it stable.
- `limit: 2` ends the stream after two events, so the example finishes. A real consumer leaves it out and loops.
- After `watch_lagged`, call `watch` again: it resumes after the last ack. `history_truncated` means `wal_retention` removed the position. See [watch and ack](/http-api#watch-and-ack).
- The Ruby model layer (`ndb-rails`) has no `watch`. Use the client, as this example does.
- Every SDK supports every step of this example.

## Run it

```sh
cd examples
make run-outbox LANG=go   # go, ruby, ts or curl
make run-outbox           # every language
```

Source: [examples/outbox](https://github.com/thadeu/ndb/tree/main/examples/outbox). The programs read `NDB_URL` and `NDB_TOKEN`, and they can run again and again.
