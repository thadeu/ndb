# Cross-VM over WireGuard

ndb runs on the db VM. Apps on other VMs reach it over TCP, inside a WireGuard tunnel.

```
  db VM (wg0 10.10.0.1)                     apps VM (wg0 10.10.0.2)
 ┌──────────────────────────────┐          ┌──────────────────────────────┐
 │ postgres, rabbitmq, anway... │          │ Go / Ruby / Node / Bun pods   │
 │ ndb  -tcp 10.10.0.1:7070     │◀─────────│ NDB_URL=tcp://ndb.internal:7070│
 │      -unix /run/ndb/ndb.sock │ WireGuard│ (private DNS → 10.10.0.1)     │
 │ data_dir on local disk       │ (UDP)    │                               │
 └──────────────────────────────┘          └──────────────────────────────┘
```

## Files

| File | Job |
|---|---|
| `ndb.yml` | db VM: unix socket plus TCP on `10.10.0.1:7070` |
| `wg0-db.conf`, `wg0-apps.conf` | WireGuard templates (fill in the keys and the public IP) |
| `nftables-db.conf` | firewall: 7070 on `wg0` only |
| `docker-compose.apps.yml` | apps VM: Go, Ruby and Node apps with `NDB_URL=tcp://ndb.internal:7070` and `NDB_TOKEN` |

## Steps

1. **WireGuard.** On both VMs, make keys (`wg genkey | tee private.key | wg pubkey > public.key`), fill the two `wg0-*.conf` templates, put them in `/etc/wireguard/wg0.conf`, and run `systemctl enable --now wg-quick@wg0`. Check with `ping 10.10.0.1` from the apps VM.
2. **ndb on the db VM.** Install the binary and the unit from [deploy/systemd](../../systemd/), copy `ndb.yml` to `/etc/ndb/ndb.yml`, make the token (see *Token* below), and start it. The daemon listens on `10.10.0.1:7070` and on `/run/ndb/ndb.sock`. WireGuard must be up before ndb starts: add `After=wg-quick@wg0.service` and `Requires=wg-quick@wg0.service` to the unit (`systemctl edit ndb`).
3. **Firewall.** Load `nftables-db.conf` (or the same rules in your tool). Port 7070 is allowed on `wg0` only. Check from outside the tunnel: `nc -vz <db-vm-public-ip> 7070` must fail.
4. **Private DNS.** Make `ndb.internal` resolve to `10.10.0.1` on the apps VM. The compose file uses `extra_hosts` as a simple start.
5. **Apps VM.** `NDB_TOKEN=<the token> docker compose -f docker-compose.apps.yml up --build -d`, then `docker compose -f docker-compose.apps.yml logs -f`.

Check from the apps VM:

```bash
curl -s -H "Authorization: Bearer $NDB_TOKEN" http://ndb.internal:7070/_health
```

Without the header the answer is `401 unauthorized`.

## Token

The tunnel and the firewall are the first protection. The bearer token is the second: if a rule is wrong, a stranger on the network still gets `401`. The unix socket stays token-free.

```bash
head -c 32 /dev/urandom | base64 > /etc/ndb/token
chown ndb:ndb /etc/ndb/token && chmod 600 /etc/ndb/token
systemctl edit ndb     # ExecStart=... -token-file /etc/ndb/token
```

The SDKs read `NDB_URL` and `NDB_TOKEN` by themselves (Go, Ruby and TypeScript), so the sample apps need no token code. To set them in code: Ruby `Ndb.connect(addr, token:)`, Go `ndb.Connect(addr, ndb.WithToken(t))`, TypeScript `connect(addr, { token })`.

## Rules

- **Listen on the WireGuard address only.** Never `0.0.0.0` and never a public IP. Add `-token-file` as defense in depth (see *Token*).
- **Apps on the db VM keep the unix socket.** Apps on other VMs use `tcp://<private dns>:7070`.
- **Direct mode stays local to the db VM.** Apps on other VMs write through the API (engine mode). Never put NDJSON files on a network file system for ndb to read.
- **`data_dir` is on a local disk** of the db VM.
- **Online backup is a unix-socket call**, so run `ndb backup` on the db VM.
