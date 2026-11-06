<p>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/png/lockup-on-dark.png">
    <img src="brand/png/lockup-on-light.png" alt="ndb" width="320">
  </picture>
</p>

# ndb

Drop NDJSON. Query it. NoSQL. ndb is a query engine for NDJSON: one Go daemon with a JSON query language (operators like `$eq`, `$in`, `$regex`) over HTTP, on a unix socket (and optional TCP with a bearer token). Go, Ruby and TypeScript SDKs call the same API, so the same query gives the same result in every language and from `curl`.

Your apps keep NDJSON as the source of truth: either they append to files (direct mode) or ndb keeps a write-ahead log for them (engine mode). ndb builds indexes and columnar segments from that data. If you delete the derived data, ndb rebuilds it.

> Status: pre-release (`v0.x`). The API may still change.

## Run

Install the binary (Linux and macOS, amd64 and arm64; the script checks the sha256):

```bash
curl -fsSL https://ndb.clowk.in/install.sh | bash
```

On a Linux server, add `--systemd`: it also creates the `ndb` user, `/var/lib/ndb`, `/etc/ndb/ndb.yml` (never overwritten) and starts `ndb.service`. The socket is `/run/ndb/ndb.sock`.

```bash
curl -fsSL https://ndb.clowk.in/install.sh | sudo bash -s -- --systemd
```

Or build from source. You need Go 1.26 or newer.

```bash
git clone https://github.com/thadeu/ndb && cd ndb
make build                                  # static binary in bin/ndb

mkdir -p /tmp/ndb
bin/ndb -data-dir /tmp/ndb/data -unix /tmp/ndb/ndb.sock
```

## Quick start (5 minutes)

Keep the daemon running, and open a second terminal.

```bash
alias nq='curl -s --unix-socket /tmp/ndb/ndb.sock -H "Content-Type: application/json"'

# 1. Is it ready?
nq http://ndb/health
# {"status":"ok"}

# 2. Insert. The first insert creates the collection.
nq -X POST http://ndb/v1/c/calls/insert -d '{"insert":[
  {"_id":"c1","status":"answered","dnis":"1133","duration":42,"ts":"2026-11-04T12:00:00Z"},
  {"_id":"c2","status":"failed","dnis":"1144","duration":0,"ts":"2026-11-04T12:30:00Z"},
  {"_id":"c3","status":"answered","dnis":"1133","duration":90,"ts":"2026-11-04T13:10:00Z"}]}'
# {"results":[{"id":"c1","v":1},{"id":"c2","v":1},{"id":"c3","v":1}]}

# 3. Find. The answer is one JSON document per line.
nq -X POST http://ndb/v1/c/calls/find -d '{"where":{"status":"answered"},"sort":{"ts":-1},"limit":10}'
# {"_id":"c3","status":"answered",...}
# {"_id":"c1","status":"answered",...}

# 4. Aggregate: calls by status and hour.
nq -X POST http://ndb/v1/c/calls/aggregate -d '{"pipeline":[
  {"$group":{"_id":{"status":"$status","hour":{"$dateTrunc":{"date":"$ts","unit":"hour"}}},
             "n":{"$count":{}},"avg":{"$avg":"$duration"}}},
  {"$sort":{"_id":1}}]}'

# 5. Declare an index (optional), then look at the collection.
nq -X PUT http://ndb/v1/c/calls -d '{"id":"_id","time":"ts","indexes":["status"]}'
nq http://ndb/v1/c/calls/stats
```

Stop the daemon with Ctrl-C. It finishes the running requests, then closes. Start it again with the same `-data-dir` and the data is there.

Next: [docs/dev/query.md](docs/dev/query.md) for the query language, and [docs/dev/clients.md](docs/dev/clients.md) for Go, Rails and TypeScript.

## Docker

```bash
docker run -d --name ndb \
  -v ndb-data:/var/lib/ndb -v ndb-run:/run/ndb \
  ghcr.io/thadeu/ndb:latest
```

The socket is `/run/ndb/ndb.sock`. Share the `/run/ndb` volume with your app containers. See [docs/dev/docker.md](docs/dev/docker.md).

## Download

`curl -fsSL https://ndb.clowk.in/install.sh | bash` installs the latest release (`NDB_VERSION=0.1.0` pins one; `uninstall.sh` removes it). Each `v*` tag publishes static binaries (no CGO) for Linux amd64/arm64 and macOS arm64, with a `checksums.txt`, at [Releases](https://github.com/thadeu/ndb/releases). The binary also has a CLI: `ndb stats`, `ndb size`, `ndb collections`, `ndb query`, `ndb compact`, `ndb quarantine`, `ndb restore`, `ndb backup`, `ndb help` ([docs/dev/operations.md](docs/dev/operations.md#cli)). A systemd unit is in [deploy/systemd/](deploy/systemd/). The image is `ghcr.io/thadeu/ndb`. Client SDKs: Go `github.com/thadeu/ndb/sdk/go`, Ruby gems `ndb` (plain client) and `ndb-rails` (Rails), npm `@thadeu/ndb`. See [CHANGELOG.md](CHANGELOG.md).

## Features

- NoSQL `find` with `$eq $ne $gt $gte $lt $lte $in $nin $exists $and $or $not`, sort, offset and cursor pagination.
- `aggregate` with `$match $group $sort $limit $skip $project`, `$dateTrunc` for time buckets.
- Two write modes: direct (apps append NDJSON files, ndb follows them) and engine (`insert`, atomic `update` and `delete` by filter with a WAL).
- `watch` and `ack` for outbox-style consumers, with an optional `where` filter and `wal_retention` to trim the WAL.
- Optional bearer token on the TCP listener (`-token-file`); the unix socket needs none.
- Secondary and unique indexes, built in the background.
- TTL per document, retention, compaction, and a blob store for large values.
- Quarantine for bad lines in direct mode. Online backup. Prometheus `/metrics`.
- One static binary, one data directory.

## Documentation

- [docs/dev/architecture.md](docs/dev/architecture.md): Components and data flow.
- [docs/dev/query.md](docs/dev/query.md): Query language, pagination and errors.
- [docs/dev/http-api.md](docs/dev/http-api.md): HTTP endpoints.
- [docs/dev/configuration.md](docs/dev/configuration.md): `ndb.yml`, flags, durability.
- [docs/dev/storage.md](docs/dev/storage.md): On-disk layout.
- [docs/dev/wal.md](docs/dev/wal.md): The NDJSON write-ahead log, fsync, recovery, truncation.
- [docs/dev/clients.md](docs/dev/clients.md): Go, Rails and TypeScript guides.
- [docs/dev/operations.md](docs/dev/operations.md): Backup, metrics, quarantine, recovery, compaction.
- [docs/dev/docker.md](docs/dev/docker.md): Docker image.
- [docs/dev/deployment.md](docs/dev/deployment.md): Single VM and cross-VM examples.
- [docs/dev/development.md](docs/dev/development.md): Tests and release.
- [docs/dev/releasing.md](docs/dev/releasing.md): Release checklist.
- [.plans/ndb-v1.md](.plans/ndb-v1.md): The design plan.

## Contributing

Bug reports and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first. It explains the tests, the code style and how to open a pull request. Maintainers: the release steps are in [docs/dev/releasing.md](docs/dev/releasing.md). Report security problems as described in [SECURITY.md](SECURITY.md), not in a public issue.

## Community

Use [GitHub Issues](https://github.com/thadeu/ndb/issues) for bugs and feature requests. Everyone who takes part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

The daemon is licensed under [AGPL-3.0](LICENSE). The SDKs in `sdk/` are MIT (see the gemspec; the Go and npm packages will carry the same license).
