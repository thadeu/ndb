# Ndb Console

A web console for [ndb](../README.md): open a local socket or a remote daemon, see its collections and disk use. One binary, `ndb-ui`, with the styles and scripts inside.

Tabs: Overview, Browser (filter, JSON tree editor, insert, delete), Dev Tools (raw requests), Watch (live tail, read-only), and Backups (download over the unix socket). See [docs/dev/ui.md](../docs/dev/ui.md).

## Run

```bash
bun install && bun run build
./dist/ndb-ui                      # http://127.0.0.1:4321
```

```bash
docker run -p 4321:4321 -e NDB_URL=tcp://10.10.0.1:7070 -e NDB_TOKEN=... ghcr.io/thadeu/ndb-ui
```

With no config, the console offers `unix:///run/ndb/ndb.sock`. Connections are saved in `$XDG_CONFIG_HOME/ndb/console.json` (or `~/.config/ndb/console.json`), mode 0600.

No login yet: do not expose it on a public network. Put it behind authentication (a VPN, an SSH tunnel, or a proxy with a login) when you expose it. It answers only for IP addresses, `localhost`, and the names in `NDB_UI_ALLOWED_HOSTS` (DNS rebinding), and it refuses a write whose `Origin` is another site (CSRF). See `src/guard.ts`.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `4321` | Listen port. |
| `HOST` | `127.0.0.1` | Listen address (`0.0.0.0` in the image). |
| `NDB_URL` | | One fixed connection: `unix:///path`, `tcp://host:port`, or `http(s)://...`. Hides the add form. |
| `NDB_TOKEN` | | Bearer token for `NDB_URL`. |
| `NDB_NAME` | `default` | Name of the fixed connection. |
| `NDB_UI_ALLOWED_HOSTS` | | Comma list of host names to answer for, besides IPs and `localhost` (`ndb.example.com,*.internal`). |
| `XDG_CONFIG_HOME` | `~/.config` | Where `ndb/console.json` lives. |

Behind a proxy, `X-Forwarded-Prefix` sets the mount path.

## Develop

```bash
mise install          # Bun 1.4
bun run dev
bun run typecheck
NDB_BIN=/path/to/ndb nice bun test
```
