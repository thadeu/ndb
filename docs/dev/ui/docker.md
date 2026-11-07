# Run the UI in Docker

The image is `ghcr.io/thadeu/ndb-ui` (linux amd64 and arm64). It listens on `0.0.0.0:4321` inside the container, so publish the port on `127.0.0.1` only: the console has no login.

## A daemon over TCP

```bash
docker run -d --name ndb-ui -p 127.0.0.1:4321:4321 \
  -e NDB_URL=tcp://10.10.0.1:7070 -e NDB_TOKEN=... \
  ghcr.io/thadeu/ndb-ui
```

## A daemon on the same host (unix socket)

Mount the folder of the socket. The console then opens it like a local file:

```bash
docker run -d --name ndb-ui -p 127.0.0.1:4321:4321 \
  -v /run/ndb:/run/ndb \
  -e NDB_URL=unix:///run/ndb/ndb.sock \
  ghcr.io/thadeu/ndb-ui
```

The console runs as `nonroot` (uid 65532), like the ndb image. With the default socket mode `0660`, it can open a socket made by the ndb image. For a daemon that runs as another user (systemd), set `listen.unix_mode` or run the container with that user's group (`--group-add`).

## Compose, next to ndb

`volumes_from` shares the socket volume of the ndb container (the image declares `/run/ndb` as a volume):

```yaml
services:
  ndb:
    image: ghcr.io/thadeu/ndb:0.3.1

  ndb-ui:
    image: ghcr.io/thadeu/ndb-ui:0.3.1
    volumes_from: [ndb]
    environment:
      NDB_URL: unix:///run/ndb/ndb.sock
    ports:
      - 127.0.0.1:4321:4321
```

## Many daemons

Without `NDB_URL`, the console keeps a list of connections in `/home/nonroot/.config/ndb/console.json`. Mount a volume there to keep the list:

```bash
docker run -d -p 127.0.0.1:4321:4321 \
  -v ndb-ui-config:/home/nonroot/.config/ndb \
  ghcr.io/thadeu/ndb-ui
```

In a container, **Discover** only sees the sockets that are mounted, and the hosts that the container can reach.
