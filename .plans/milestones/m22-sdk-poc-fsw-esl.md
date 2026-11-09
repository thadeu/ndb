# M22 — SDK fixes from the fsw-esl POC

Status: not started · Depends on: M08 (Go client), M11 (TS client) · Before: M20 (streams)

## Goal

Fix what the fsw-esl POC (Go, in Docker) found in the clients, before the
streams work: a test clean in the Go SDK like the one of Rails, a bounded
connection pool in the Go SDK, and one error constant per server code in
every SDK.

## Findings (fsw-esl POC, 2026-11-09)

1. **No test support in Go.** Rails has `Ndb::Testing` (truncate the
   collections a test used, collections per parallel worker); Go has
   nothing, so each app writes its own clean.
2. **The 1 s and 4 s peaks are from the client, not the engine.** The Go SDK
   has no limit of connections (`MaxConnsPerHost` 0, `MaxIdleConnsPerHost`
   16): at a peak, hundreds of connections open at once and the Docker
   network bridge fails. With a limit of 64 (a test on a copy of the SDK),
   the peaks go away and the total time goes from 6.2 s to 2.9 s.
3. **Unix socket was 2x faster than HTTP over TCP** (no TCP stack, no Docker
   bridge). Not a fix: the docs recommend the socket when the app and ndb
   share a host or a pod.
4. **`invalid_path` has no constant** in the Go SDK: the caller must read
   `(*ndb.Error).Code`. 17 server codes are missing in Go: `forbidden`,
   `immutable_id`, `internal`, `invalid_batch`, `invalid_blob_ref`,
   `invalid_doc`, `invalid_id`, `invalid_index`, `invalid_path`,
   `invalid_pattern`, `invalid_request`, `method_not_allowed`, `missing_id`,
   `not_supported`, `pattern_read_only`, `pattern_too_wide`, `unknown_route`.

## Decisions (2026-11-09)

- **No test server in the SDKs** (Go and TS): starting the daemon stays with
  the app (a service in CI, `make up`, Docker). The SDK only cleans.
- **HTTP/2: later**, and only if it is simple in Go. The pool of E22.2 is
  the fix of the peaks.

## Epics (in order)

### E22.1 Error constants in every SDK (first)

- [ ] One list of the server codes, read from the code by a test that fails
      when the server adds a code that an SDK does not have
- [ ] Go: the 17 missing `Err*` constants (`ErrInvalidPath` first), each
      matched by `errors.Is`
- [ ] TS (`ErrorCode`) and Ruby (error classes): the same check, and the
      missing ones
- [ ] `docs/dev/http/errors.md` stays the one table; each client page links
      to it

### E22.2 Connection pool in the Go SDK

- [ ] `ndb.WithMaxConns(n)`: `MaxConnsPerHost = n` and
      `MaxIdleConnsPerHost = n`; when the pool is full, a call waits for a
      free connection instead of opening one (the wait respects `ctx`)
- [ ] A default limit (64, the POC value; confirm with the bench below), so
      a new user does not hit the peaks
- [ ] `ndb.WithIdleTimeout(d)` (`IdleConnTimeout`)
- [ ] A bench in the SDK: 100, 500 and 1000 goroutines against one daemon,
      over unix and TCP, with no limit and with the default: p50, p99, max,
      total time, open connections; the numbers go in
      `docs/dev/clients/go.md`
- [x] Ruby and TS: their pools are bounded already (TS: undici `Pool` of
      16 connections; Ruby: `pool_size`, 4 by default in Rails), so the peak
      of the POC is a Go SDK problem only

### E22.3 Test clean in the Go SDK

The same model as `Ndb::Testing` of Rails, in the main package (no new
package, no daemon start):

```go
func TestMain(m *testing.M) {
	code := m.Run()

	ndb.Testing.Clean(context.Background(), db)   // the collections the tests used
	os.Exit(code)
}

t.Cleanup(func() { ndb.Testing.Clean(ctx, db) })   // or after each test
```

- [ ] The DB records the collections it used (`Coll`, `Collection`); 
      `Clean(ctx, db, opts...)` truncates them (fast at any size; keeps the
      definition and the indexes) and forgets them
- [ ] `Only(names...)`, `Except(names...)`; `CleanAll(ctx, db)` truncates
      every engine collection with the prefix, for the start of a run;
      direct collections and partitions are left out, as in Rails
- [ ] `WithPrefix("test1_")` on `Connect`: collections of their own per
      parallel package or worker, as `worker!` in Rails
- [ ] Guard: it runs only under `go test` (`testing.Testing()`), or with
      `NDB_ALLOW_CLEAN=1`; else `ErrUnsafeClean`. An unknown collection is
      not an error
- [ ] The same in TS (`clean(db)`, `cleanAll(db)`, prefix, guard with
      `NODE_ENV=test` or `NDB_ALLOW_CLEAN`), small, in the main package
- [ ] Tests, and a section in `docs/dev/clients/go.md` and
      `typescript.md`

## Exit criteria

- Every server error code has a constant in Go, TS and Ruby, and a test
  keeps it so
- With the default pool, the POC load has no 1 s or 4 s peaks, measured by
  the SDK bench
- A Go suite cleans its collections with one call, and the call refuses to
  run outside `go test`
- The client docs show the clean and the pool

## Out of scope

- A test server started by the SDK (the app starts the daemon)
- HTTP/2 (see Backlog)

## Backlog

- **HTTP/2 (h2c over the unix socket).** It shares one connection between
  many requests. Measure against HTTP/1.1 with the pool of E22.2 first; add
  it to the daemon and the Go SDK only if it wins and stays simple. It does
  not change the unix-vs-TCP gap.
