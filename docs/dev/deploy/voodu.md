# Voodu

The [Single VM](single-vm.md) layout as voodu manifests: one `ndb` with a data volume and a socket dir, and one app per language that mounts the socket dir.

::: warning Templates
These manifests were not run. The block and field names follow a plain, generic style. Check them against the manifests you already deploy and adjust the syntax.
:::

The files are in [`deploy/examples/single-vm/voodu`](https://github.com/thadeu/ndb/tree/main/deploy/examples/single-vm/voodu).

## ndb

<<< @/deploy/examples/single-vm/voodu/ndb.voodu{hcl}

- `replicas = 1`: one daemon owns the data dir (`flock`). Never scale it to 2. To have the same data on another VM, run one more `ndb` there as a [copy](../replication.md) (`NDB_REPLICA_OF` in `vd config`), not a second replica of this one.
- The `data` volume is a local persistent disk, never NFS or EFS.
- The `socket` volume is a host path that every app mounts.
- `stop_timeout` above 30 s, so that running requests finish.

## An app

<<< @/deploy/examples/single-vm/voodu/app.voodu{hcl}

- `user = "1000:65532"`: group 65532 is the group of the socket, so the app can open it (the socket is `0660` by default).
- `NDB_URL` is the only setting the SDKs need. It can hold a [database](../databases.md) for each app on one daemon: `unix:///run/ndb/ndb.sock?db=billion`, or over TCP `tcp://:<token>@ndb-0.<scope>.voodu:7070/billion` (put that value in `vd config`, not in the file, because it holds the token).
- Add one block like this for each app.

## Apply

```bash
vd apply -f ndb.voodu -y
vd apply -f app.voodu -y
```

Apply ndb first. The apps retry until the daemon answers, so the order is only for a clean start.
