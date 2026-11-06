# Console (ndb-ui)

`ndb-ui` is a web console for ndb. It opens a local socket or a remote daemon, and has five tabs:

| Tab | What it does |
|---|---|
| Overview | collections, rows, size, indexes, TTL, and the disk bar |
| Browser | pick a collection, filter with `where`, page through documents, edit one as a JSON tree, insert, delete |
| Dev Tools | send raw HTTP requests (`METHOD /path` and a JSON body), see status, `X-Ndb-*` headers, and a tree of the answer |
| Watch | tail the changes of a collection live, read-only |
| Backups | download a backup file, and see the restore command |

It is one binary with the styles and scripts inside. It uses the same HTTP API as the SDKs, so it needs nothing from the daemon.

## Self-hosted

The console is **self-hosted only**. You run it on your machine or next to your daemons, and it talks to them directly. There is no account, no login, and nothing leaves your network. It is free, in the same repository and under the same license as ndb.

A **hosted console** (one place for all your daemons, with sign-in and teams) is planned for later. It does not exist yet, and the self-hosted console stays: the hosted version will be built on top of it, not instead of it.

| | Self-hosted (now) | Hosted (planned) |
|---|---|---|
| Where it runs | your machine, a VM, or a container | managed for you |
| Login | none; keep it on localhost or behind your own proxy | accounts and teams |
| Daemons | any socket or TCP address it can reach | daemons that you register |
| Tokens | `~/.config/ndb/console.json` on the console host | stored by the service |

## Run

::: code-group

```bash [Binary]
curl -fsSL https://ndb.clowk.in/install.sh | bash -s -- --ui
ndb-ui                         # http://127.0.0.1:4321
```

```bash [Docker]
docker run -p 127.0.0.1:4321:4321 \
  -e NDB_URL=tcp://10.10.0.1:7070 -e NDB_TOKEN=... \
  ghcr.io/thadeu/ndb-ui
```

```bash [Docker, unix socket]
docker run -p 127.0.0.1:4321:4321 -v /run/ndb:/run/ndb \
  -e NDB_URL=unix:///run/ndb/ndb.sock \
  ghcr.io/thadeu/ndb-ui
```

:::

With no config, the console offers `unix:///run/ndb/ndb.sock`. Add other daemons on the Connections page. A connection is `unix:///path`, `tcp://host:port`, or `http(s)://host`; for TCP, give the bearer token of the daemon (`-token-file`).

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `NDB_URL` | | One fixed connection. The add form is hidden and the config file is not used. |
| `NDB_TOKEN` | | Bearer token for `NDB_URL`. |
| `NDB_NAME` | `default` | Name of the fixed connection. |
| `PORT` | `4321` | Listen port. |
| `HOST` | `127.0.0.1` | Listen address (`0.0.0.0` in the image). |

## Security

- **Localhost only, no login.** Anyone who can open the page can read and write every collection of the connected daemons. Do not expose the console on a public network. To share it, put it behind a proxy that does the login, and set `X-Forwarded-Prefix` if it is not at the root.
- **The browser never gets a token.** Pages are drawn on the server, and every action is a POST route on the console, which adds the token. The pages only show "token set".
- **Tokens are kept in `~/.config/ndb/console.json`** (`$XDG_CONFIG_HOME/ndb/console.json`), mode `0600`. With `NDB_URL`, nothing is written to disk.
- Every POST checks that the `Origin` is the console page itself.

## Notes on each tab

- **Browser:** the filter, the collection, the page, and the selected `_id` are in the URL, so a link shows the same view. A save sends only the changed fields as `$set` and `$unset`, with the `_v` that the page was loaded with as a guard. If somebody else wrote first, nothing is written and the page says so. A direct-mode collection is read-only.
- **Dev Tools:** it only sends paths that start with `/v1/`, plus `/health` and `/metrics`. `/v1/backup` (use the Backups tab) and `watch` (use the Watch tab) are refused. The editor text and the last 50 requests are kept in the browser for each connection. *Copy as curl* writes `$NDB_TOKEN` as a placeholder, never the token.
- **Watch:** each stream opens a watch with a consumer named `ndb-ui-xxxxxxxx` and never acks. ndb stores a position row for every consumer name that it sees, so each stream leaves one row in `cursors.bolt`, at the start point of the stream. With `wal_retention`, the WAL files from the oldest such position are kept until the row is removed. ndb has no call to remove a consumer yet.
- **Backups:** the download streams `POST /v1/backup` and works on a unix socket connection only (ndb answers `403` over TCP; run `ndb backup` on the host). It can compress with gzip. Restore is not in the console, because it replaces the data directory while ndb is stopped. See [Backup and restore](operations.md#backup-and-restore).

## Discover

The New connection drawer has three fields: Name (optional; empty takes `local` for a socket or the host for TCP), URL or host, and Token. Type the full URL, or type a host and press **Discover**: the list under the field shows the daemons it found, and a click fills the URL.

- **Empty host, `localhost`, `127.0.0.1`, or `::1`:** the console looks for unix sockets: `listen.unix` of `ndb.yml` (`NDB_CONFIG`, `./ndb.yml`, `/etc/ndb/ndb.yml`), `/run/ndb/ndb.sock`, and each `*.sock` in `/run/ndb`, `/var/run`, `/run`, `/tmp`, `$XDG_RUNTIME_DIR`, and in the subfolders of `/tmp` with `ndb` in the name (at most 50). It also tries TCP on `127.0.0.1`.
- **Other host:** it tries TCP on the port in the field (`10.10.0.1:7071`), or on `7070`.
- Each candidate gets `GET /health` with an 800 ms timeout. `ok` shows the version and the collection count. `needs a token` is a `401`. `starting` is a `503` while ndb recovers. Anything else is dropped.
- The probe never sends a saved token. In Docker, mount the ndb socket volume, or the console cannot see the sockets.
- Discover needs JavaScript. Without it, type the full URL. With `NDB_URL`, the form is hidden.

