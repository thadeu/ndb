# Benchmarks

Scenario benchmarks of the daemon, outside CI. Run one before and after a
change of impact (storage, writer, query planner), and put both tables in
the pull request.

## ESL outbox (`ndb bench`, `make bench`)

The first real use of ndb: an ESL app that talks to FreeSWITCH writes 7 docs
per call (6 events and a `finishcall`, ~1 KB and ~4 KB) and workers consume
them. `ndb bench` starts a daemon from its own binary in a temp dir and
runs, in about two minutes:

1. **live**: 350 concurrent calls write one doc at a time at 400 docs/s
   (`durability: strict`), a `_watch` consumer acks them;
2. **crash**: `kill -9` while writing, restart, every acked write read back;
3. **load**: 30k calls (210k docs) bulk loaded into a daily partition;
4. **reads**: by id, by indexed fields, counts and aggregates.

The daemon RSS is sampled every 250 ms. The binary is all you need on the
machine to measure: copy `ndb` there and run `ndb bench`. Flags: `-live`,
`-rate`, `-calls`, `-dir`, `-keep`. The code is `internal/bench`.
