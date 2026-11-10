# Install the UI

`ndb-ui` is one binary with the styles and scripts inside. It needs nothing from the daemon: it uses the same HTTP API as the SDKs.

## Install

```bash
curl -fsSL https://ndb.clowk.in/install.sh | bash -s -- --ui
```

The script installs `ndb-ui` instead of the daemon (`NDB_COMPONENT=ui` does the same). `NDB_VERSION=0.3.1` pins a version. Release files for Linux and macOS (amd64 and arm64) are at [Releases](https://github.com/thadeu/ndb/releases), with `ndb-ui-checksums.txt`.

## Run

```bash
ndb-ui                                   # http://127.0.0.1:4321
PORT=8080 ndb-ui                          # another port
NDB_URL=unix:///run/ndb/ndb.sock ndb-ui   # one fixed connection, no add form
```

With no config, the console offers `unix:///run/ndb/ndb.sock`. Add other daemons in the **New connection** drawer. A connection is `unix:///path`, `tcp://host:port`, or `http(s)://host`. For TCP, give the bearer token of the daemon (`-token-file`).

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `NDB_URL` | | One fixed connection. The add form is hidden and the config file is not used. |
| `NDB_TOKEN` | | Bearer token for `NDB_URL`. |
| `NDB_NAME` | `default` | Name of the fixed connection. |
| `PORT` | `4321` | Listen port. |
| `HOST` | `127.0.0.1` | Listen address (`0.0.0.0` in the image). |
| `NDB_UI_ALLOWED_HOSTS` | | Host names the console answers for, besides IP addresses and `localhost`. A comma list, such as `ndb.example.com,*.internal` (`*.internal` is every name under `internal`). |

## Protection

The console has no login. It keeps other sites out of it in two ways:

- **Host check (DNS rebinding).** A request whose `Host` is not an IP address, `localhost`, or a name in `NDB_UI_ALLOWED_HOSTS` gets `403`. A page on another site cannot point its own name at `127.0.0.1` and read the console. Behind a proxy, or with a name such as `ndb.example.com`, add that name to `NDB_UI_ALLOWED_HOSTS`. `/healthz` answers for any host.
- **Origin check (CSRF).** A `POST`, `PUT`, `PATCH`, or `DELETE` whose `Origin` is not the console page itself gets `403`, also a JSON request, and also a request that the browser marks `Sec-Fetch-Site: cross-site`. The forms and the tab actions also need the `Origin` of the console page.

These checks do not replace a login. **Put `ndb-ui` behind authentication (a VPN, an SSH tunnel, or a proxy with a login) when you expose it outside your machine.** `ndb-stack` (the daemon and the console in one image) is for development only: never use it in production.

## A daemon on another VM

Open the daemon socket over SSH, then add `tcp://127.0.0.1:7070` with no token:

```bash
ssh -N -L 7070:/run/ndb/ndb.sock user@vm
```

Or give the daemon a TCP address and a token (`listen.tcp` and `-token-file`, see [Configuration](../configuration.md)), and add `tcp://host:port` with the token.
