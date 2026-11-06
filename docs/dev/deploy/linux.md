# Linux (systemd)

Run ndb as a system service on a Linux host: one binary, one `ndb` user, one unit. This is the simplest way on a plain VM.

## Install

```bash
curl -fsSL https://ndb.clowk.in/install.sh | sudo bash -s -- --systemd
```

With `--systemd` the script also:

- creates the `ndb` user and group;
- creates `/var/lib/ndb` (data) and `/etc/ndb/ndb.yml` (config). It never overwrites an existing `ndb.yml`;
- installs and starts `ndb.service`.

Without `--systemd` it installs only the binary:

```bash
curl -fsSL https://ndb.clowk.in/install.sh | bash
```

- The script checks the sha256 of the release.
- `NDB_VERSION=0.1.2` pins a version.
- `NDB_INSTALL_DIR` sets the binary dir (default `/usr/local/bin`, else `~/.local/bin`).

## The unit

The unit is [`deploy/systemd/ndb.service`](https://github.com/thadeu/ndb/blob/main/deploy/systemd/ndb.service):

```ini
[Service]
User=ndb
Group=ndb
ExecStart=/usr/local/bin/ndb -config /etc/ndb/ndb.yml -unix-mode 0660
Restart=on-failure
TimeoutStopSec=40
StateDirectory=ndb
RuntimeDirectory=ndb
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/ndb /run/ndb
```

- `StateDirectory` and `RuntimeDirectory` create `/var/lib/ndb` and `/run/ndb`, owned by `ndb`.
- `-unix-mode 0660`: apps in the `ndb` group can open the socket. Add the app user to the group: `usermod -aG ndb <app-user>`.
- `TimeoutStopSec=40`: the daemon waits up to 30 s for running requests on SIGTERM.

The config is [`deploy/systemd/ndb.yml`](https://github.com/thadeu/ndb/blob/main/deploy/systemd/ndb.yml). `data_dir` must be `/var/lib/ndb` and `listen.unix` must be `/run/ndb/ndb.sock`, to match the unit:

```yaml
data_dir: /var/lib/ndb

listen:
  unix: /run/ndb/ndb.sock
```

Add collections and the other keys from [Configuration](../configuration.md).

## Day to day

```bash
systemctl status ndb
journalctl -u ndb -f
curl --unix-socket /run/ndb/ndb.sock http://ndb/health
ndb stats
```

## TCP and token

To serve apps on other hosts, listen on a private address and require a token:

```bash
head -c 32 /dev/urandom | base64 > /etc/ndb/token
chown ndb:ndb /etc/ndb/token && chmod 600 /etc/ndb/token
systemctl edit ndb     # ExecStart=... -tcp 10.10.0.1:7070 -token-file /etc/ndb/token
```

Never listen on `0.0.0.0` or a public IP. The full setup with WireGuard is [Cross-VM](cross-vm.md).

## Upgrade and remove

- Upgrade: take a backup, run the install command again (or with `NDB_VERSION`), then `systemctl restart ndb`.
- Remove: `curl -fsSL https://ndb.clowk.in/uninstall.sh | sudo bash -s -- --systemd` stops and removes the unit and keeps the data. Add `--purge` to delete `/var/lib/ndb`, `/etc/ndb` and the user.
