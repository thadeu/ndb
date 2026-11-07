# Benchmarks

Scenario benchmarks of the daemon, outside CI. Run one before and after a
change of impact (storage, writer, query planner), and put both tables in
the pull request.

## ESL outbox (`ndb bench`, `make bench`)

The first real use of ndb: an ESL app that talks to FreeSWITCH writes 7 docs
per call (6 events and a `finishcall`, ~1 KB and ~4 KB) and workers consume
them. `ndb bench` starts a daemon from its own binary in a temp dir. With
no flag it runs all three parts, in about two minutes:

1. `-stress-load`: 350 concurrent calls write one doc at a time at 400
   docs/s (`durability: strict`) while a `_watch` consumer acks them; a
   `kill -9` while writing, then every acked write read back; 30k calls
   (210k docs) bulk loaded into a daily partition;
2. `-query`: by id, by indexed fields, sorted pages, a scan, counts;
3. `-aggs`: the dashboards of the ESL.

`-query` and `-aggs` load the calls first without measuring it.

The daemon RSS is sampled every 250 ms. The binary is all you need on the
machine to measure: copy `ndb` there and run `ndb bench`. Other flags:
`-live`, `-rate`, `-calls`, `-dir`, `-keep`. The code is `internal/bench`.
