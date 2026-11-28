# M32 — Ndb Cloud and the Elasticsearch door

Status: in progress (E32.4) · E32.0 decides if the Elasticsearch door goes on · Depends on: nothing (M31 is not needed, but it comes first) · Plan refs: anway-cloud `.plans/001-anway-cloud.md` (the model), the talk of 2026-11-26 and 27

## Goal

A hosted ndb at `cloud.ndb.clowk.in`, as Anway Cloud is for Anway: a
customer signs in, creates a **server** (a single-tenant ndb), and connects
apps with one URL over TLS:

```
tcp+tls://:<token>@qzu-mkfs-gan.us-east-1.ndb.clowk.in:7070/<database>
https://:<token>@qzu-mkfs-gan.us-east-1.ndb.clowk.in:7070/<database>     (the same; curl and any HTTP client)
```

The first customer is atendesimples. It can replace its OpenSearch with no
EC2 and a small change in its code:

| Today (AWS OpenSearch 2.17, us-east-1) | With ndb |
|---|---|
| about 200 GB, about US$ 900 a month | one Ndb Cloud server on the Cloud node (Oracle Cloud free tier) |
| filters and charts (aggregations) | the same queries, through the Elasticsearch door (`/_es`) |
| `elastic-builder` and the OpenSearch proxy of the app | the same builders, the base URL changes |

**The Cloud node:** an Oracle Cloud free tier VM (no cost, with no end
date): Ampere A1 (arm64), 2 OCPU, 24 GB RAM, a 150 GB boot volume and a
50 GB block volume for the data (`/dev/sdb`). The images of ndb are
already built for linux/arm64. The round trip from the Lambdas of
atendesimples depends on the Oracle region: Ashburn is in the same metro
area as AWS us-east-1 (1 to 3 ms). E32.0 measures it.

**Disk:** 50 GB of data volume against about 200 GB in OpenSearch. Parquet
is much smaller than the JSON of an index, but by how much is not known
yet: E32.0 measures it. A bigger block volume is the fix if it does not
fit (the free tier gives 200 GB of block storage in total).

## What atendesimples sends today

From `atendesimples/serveless/services/cdr-service/src/cdrs-charts`
(`QueryBuilderEs.js`, 425 lines, and 1 700 lines of charts) and
`atendesimples-app/app/services/elastic_search/`:

- **Query:** `bool` with `filter`, `must`, `must_not`, `should` and
  `minimum_should_match`; `term`, `terms`, `range`, `exists`; fields
  with `.keyword`.
- **Aggs, nested:** `terms` (with `size`, `missing`, `order`),
  `range`, `filter`, `filters`, `date_histogram` (`day`, time zone
  `-03:00`, `order`), `value_count`, `sum`, `avg`, `min`, `max`,
  `stats`, `composite`, `top_hits` (1 use), `nested` (2 uses).
- **Painless scripts** in a few aggs:
  `doc['redirects.duration'].size() > 0 ? doc['redirects.duration'][0] : 0`.
- **Writes:** one document at a time (`cdr_log/post_log.rb`) and from Lambdas.

## Decided

- **One server = one pod** (a Voodu statefulset) with its own data dir, as
  in Anway Cloud. No shared daemon between customers.
- **TLS at Caddy, by SNI**, with the TCP ingress of Anway Cloud on port
  7070 (`VOODU_CADDY_TCP_PORTS=7419,7070`). The pod has no public port. ndb
  stays plain HTTP inside: it gets no TLS code.
- **`tcp+tls://` is a second name of `https://`** in every SDK, the CLI
  and the console, so the URL looks like `ANWAY_URL`. `https://` keeps
  working.
- **The Elasticsearch door is a namespace: `/_es/...`.** In the native API
  a key with no `$` is a field, so `{"bool": {...}}` already means the
  field `bool`. A namespace keeps the native API as it is (HTTP API 1.x),
  and an OpenSearch or Elasticsearch client points its base URL to
  `https://host:7070/_es` with no other change. The answers have the
  Elasticsearch shape (`hits.total.value`, `hits.hits[]._source`,
  `aggregations.<name>.buckets[]` with `key`, `key_as_string`,
  `doc_count`).
- **What the door does not support is an error, never a guess:**
  `400 es_unsupported` names the clause (`script`, `match` with an
  analyzer, `nested` ...). Painless is out: a script of atendesimples
  becomes a field of the document, written by the app (the first redirect
  duration), or an aggregate expression.
- **`.keyword` is the same field.** ndb compares strings exactly; there is
  no analyzer.
- **The Cloud keeps the token and the encryption key of each server**
  (Lockbox, as Anway Cloud keeps the password). The pod gets them in env
  (`NDB_TOKEN`, `NDB_ENCRYPTION_KEY`), so a restore never needs a key that
  only the lost disk had.
- **Backups are part of the product from the first paid server:** daily,
  online (`ndb backup -`), streamed to **Cloudflare R2** (S3 API, no
  egress cost, off the node), 7 days kept, restore from the page.
- **One private monorepo, `thadeu/ndb-cloud`** (pnpm workspaces), with one
  design system for both UIs:
  - `packages/ui`: the design system. CSS (tokens, light and dark, the
    classes of each component), plain JS (menus, mega menu, split panel,
    table selection, the Dev Tools editor), icons, and a gallery page with
    the HTML of each component. No framework, as `@tdu/ui` today.
  - `apps/cloud`: the Cloud (Rails 8.1, Phlex). Deploy with `vd apply`.
  - `apps/ui`: the self-hosted UI (Bun + Hono), image
    `ghcr.io/thadeu/ndb-ui` (the same name as today, so no user breaks).
    Free to use; the code is not public, only the image (a usage license
    in the image and in the ndb README).
  The reference is the prototype in `ndb-cloud` (`design/`, the rules in
  `design/README.md`). The data screens (Collections, Dev Tools) are
  written once in `apps/ui`; the Cloud shows them for a server in proxy
  mode with signed headers, inside its own layout.
- **The console leaves the public ndb repo.** `console/` moves to
  `ndb-cloud/apps/ui` (git subtree, the history kept). The ndb repo keeps
  the daemon, the SDKs and the docs.
- **The self-hosted UI has the screens of the Cloud, without the pods:**
  Dashboard; Servers (your daemons: "Add connection" with a URL and a
  token, in place of "Create server"; the split panel with Details,
  Status, Monitoring, Connection, Collections, Backups, Replication,
  Settings, Tags); Dev Tools (the old Aggregate is in it); Collections;
  Backups ("Back up now", download, restore; no schedule); Restores (PITR
  and backups); Encryption keys; Replication; Upgrades (a notice only).
- **Only in the Cloud:** sign-in, accounts and the account switcher,
  billing, create server, sizes, regions, network access, alarms, events,
  managed backups (schedule, R2), Migrate from Elasticsearch (the
  self-hosted way is `ndb import es`).
- **No Tokens page and no Logs page for now.** The log level and the token
  names go in the Settings tab of a server (`GET`/`PUT /_log`,
  `/_cluster/settings`).
- **Replication is a screen of both UIs** (M31 is its base). In the Cloud,
  a customer sets it between servers of the same account: for example the
  owner in São Paulo and a copy in the US for a worker that runs there.
  The Cloud makes the `replicate` token, sets `NDB_REPLICA_OF` on the copy
  and keeps the same encryption key on both. Promote is a button with a
  typed confirmation, never automatic.

## Epics

Tests first in each epic. The ndb epics follow the repo rules (docs, spec
cases, every SDK). The Cloud and UI epics live in the private monorepo `ndb-cloud`,
with its own `.plans/001-ndb-cloud.md`.

**Order (decided 2026-11-27):** the Cloud first, the Elasticsearch door last.
The ndb parts (E32.4), the design system (E32.7), the self-hosted UI (E32.9),
the Cloud app (E32.5), backups (E32.6) and replication (E32.10) do not need
the door. E32.0 is the gate of the door (E32.1 to E32.3) and of the pilot
(E32.8) only: it measures if atendesimples fits, not if the Cloud works.

### E32.4 — ndb parts for the Cloud (ndb repo)

- [x] `tcp+tls://` = `https://` in Go, Ruby, Rails, TS, the CLI and the
      console; cases in `spec/url/cases.json`.
- [x] Move `console/` to `ndb-cloud/apps/ui` (git subtree). Remove it from
      the ndb repo: the `ndb-ui` and `ndb-stack` jobs of `ghcr.yml` move
      to `ndb-cloud` (`ndb-stack` takes the ndb binary of the release), and
      the docs (`docs/dev/ui.md`, the deploy pages) point to the image.
      Done: `cloud.yml` sends `ndb-release` to `ndb-cloud` after `release.yml`
      and `ghcr.yml`; `ndb-cloud` tests ndb-ui against the release binary,
      uploads the `ndb-ui-*` binaries to the public ndb release (for
      `install.sh --ui`) and pushes `ndb-ui` and `ndb-stack`, same version.
- [x] `NDB_TCP`: the TCP address from the env (the Voodu manifest has env
      only, and the image has no shell).
- [ ] `GET /_replication` gets the numbers of the Replication screen, per
      link: `remote_addr`, `connected_at`, `last_heartbeat_at`, `rtt_ms`
      (from the heartbeat), `bytes_per_s` and `records_per_s`, and
      `term`. On the owner, the same for each copy connected.
- [x] Backups to a remote place: `ndb backup -` already writes to stdout
      (`ndb backup - | aws s3 cp - s3://...`). Add a test that a backup of
      a server under writes, streamed this way, restores the same.

### E32.7 — Design system (ndb-cloud, packages/ui)

From the prototype (`ndb-cloud/design/`), with the rules of
`design/README.md`:

- [ ] Tokens in CSS variables, light and dark (`data-theme`), the theme
      switch kept per browser.
- [ ] Shell: top bar (logo, mega menu with the pages and a tip from the
      docs, search with Option+S, region, account), the 28 px breadcrumb
      bar, the side nav (account or connection switcher on top), footer.
- [ ] List page: title with count, refresh, actions, the primary split
      button, saved filter sets, filter, pages; the table (13 px, sort,
      checkboxes, the blue selected row).
- [ ] Split panel under a list: grip to resize, close, tabs.
- [ ] Details (key and value in 3 columns, copy buttons), boxes, tiles,
      alerts, modal, flash, switch, segmented control, charts (line,
      stacked bars).
- [ ] The Dev Tools editor (request and response, line numbers, the
      current request, Ctrl+Enter, the divider).
- [ ] The gallery page with every component. Responsive at 360 px.

### E32.9 — The self-hosted UI (ndb-cloud, apps/ui)

- [ ] The screens of "Decided", on `packages/ui`, with the data of the
      daemon (`/_health`, `/_metrics`, `/_stats`, `/_size`,
      `/_collections`, `/_databases`, `/_backup`, `/_restore`, `/_pitr`,
      `/_cluster/settings`, `/_log`, `/_replication`).
- [ ] Servers: add, edit and remove a connection (URL with the token and
      the database); saved in the config file as today.
- [ ] An optional password (`NDB_UI_PASSWORD`) with a session cookie, for
      a UI on a VM. Without it, the guards of today (Origin, Host).
- [ ] The image `ghcr.io/thadeu/ndb-ui` from `ndb-cloud`, on tags only (CI
      minutes of a private repo).

### E32.5 — The Cloud app (ndb-cloud, apps/cloud)

Copy anway-cloud (Rails 8.1, Phlex, Clowk, ContaGorda Pay, Anway jobs,
Voodu PAT plane), then change what is ndb:

- [ ] Servers: create (name, region, size), list, delete with the typed
      name. Manifests: statefulset (`NDB_TOKEN`, `NDB_ENCRYPTION_KEY`,
      `memory_limit` from the size, volume `data`) and TCP ingress on 7070
      with TLS.
- [ ] Health: running only when `GET /_health` answers over verified TLS;
      the reconcile every 2 minutes.
- [ ] Connection tab: the URL (Show/Copy, the token never in the page
      source), snippets for ndb-rails, Go, TS, curl, and an OpenSearch
      client on `/_es`.
- [ ] Usage tab: CPU and memory of the pod, disk (`/_size`), documents
      and collections (`/_stats`), requests per second (metrics).
- [ ] Settings tab: size and resize, rotate token, the token names and
      their scopes, the log level, restart, pause and resume, upgrade to a
      new ndb release (GitHub + ghcr check, as Anway Cloud), delete.
- [ ] Billing: a monthly subscription per paid server; sizes by disk too
      (ndb data grows, a queue does not). `config/cloud.yml`: region
      of the Oracle node (its id follows the Oracle region) with its capacity.

### E32.6 — Backups (ndb-cloud, apps/cloud)

- [ ] A daily job per server: `ndb backup -` streamed to Cloudflare R2
      (one bucket, a prefix per server, the R2 keys only on the Cloud).
      7 days kept (an R2 lifecycle rule).
- [ ] Backups tab: the list (time, size), "Back up now", download (a signed
      link), restore into the same server (stop, restore, start) or into a
      new server.
- [ ] A restore test in CI: a backup of a fixture data dir restores and
      answers the same.

### E32.10 — Replication screen (both UIs) and managed replication (Cloud)

- [ ] A canvas of the links: one node per server (name, region, role
      owner or copy, version, term) and one edge per link (owner to copy).
      The edge shows the state (streaming, resync, down) by color and
      motion, the lag (records and seconds), the round trip (`rtt_ms`),
      the rate (records/s, bytes/s), the remote address, connected since,
      the last heartbeat. A click on an edge or a node opens its details
      in the split panel, with the lag and rtt charts over time.
- [ ] Self-hosted: it reads `/_replication` of each connection and joins
      the links by address.
- [ ] Cloud: "Add a copy" on a server: pick the target server (another
      region of the same account) and the database; the Cloud does the
      token, `NDB_REPLICA_OF`, the key and the restart. "Promote" with a
      typed confirmation; "Remove copy".
- [ ] Alarms (Cloud): a link down for 2 minutes, a lag over 30 s.

### E32.0 — Viability (a gate for the Elasticsearch door and the pilot)

The maintainer gives the node and the access to OpenSearch.

- [ ] Round trip from a Lambda in us-east-1 to the Cloud node (`ping` and
      an HTTPS request).
- [ ] From the production OpenSearch: the mapping, the document count and
      the size of each index; export one month of CDRs (PIT +
      `search_after`) to NDJSON.
- [ ] Load it in ndb on the node: `time: ori_start_time`,
      `partition_by: month`, an index on the account field. Measure the
      load time, the disk (WAL and Parquet, with `wal_retention: 7d`) and
      the RSS.
- [ ] Write 3 charts by hand in the native `_aggregate` (TMA,
      Distribution, Evolution per day) and compare their numbers with
      OpenSearch for the same account and days.
- [ ] Time each chart: one account and 30 days, one account and 6 months,
      all accounts and 30 days. p50 and p99 of 20 runs, cold and warm.
- [ ] Project the full 200 GB: disk, RAM, the time of a chart over all
      months.

**Go** when: the charts give the same numbers; one account and 30 days is
under 500 ms at p99; the projected disk fits the node with 30% free; the
pod stays inside 2 OCPU and 8 GiB on the arm64 node. Else the miss and its cause go
in this file, and the plan stops or changes.

### E32.1 — The Elasticsearch door: search (ndb repo)

- [ ] Test: shared spec cases (`spec/es/`) give an Elasticsearch body and
      its answer; the daemon passes them. Bodies taken from the builders
      of atendesimples.
- [ ] `GET /_es/` (name, version: what the clients read on connect).
- [ ] `POST|GET /_es/{index}/_search` and `_count`; `{index}` can be a
      pattern (`cdrs-*`) or a list.
- [ ] Query DSL to the ndb filter: `bool` (`filter`, `must`, `must_not`,
      `should`, `minimum_should_match`), `term`, `terms`, `range` (with
      date math and `time_zone`), `exists`, `ids`, `match_all`,
      `prefix`, `wildcard` (to `$regex`).
- [ ] `from`, `size`, `sort`, `_source` (include and exclude),
      `search_after`, `track_total_hits`.
- [ ] `400 es_unsupported` for the rest, with the path of the clause.

### E32.2 — The Elasticsearch door: aggs (ndb repo)

- [ ] Test: each agg of the list, alone and nested 3 levels, against the
      answers of OpenSearch for the same data (fixtures kept in
      `spec/es/`).
- [ ] Bucket aggs: `terms` (`size`, `missing`, `order`, `min_doc_count`),
      `range`, `date_range`, `filter`, `filters`, `date_histogram`
      (`calendar_interval`, `fixed_interval`, `time_zone`, `order`,
      `min_doc_count`, `extended_bounds`), `composite` (`terms` and
      `date_histogram` sources, `after`).
- [ ] Metric aggs: `value_count`, `sum`, `avg`, `min`, `max`, `stats`,
      `cardinality` (exact; ndb has no HyperLogLog), `top_hits`
      (`size`, `sort`, `_source`).
- [ ] Run them on the engine of `_aggregate` (column reads, zone maps,
      every core): the door builds a plan, it does not scan in Go maps.
- [ ] `ndb bench -es`: the charts of E32.0 through the door, within 10%
      of the native `_aggregate`.

### E32.3 — The Elasticsearch door: writes and import (ndb repo)

- [ ] `PUT|POST /_es/{index}/_doc/{id}`, `POST /_es/{index}/_doc`,
      `GET` and `DELETE /_es/{index}/_doc/{id}`, `_update/{id}` (`doc`,
      `doc_as_upsert`).
- [ ] `POST /_es/_bulk` and `/_es/{index}/_bulk`: the NDJSON of
      Elasticsearch (`index`, `create`, `update`, `delete` lines), the
      answer per item, `errors`.
- [ ] `ndb import es <url> <index> [<collection>]`: PIT +
      `search_after`, resumable (the cursor in a file), `_bulk` into ndb,
      a count check at the end. It is also the tool of every customer who
      leaves Elasticsearch.

### E32.8 — Pilot: atendesimples

- [ ] Staging first (`datalake-staging`): `ndb import es` of the index,
      then the app writes to both (OpenSearch and ndb).
- [ ] The charts read from ndb behind a flag; a job compares the numbers
      of both for a week.
- [ ] The painless scripts become fields written by the app.
- [ ] Production: the same steps. OpenSearch is turned off after 30 days
      with no difference.

## Exit criteria

- [ ] E32.0 passed, with its numbers in this file.
- [ ] Every chart of atendesimples answers through `/_es` with the same
      numbers as OpenSearch (the compare job: no difference for 7 days).
- [ ] A customer goes from sign-in to a running server and a first write in
      under 5 minutes, with TLS on 7070.
- [ ] A backup restores into a new server and answers the same.
- [ ] The self-hosted image `ndb-ui` from `ndb-cloud` replaces the console
      of the ndb repo; the ndb repo has no `console/`.
- [ ] A copy set from the Cloud (owner in one region, copy in another)
      streams, and the Replication screen shows its lag and rtt.
- [ ] TestCompat passes: the native API and the data dir do not change.
- [ ] Coverage of the new ndb packages ≥ 90%.

## Out of scope

- Full-text search (analyzers, `match` with scoring, BM25).
- Painless and every other script.
- Kibana or OpenSearch Dashboards on ndb (the Cloud UI and the console
  show the data).
- Many nodes per server, and replication between regions (M31 is the base,
  later).
- SQL.
- A Tokens page and a Logs page (the token names and the log level are in
  the Settings tab of a server).
