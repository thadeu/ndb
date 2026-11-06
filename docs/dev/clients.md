# Clients

Every SDK has two layers: a small transport (HTTP over a unix socket or TCP) and an idiomatic model layer that declares the collection (`PUT /v1/c/{coll}`) on first use and builds query JSON. The daemon decides what a query means. See [query.md](query.md).

| Client | For | Guide |
|---|---|---|
| Go | Go services and workers | [Go](clients/go.md) |
| Ruby | scripts, workers, any Ruby app | [Ruby](clients/ruby.md) |
| Rails | Rails apps, ActiveModel style (`Ndb::Record`) | [Rails](clients/rails.md) |
| TypeScript | Node.js apps and workers | [TypeScript](clients/typescript.md) |
| CLI | the terminal, shell scripts, admin | [CLI](clients/cli.md) |
| `curl` | anything with HTTP | [HTTP API](http-api.md) |

## Install

Sources are in `sdk/`: `sdk/go`, `sdk/ruby` (gem `ndb`), `sdk/rails` (gem `ndb-rails`), `sdk/ts`. They are published on pkg.go.dev, rubygems.org and npm.

| SDK | Install |
|---|---|
| Go | `go get github.com/thadeu/ndb/sdk/go@latest` (the module is tagged `sdk/go/vX.Y.Z`) |
| Ruby (plain client, no dependencies) | `gem "ndb"` |
| Rails (ActiveModel layer) | `gem "ndb-rails"` (installs `ndb`) |
| TypeScript | `pnpm add @thadeu/ndb` (the name `ndb` is taken on npm) |

## Address and token

Addresses are `unix:///run/ndb/ndb.sock` or `tcp://host:port`. Every SDK and the CLI speak both. Without an address, each SDK reads `NDB_URL`.

When the daemon runs with `-token-file`, the TCP listener needs a bearer token (the unix socket never does). Each SDK takes a `token` option, or reads `NDB_TOKEN`. The token goes only in the `Authorization` header. A wrong or missing token is `401 unauthorized`. See [configuration.md](configuration.md#authentication).

| SDK | Address | Token |
|---|---|---|
| Ruby | `Ndb::Client.new(url:)` or `socket:` | `Ndb::Client.new(token:)` |
| Rails | `url:` or `socket:` in `config/ndb.yml` | `token:` in the same place |
| Go | `ndb.Open(addr)`; empty `addr` reads `NDB_URL` | `ndb.Open(addr, ndb.WithToken(t))` |
| TypeScript | `connect(addr)`; no `addr` reads `NDB_URL` | `connect(addr, { token })` |

## Retry while the daemon starts

The daemon opens its socket before recovery ends and answers every request with HTTP 503 and the code `not_ready` ("ndb is recovering"; `/health` answers 503 `{"status":"recovering"}`, which the SDKs also read as `not_ready`). The socket is also missing or refuses connections while the daemon restarts. The Go, Ruby and TypeScript SDKs retry these cases by default:

- Backoff: start 50 ms, factor 2, cap 1 s, with jitter. Total deadline 10 s.
- When the deadline passes, the last error surfaces unchanged (`ErrNotReady` / `NotReady` / `NdbError` with code `not_ready`, or the connect error).
- Set the deadline with `WithReadyTimeout(d)` (Go), `ready_timeout:` in `Ndb::Client.new`, `config.ready_timeout` or `ready_timeout` in `config/ndb.yml` (Ruby, seconds), `{ readyTimeout }` in `connect()` (TypeScript, ms). `0` turns the retry off.
- Writes are safe: a write is retried only when it certainly was not processed (a failed connect, or a 503 `not_ready` answer). A write whose connection dropped after the request was sent is never retried.
- Reads (`find`, `count`, `aggregate`, `get`, `stats`, and `GET`) are idempotent, so they are also retried when the connection drops before an answer.
- A watch stream is retried only for the initial open, never mid-stream.
- The caller's context (Go) or `AbortSignal` (TypeScript) stops the wait at once. Ruby has no cancellation: the deadline bounds the wait. A timeout error is never retried.

## Direct mode from an app

An app in any language can skip the SDK for writes: declare the collection in `ndb.yml` with `write: direct` and append one JSON line per event to its own file. Use the SDK (or `curl`) to read. See [storage.md](storage.md#write-modes).

For a deployment with several apps and languages, see [Deploy](deployment.md).
