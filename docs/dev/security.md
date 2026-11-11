# Security

This page tells what ndb protects, what it does not protect, and what you must do. Read it before you put ndb on a network.

## Threat model

- ndb is a database for servers. Your apps, the console server and the CLI talk to it. A browser must not talk to it directly.
- ndb trusts every client that can open the unix socket. It trusts every TCP client that has a token.
- ndb does not trust web pages in a browser on the same machine. It does not trust clients on the network that have no token.
- ndb has no TLS, no user accounts and no encryption of data at rest. A proxy, the network and the disk give these.

## Listeners

ndb listens on a unix socket and, optionally, on TCP.

- **Unix socket** (`listen.unix`). The file permission is the access control. The default mode is `0660`: the owner and the group have full access, with no token. Put in the group only the users and containers that may read and write all data. Use `listen.unix_mode` or `-unix-mode` to change the mode.
- **TCP** (`listen.tcp`). A server install has no TCP listener. A user install (a laptop) listens on `127.0.0.1:7070` with no token. Bind TCP to `127.0.0.1` or to a private address only. Never bind it to a public address.

## Tokens and scopes

- `-token-file` or `tokens:` in `ndb.yml` make every TCP request send `Authorization: Bearer <token>`. A missing or wrong token is `401 unauthorized`.
- A scoped token can read, write or administer only some collections. A token without the right is `403 forbidden`. See [Scoped tokens](configuration.md#scoped-tokens).
- ndb compares tokens in constant time. Logs show the token name, never the secret.
- Make each token from 32 random bytes. Give each app its own token with the smallest scope.
- `POST /_backup` works only on the unix socket.

## Browser attacks (CSRF and DNS rebinding)

A web page in a browser can send requests to `127.0.0.1:7070`. ndb refuses two attacks.

**CSRF.** A page sends a "simple" request (for example `POST` with `Content-Type: text/plain`). The browser sends no preflight, but it adds the `Origin` header.

- ndb refuses each request that has an `Origin` header with `403 forbidden`. The message names the origin and the setting.
- ndb also refuses a request with `Sec-Fetch-Site: cross-site` or `same-site` (an image, a link from another site).
- curl, the SDKs, the CLI and the console server send no `Origin`. They work as before.
- This guard applies to every listener, the unix socket too.
- To let a browser app call ndb directly, put its origin in `cors_origins` (for example `https://app.example.com`). ndb then answers with `Access-Control-Allow-Origin` and `Vary: Origin`, and answers the preflight `OPTIONS` with `204`. Without an allowed origin, `OPTIONS` stays `405 method_not_allowed`. The page must still send a token: do this only with a token of a small scope.

**DNS rebinding.** A page at `evil.example` changes its DNS record to `127.0.0.1`. Then its requests go to ndb, and the page can read the answers. The browser sends `Host: evil.example`.

- On TCP with no token configured, ndb accepts only a `Host` that is an IP address, `localhost`, a name with no dot (a Docker Compose service such as `ndb`) or a name in `allowed_hosts`. Any other `Host` is `403 forbidden`.
- With a token, ndb does not check the `Host`: a rebound page cannot send the token.
- The unix socket does not check the `Host`.
- Use `allowed_hosts` for a private DNS name, for example `ndb.internal`, when the daemon has no token.

The best protection is a token, also on a laptop. With a token, a web page cannot read or write anything.

## Brute force

ndb counts the failed token checks (`401`) of each client IP.

- After `auth_failures` failures (default `10`) in `auth_window` (default `1m`), the IP gets `429 too_many_requests` for `auth_lockout` (default `5m`). The answer has a `Retry-After` header. The daemon writes a `warn` line `token lockout` with the IP.
- During the lockout, ndb refuses all TCP requests from that IP, also with the right token. Thus a guess cannot be tested.
- A request with the right token never counts as a failure.
- An IPv6 client counts by its `/64` network.
- ndb remembers at most 10 000 IPs. Over that, it forgets the IP it saw least recently.
- The unix socket has no token check, so it has no lockout. `auth_failures: 0` turns the lockout off.

**Behind a proxy**, all clients have the IP of the proxy. Then one bad client locks out all clients. Set `trust_proxy: true` to use the last hop of `X-Forwarded-For` (the address that the proxy added). Set it only when a proxy is in front: without a proxy, a client can write any `X-Forwarded-For`.

## TLS

ndb has no TLS. Plain HTTP is safe on the same host (the unix socket, `127.0.0.1`) and over an encrypted private network such as WireGuard ([Two VMs](deploy/cross-vm.md)). When clients come from a network that is not encrypted, you must put a TLS proxy in front. Voodu does this for you.

Caddy gets and renews the certificate:

```
ndb.example.com {
  reverse_proxy 127.0.0.1:7070
}
```

nginx:

```nginx
server {
  listen 443 ssl;
  server_name ndb.example.com;
  ssl_certificate     /etc/ssl/ndb.crt;
  ssl_certificate_key /etc/ssl/ndb.key;

  location / {
    proxy_pass http://127.0.0.1:7070;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_buffering off;          # _watch streams
    proxy_read_timeout 1h;        # _watch streams and block_ms reads
  }
}
```

Always set a token when a proxy publishes ndb. With a proxy, set `trust_proxy: true`.

## Data at rest

ndb writes plain files: the WAL is NDJSON, the segments are Parquet, the blobs are raw bytes, and a backup is NDJSON. Any user who can read the data dir can read all data.

- Keep the data dir owned by the `ndb` user with mode `0750` or less.
- Use disk encryption (LUKS, an encrypted cloud volume) when the data is sensitive.
- Encrypt the backups before you copy them off the host (for example `age` or `gpg`), and limit who can read them. A backup holds the [encryption key](#the-encryption-key) unless it comes from the env.
- A token file must have mode `0600`.

## The encryption key

Each data dir has one encryption key of 32 bytes. The [encrypted fields](#encrypted-fields) use it. At start the daemon takes, in this order:

1. `NDB_ENCRYPTION_KEY`: 64 hex characters (`openssl rand -hex 32`). It is not written to the data dir; only its fingerprint is, so a later start with no env, or with another key, stops with an error instead of reading nothing.
2. The key kept in the data dir, in `_system/settings.wal` (mode `0600`).
3. None: the daemon makes one, keeps it, and logs a `warn` line once.

`GET /_cluster/settings` shows the key masked (`e5af****…****edff`) and where it came from. No answer and no log line has the whole key.

What protects what:

| Leak | The key kept in the data dir (default) | The key in `NDB_ENCRYPTION_KEY` |
|---|---|---|
| a backup file | **not protected**: a backup holds the settings, the key with them | protected |
| a copy of the data dir or of the disk | **not protected** | protected |
| the memory of the daemon | not protected | not protected |

So the default makes a restore need nothing. To keep the key out of the files, give it in `NDB_ENCRYPTION_KEY` from a secret store, and keep a copy of it: a backup of a data dir with an env key can only be restored with the same key.

## Encrypted fields

A field in `encrypted` is not stored as you send it. Before the WAL, the daemon replaces its value with `"ndb:e1:k1:<base64url>"`: AES-256-GCM of its JSON (a string, a number or an object), with a random nonce. So the WAL, the segments, the backups and the logs never have the plain value.

```bash
curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/users -d '{"encrypted":["cpf","private_key"]}'
```

- `e1` is the format and `k1` the key. Each value is bound to its collection and field: a value copied to another field or collection does not decrypt.
- **Who reads the value:** the unix socket, the token of `-token-file`, and a token with `decrypt` on the collection get the plain value, on every read (`_doc`, `_search`, `_aggregate`, `_watch`, the `doc` of `_update` and `_lock`). Any other token gets the stored `"ndb:e1:k1:..."`.
- **Writes:** `_doc`, `_bulk`, upsert, `_tx` and `$set` encrypt the field. `$inc`, `$push` and the other operators on it are `400 invalid_query`: they need the value.
- **Filters:** a filter on the value of an encrypted field finds nothing: the stored value is encrypted. A blind index for `$eq` and `$in` is the next step.
- **Logs:** no request body or document of a collection with encrypted fields goes in a log line, at any level.
- A field added to `encrypted` later is encrypted from the next write of each document; the documents that are not written again stay as they are.
- The key protects the files only when it is in `NDB_ENCRYPTION_KEY` (see [The encryption key](#the-encryption-key)). With the default key, encrypted fields keep the value out of the API for tokens with no `decrypt`, the logs and the console.

## Logs

At the `debug` level, a request line has the body, and a `write` line has the documents (cut to `log_body_max`, default 2048 bytes). So debug logs contain your data.

- Use `debug` only for a short time. `PUT /_log` changes the level without a restart.
- Set `log_body_max: 0` to log no bodies.
- Logs never contain the `Authorization` header or a token secret, nor a body or a document of a collection with [encrypted fields](#encrypted-fields).

## Images

| Image | Use |
|---|---|
| `ghcr.io/thadeu/ndb` | the daemon, for servers. |
| `ghcr.io/thadeu/ndb-ui` | the console. It has no login: run it only behind authentication (a proxy with a login, a VPN) or on `127.0.0.1`. |
| `ghcr.io/thadeu/ndb-stack` | the daemon and the console in one image, **for development only**. It has no token by default and listens on `0.0.0.0`. Never use it in production. |

## Denial of service limits

ndb has limits, so that one request cannot stop the daemon:

- **Body size:** `max_line_size` of the collection (default 1 MiB), `blob_max_size` for blobs. Over it: `413 request_too_large`.
- **Regular expressions** use RE2: the time is linear, with no catastrophic backtracking. A pattern is at most 1 KiB.
- **Collection patterns** match at most 256 collections (`pattern_too_wide`).
- **Aggregate:** one group table is at most `group_limit` (`group_limit_exceeded`). A lookup attaches at most 10 000 documents (`lookup_too_large`).
- **Client cancel:** when the client leaves, ndb stops its query.
- **`max_conns`** limits the open TCP connections (default `0`, no limit). Over it, a new connection waits until another one closes. The SDK pools limit the connections of one app (Go `WithMaxConns`, Ruby `pool_size:`); `max_conns` limits the whole daemon. An idle keep-alive connection uses a slot for up to 60 s.
- **`max_watches`** limits the open `_watch` streams (default `0`, no limit). Over it, a new `_watch` is `429 too_many_requests` before the stream starts. `/_metrics` shows the open streams in `ndb_watch_streams`.
- **Slow headers:** a client has 10 s to send the request headers.

## Operator injection in apps

A query is a JSON object, and its values can be operators such as `$ne`, `$gt` or `$regex`. ndb has no `$where` and runs no code, but if an app puts user input in a query as an object, a user can change the query. For example, a login form that sends `{"password": {"$ne": ""}}` matches every user.

- Never pass user input as a query object. Read the input as a string or a number, then build the query in code: `{"email": email}` where `email` is a string.
- Check the type of each field from a request body before you use it in a query.
- Do not let users choose the collection name, the `sort` or the `fields` without an allow list.
