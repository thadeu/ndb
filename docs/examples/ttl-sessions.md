# Expiring sessions

A document can expire by itself, like a key in Redis. You set the time with `expire_in`, read it with `ttl`, move it with `expire`, and keep it forever with `persist`. After the time, no read returns it.

## When to use

Sessions, tokens, caches and locks: data that must disappear by itself.

## Collection

Declared in `examples/ndb.yml`. `expires_field` names the field where the daemon stores the end time:

<<< @/examples/ndb.yml#sessions{yaml}

## Declare the expiry field from code

`ndb.yml` is enough for every SDK. Each SDK can also declare the field itself.

::: code-group

<<< @/examples/ttl-sessions/go/main.go#model [Go]

<<< @/examples/ttl-sessions/ruby/main.rb#declare [Ruby]

<<< @/examples/ttl-sessions/ts/main.ts#schema [TypeScript]

<<< @/examples/ttl-sessions/curl.sh#declare [curl]

:::

## Insert with expire_in

`expire_in` is a number of seconds or a text such as `"30m"` or `"1h"`. It applies to every document of the call, so the short session has its own call.

::: code-group

<<< @/examples/ttl-sessions/go/main.go#insert [Go]

<<< @/examples/ttl-sessions/ruby/main.rb#insert [Ruby]

<<< @/examples/ttl-sessions/ts/main.ts#insert [TypeScript]

<<< @/examples/ttl-sessions/curl.sh#insert [curl]

:::

## Read the time left

`ttl` returns the seconds left (rounded up), `-1` when the document never expires, and a not-found answer when it is missing or expired.

::: code-group

<<< @/examples/ttl-sessions/go/main.go#ttl [Go]

<<< @/examples/ttl-sessions/ruby/main.rb#ttl [Ruby]

<<< @/examples/ttl-sessions/ts/main.ts#ttl [TypeScript]

<<< @/examples/ttl-sessions/curl.sh#ttl [curl]

:::

## Extend one session

`expire` sets a new end time, like `EXPIRE` in Redis.

::: code-group

<<< @/examples/ttl-sessions/go/main.go#extend [Go]

<<< @/examples/ttl-sessions/ruby/main.rb#extend [Ruby]

<<< @/examples/ttl-sessions/ts/main.ts#extend [TypeScript]

<<< @/examples/ttl-sessions/curl.sh#extend [curl]

:::

## Keep one session forever

`persist` removes the end time, like `PERSIST` in Redis.

::: code-group

<<< @/examples/ttl-sessions/go/main.go#persist [Go]

<<< @/examples/ttl-sessions/ruby/main.rb#persist [Ruby]

<<< @/examples/ttl-sessions/ts/main.ts#persist [TypeScript]

<<< @/examples/ttl-sessions/curl.sh#persist [curl]

:::

## After the time passes

The expired document is hidden from every read at once. A sweeper removes it from the WAL later, and `watch` shows that delete with `"_expired": true`.

::: code-group

<<< @/examples/ttl-sessions/go/main.go#after [Go]

<<< @/examples/ttl-sessions/ruby/main.rb#after [Ruby]

<<< @/examples/ttl-sessions/ts/main.ts#after [TypeScript]

<<< @/examples/ttl-sessions/curl.sh#after [curl]

:::

## Output

The four programs print the same text.

<<< @/examples/ttl-sessions/output.txt{text}

## Notes

- Every SDK supports every step: `expire_in`, `ttl`, `expire`, `persist` and declaring `expires_field` from code (Go tag `expires`, Ruby `ensure_collection(expires_field:)`, TypeScript `expires` in the schema, curl `PUT`).
- `ttl: 24h` with a `time` field is a derived expiry: every document ends a fixed time after its `time` field. `expire` and `persist` fail on it with `ttl_derived`.
- Each `expire` or `persist` is a normal write: new version, `watch` event, and a `_v` filter works.
- More: [TTL like Redis](/configuration#ttl-like-redis) and [Compaction, retention and TTL](/operations#compaction-retention-and-ttl).

## Run it

```sh
cd examples
make run-ttl-sessions LANG=go   # go, ruby, ts or curl
make run-ttl-sessions           # every language
```

Source: [examples/ttl-sessions](https://github.com/thadeu/ndb/tree/main/examples/ttl-sessions). The programs read `NDB_URL` and `NDB_TOKEN`, and they can run again and again.
