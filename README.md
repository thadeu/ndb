# ndb

A query engine for NDJSON folders: one Go daemon, Mongo-style JSON queries,
SDKs for Go, Ruby and TypeScript.

Status: early development. See [`.plans/ndb-v1.md`](.plans/ndb-v1.md) and
[`.plans/milestones/`](.plans/milestones/).

## Develop

```
make test       # unit tests
make race       # tests with the race detector
make lint       # golangci-lint (wsl enforces blank lines)
make fuzz-smoke # each fuzz target for 30 s
```
