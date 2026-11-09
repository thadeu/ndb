<p>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/png/lockup-on-dark.png">
    <img src="brand/png/lockup-on-light.png" alt="ndb" width="320">
  </picture>
</p>

# ndb

**The API of Elasticsearch. The filters of MongoDB. One Go binary.**

ndb takes the routes developers know from Elasticsearch (`POST /logs/_search`, `_count`, `_bulk`, `_update_by_query`, index patterns like `logs-2026-*`, date math like `now-7d/d`) and the query language they know from MongoDB (`$in`, `$gte`, `$regex`, `$elemMatch`, `$inc`, `$group`), and runs them on plain NDJSON. No JVM, no cluster, no schema: one daemon on a unix socket (and optional TCP with a bearer token). Go, Ruby, Rails and TypeScript SDKs speak the same API, so the same query gives the same result in every language and from `curl`.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_search -d '{
  "query": { "status": { "$in": ["missed", "busy"] }, "ts": { "$gte": "now-1d/d" } },
  "sort": { "ts": -1 },
  "limit": 50
}'
```

Your apps keep NDJSON as the source of truth: either they append to files (direct mode) or ndb keeps a write-ahead log for them (engine mode). ndb builds indexes and columnar segments from that data. If you delete the derived data, ndb rebuilds it.

> Status: 1.0. The HTTP API is stable: routes and keys change only in a major release. See the [HTTP API](docs/dev/http.md).

## Run

Install the binary (Linux and macOS, amd64 and arm64; the script checks the sha256):

```bash
curl -fsSL https://ndb.clowk.in/install.sh | bash
```

Then `ndb serve` starts it with no flags and no root: data in `~/.ndb`, HTTP on `127.0.0.1:7070`.

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
nq http://ndb/_health
# {"status":"ok"}

# 2. Insert. The first insert creates the collection.
nq -X POST http://ndb/calls/_bulk -d '{"docs":[
  {"_id":"c1","status":"answered","dnis":"1133","duration":42,"ts":"2026-11-04T12:00:00Z"},
  {"_id":"c2","status":"failed","dnis":"1144","duration":0,"ts":"2026-11-04T12:30:00Z"},
  {"_id":"c3","status":"answered","dnis":"1133","duration":90,"ts":"2026-11-04T13:10:00Z"}]}'
# {"results":[{"_id":"c1","_v":1},{"_id":"c2","_v":1},{"_id":"c3","_v":1}]}

# 3. Search. The answer is a JSON array of documents.
nq -X POST http://ndb/calls/_search -d '{"query":{"status":"answered"},"sort":{"ts":-1},"limit":10}'
# [{"_id":"c3","status":"answered",...},{"_id":"c1","status":"answered",...}]

# 4. Aggregate: calls by status and hour.
nq -X POST http://ndb/calls/_aggregate -d '{"pipeline":[
  {"$group":{"_id":{"status":"$status","hour":{"$dateTrunc":{"date":"$ts","unit":"hour"}}},
             "n":{"$count":{}},"avg":{"$avg":"$duration"}}},
  {"$sort":{"_id":1}}]}'

# 5. Declare an index (optional), then look at the collection.
nq -X PUT http://ndb/calls -d '{"time":"ts","indexes":["status"]}'
nq http://ndb/calls/_stats
```

Stop the daemon with Ctrl-C. It finishes the running requests, then closes. Start it again with the same `-data-dir` and the data is there.

Next: [docs/dev/query.md](docs/dev/query.md) for the query language, and [docs/dev/clients.md](docs/dev/clients.md) for Go, Rails and TypeScript.

## Docker

```bash
docker run -d --name ndb \
  -v ndb-data:/var/lib/ndb -v ndb-run:/run/ndb \
  ghcr.io/thadeu/ndb:latest
```

The socket is `/run/ndb/ndb.sock`. Share the `/run/ndb` volume with your app containers. See [docs/dev/deploy/docker.md](docs/dev/deploy/docker.md).

## Download

`curl -fsSL https://ndb.clowk.in/install.sh | bash` installs the latest release (`NDB_VERSION=0.1.0` pins one; `uninstall.sh` removes it). Each `v*` tag publishes static binaries (no CGO) for Linux amd64/arm64 and macOS arm64, with a `checksums.txt`, at [Releases](https://github.com/thadeu/ndb/releases). The binary also has a CLI: `ndb stats`, `ndb size`, `ndb collections`, `ndb query`, `ndb compact`, `ndb truncate`, `ndb drop`, `ndb quarantine`, `ndb restore`, `ndb backup`, `ndb help` ([docs/dev/clients/cli.md](docs/dev/clients/cli.md)). A systemd unit is in [deploy/systemd/](deploy/systemd/). The image is `ghcr.io/thadeu/ndb`. Client SDKs: Go `github.com/thadeu/ndb/sdk/go`, Ruby gems `ndb` (plain client) and `ndb-rails` (Rails), npm `@thadeu/ndb`. See [CHANGELOG.md](CHANGELOG.md).

## UI (console)

`ndb-ui` is a self-hosted web console: connections (local socket or TCP with a token), Overview, a Browser with a JSON tree editor, Dev Tools for raw requests, a live Watch, and Backups. It runs on your side, with no account.

```bash
curl -fsSL https://ndb.clowk.in/install.sh | bash -s -- --ui && ndb-ui     # http://127.0.0.1:4321
docker run -p 127.0.0.1:4321:4321 -e NDB_URL=tcp://10.10.0.1:7070 -e NDB_TOKEN=... ghcr.io/thadeu/ndb-ui
```

A hosted version is planned for later. See [docs/dev/ui.md](docs/dev/ui.md).

## Features

- Search with `$eq $ne $gt $gte $lt $lte $in $nin $exists $regex $elemMatch $and $or $not`, date math (`now-7d/d`), sort, offset and cursor pagination, and collection patterns (`logs-2026-*`).
- `aggregate` with `$match $group $sort $limit $skip $project`, `$dateTrunc` for time buckets.
- Two write modes: direct (apps append NDJSON files, ndb follows them) and engine (insert, atomic update and delete by id or by query, with a WAL).
- `watch` and `ack` for outbox-style consumers, with an optional `query` filter and `wal_retention` to trim the WAL.
- Optional bearer token on the TCP listener (`-token-file`); the unix socket needs none.
- Secondary and unique indexes, built in the background.
- TTL per document, retention, compaction, and a blob store for large values.
- Quarantine for bad lines in direct mode. Online backup. Prometheus `/_metrics`.
- One static binary, one data directory.

## Documentation

- [docs/dev/architecture.md](docs/dev/architecture.md): Components and data flow.
- [docs/dev/query.md](docs/dev/query.md): find, filters, update and delete by filter, pagination.
- [docs/dev/aggregate.md](docs/dev/aggregate.md): the aggregate pipeline.
- [docs/dev/watch.md](docs/dev/watch.md): watch and ack for outbox consumers.
- [docs/dev/http.md](docs/dev/http.md): the HTTP API, the contract of ndb (routes in [docs/dev/http/](docs/dev/http/)).
- [docs/dev/configuration.md](docs/dev/configuration.md): `ndb.yml`, flags, durability.
- [docs/dev/storage.md](docs/dev/storage.md): On-disk layout.
- [docs/dev/wal.md](docs/dev/wal.md): The NDJSON write-ahead log, fsync, recovery, truncation.
- [docs/dev/clients.md](docs/dev/clients.md): the clients: Go, Ruby, Rails, TypeScript and the CLI, one page each.
- [docs/dev/ui.md](docs/dev/ui.md): the self-hosted web console (`ndb-ui`), with [install](docs/dev/ui/install.md), [Docker](docs/dev/ui/docker.md) and the [guide](docs/dev/ui/guide.md).
- [docs/dev/operations.md](docs/dev/operations.md): Backup, metrics, quarantine, recovery, compaction.
- [docs/dev/deployment.md](docs/dev/deployment.md): choose a setup: Linux (systemd), Docker, Single VM, Cross-VM, Voodu.
- [docs/dev/development.md](docs/dev/development.md): Tests and release.
- [docs/dev/releasing.md](docs/dev/releasing.md): Release checklist.
- [.plans/ndb-v1.md](.plans/ndb-v1.md): The design plan.

## Contributing

Bug reports and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first. It explains the tests, the code style and how to open a pull request. Maintainers: the release steps are in [docs/dev/releasing.md](docs/dev/releasing.md). Report security problems as described in [SECURITY.md](SECURITY.md), not in a public issue.

## Community

Use [GitHub Issues](https://github.com/thadeu/ndb/issues) for bugs and feature requests. Everyone who takes part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

The daemon is licensed under [AGPL-3.0](LICENSE). The SDKs in `sdk/` are MIT (see the gemspec; the Go and npm packages will carry the same license).
