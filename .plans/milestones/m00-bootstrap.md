# M00 — Bootstrap

Status: done (benchmark helpers moved to M01 bench files) · Depends on: — · Plan: §14

## Goal

A repo where every later milestone can write its tests first: tooling,
CI, the `fs` interface with fault injection, and the harnesses for crash,
golden and benchmark tests.

## Epics

### E0.1 Repo and tooling

- [ ] `go.mod` (module path to decide), Go version pinned
- [ ] Layout: `cmd/ndb/` (daemon), `cmd/ndbgen/` (data generator), `internal/...`, `sdk/{go,ruby,ts}/` (see plan §18), `spec/cases/`
- [ ] `golangci-lint` with `wsl` (blank-line rules), `gofumpt`, `errcheck`, `govet`
- [ ] `Makefile`: `test`, `race`, `fuzz-smoke`, `lint`, `bench`
- [ ] CI: lint, `go test ./...`, `go test -race ./...`, fuzz smoke (30 s per target)
- [ ] CI path filters: `sdk/<lang>/` runs that SDK + the shared suite; `internal/` or `spec/` runs everything
- [ ] Empty `sdk/go/go.mod` (own module, stdlib only; tags `sdk/go/vX.Y.Z`)

### E0.2 `fs` interface

- [ ] Interface: `Create`, `OpenAppend`, `Open`, `Write`, `Sync`, `Rename`, `SyncDir`, `Fallocate`, `Truncate`, `Remove`, `Stat`
- [ ] `osfs`: real implementation (Linux + macOS; `Fallocate` is a no-op on macOS)
- [ ] `faultfs`: wraps another `fs`; fails `Write`/`Sync`/`Rename` at the Nth call, or writes only part of a buffer
- [ ] Tests for `faultfs` itself

### E0.3 Test harnesses

- [ ] **Crash harness**: runs a child process that writes in a loop; sends `SIGKILL` at a random time; the parent restarts and checks invariants
- [ ] **Golden runner**: a script that runs the same query in DuckDB CLI and compares the result; skipped when `duckdb` is not installed (DuckDB is never a Go dependency)
- [ ] **Benchmark scaffold**: shared helpers to load generated data and report MB/s, p50/p99

### E0.4 Data generator (`ndbgen`)

- [ ] Writes N GB of NDJSON with a configurable schema (strings, ints, floats, times, nested objects, arrays)
- [ ] Options: type conflicts (`age` as int and string), missing fields, large values, repeated ids (updates), `_deleted` lines
- [ ] Deterministic with a seed

### E0.5 Shared test case format

- [ ] Define `spec/cases/*.json`: setup (catalog + writes), request, expected rows (order-sensitive or not)
- [ ] One Go runner that executes a case against the library API (later reused over HTTP and by the clients)

## Exit criteria

- [ ] CI is green on the skeleton
- [ ] `faultfs` tests pass
- [ ] `ndbgen` generates 1 GB with a seed, and two runs give the same bytes
- [ ] The crash harness kills a demo writer 1 000 times and checks a trivial invariant
- [ ] One sample case in `spec/cases/` runs through the Go runner

## Out of scope

- Any storage or query code.
