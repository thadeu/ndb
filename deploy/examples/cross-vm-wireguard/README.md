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
| `docker-compose.apps.yml` | apps VM: Go and Node apps with `NDB_URL=tcp://ndb.internal:7070` |

## Steps

1. **WireGuard.** On both VMs, make keys (`wg genkey | tee private.key | wg pubkey > public.key`), fill the two `wg0-*.conf` templates, put them in `/etc/wireguard/wg0.conf`, and run `systemctl enable --now wg-quick@wg0`. Check with `ping 10.10.0.1` from the apps VM.
2. **ndb on the db VM.** Install the binary and the unit from [deploy/systemd](../../systemd/), copy `ndb.yml` to `/etc/ndb/ndb.yml`, and start it. The daemon listens on `10.10.0.1:7070` and on `/run/ndb/ndb.sock`. WireGuard must be up before ndb starts: add `After=wg-quick@wg0.service` and `Requires=wg-quick@wg0.service` to the unit (`systemctl edit ndb`).
3. **Firewall.** Load `nftables-db.conf` (or the same rules in your tool). Port 7070 is allowed on `wg0` only. Check from outside the tunnel: `nc -vz <db-vm-public-ip> 7070` must fail.
4. **Private DNS.** Make `ndb.internal` resolve to `10.10.0.1` on the apps VM. The compose file uses `extra_hosts` as a simple start.
5. **Apps VM.** `docker compose -f docker-compose.apps.yml up --build -d`, then `docker compose -f docker-compose.apps.yml logs -f`.

Check from the apps VM:

```bash
curl -s http://ndb.internal:7070/health
```

## Rules

- **Listen on the WireGuard address only.** Never `0.0.0.0` and never a public IP. There is no authentication on the TCP listener today, so the tunnel and the firewall are the protection.
- **Apps on the db VM keep the unix socket.** Apps on other VMs use `tcp://<private dns>:7070`.
- **Direct mode stays local to the db VM.** Apps on other VMs write through the API (engine mode). Never put NDJSON files on a network file system for ndb to read.
- **`data_dir` is on a local disk** of the db VM.
- **Online backup is a unix-socket call**, so run `ndb backup` on the db VM.
- **Ruby on another VM** has to wait for the TCP transport of the Ruby SDK (item E13.6 of milestone M13).

## Coming (E13.6)

Not in the daemon or the SDKs yet; do not rely on them:

- A bearer token on the TCP listener (`-token-file`, header `Authorization: Bearer ...`) as defense in depth on top of WireGuard. The unix socket stays token-free.
- The SDKs reading `NDB_URL` and a token (`NDB_TOKEN`) by themselves. The sample apps already read `NDB_URL` in their own code.
