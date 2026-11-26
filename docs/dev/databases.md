# Databases

A **database** is a named set of collections in one daemon. Two databases can each have a `users` collection, with the same name and other data. Use one database per app when many apps share one ndb.

The **default** database is the data dir itself, as it was before databases existed. An app that sends no database uses it. Nothing changes for it: the same files, the same routes, the same answers.

## Choose the database

Send the header `Ndb-Database: <name>` on any collection route. No header, or `Ndb-Database: default`, is the default database.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/users/_doc \
  -H 'Ndb-Database: billion' -d '{"_id":"u1","name":"Ana"}'

curl --unix-socket /run/ndb/ndb.sock http://ndb/users/_doc/u1 -H 'Ndb-Database: billion'
```

- **Names:** `^[a-z0-9][a-z0-9_-]{0,62}$`. Another name is `400 invalid_database`.
- **A write makes the database** when it does not exist, as a write makes a collection. A read of a database that does not exist is `404 database_not_found`, and it makes nothing.
- **Routes:** every collection, document, query, watch and stream route, and `GET /_collections`, `/_stats`, `/_size`, `/_pitr`, `POST /_restore`, the blob routes and `POST /_backup` work in the database of the header.
- **Routes of the whole daemon** do not use the header: `/_health`, `/_metrics`, `/_log`, `/_cluster/settings`, `/_cluster/key`, `/_databases`.

## List and drop

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/_databases
# {"databases":[{"name":"default","collections":12},{"name":"billion","collections":9}]}

curl --unix-socket /run/ndb/ndb.sock -X DELETE 'http://ndb/_databases/billion?confirm=billion'
# {"dropped":"billion"}
```

A drop closes the streams and the watch streams of the database, closes its collections, and removes its dir in the background. The default database cannot be dropped. See [GET /_databases](http/system.md#get-databases).

## What a database shares

Each database has its own catalog, watch positions, blobs, collections, streams and PITR snapshots. All databases of a daemon share:

- the lock of the data dir (one daemon),
- the settings (`/_cluster/settings`) and the [encryption key](security.md#the-encryption-key). A key rotation seals the values of every database again, and ends when all of them are done,
- the write buffer budget and `memory_limit`,
- the settings of `ndb.yml`, except `collections:`, which is for the default database only.

## On disk

```
<data_dir>/
  catalog.bolt, cursors.bolt, blobs/, collections/   the default database
  _databases/
    billion/
      catalog.bolt, cursors.bolt, blobs/, collections/
```

A data dir with no other database has no `_databases` dir. The daemon opens every database at start, so TTL, retention and PITR run in all of them. A database dir that does not open stops the start, as a collection does. See [Storage](storage.md#layout).

## Backup

`POST /_backup` with no header (and `ndb backup`) saves the whole daemon: the default database, then each other database. Such a file has format 2 (`"ndb_backup":2`); a daemon with no other database still writes format 1, which older daemons restore. `ndb restore` puts every database back. With the header, `POST /_backup` saves that database alone, as the default database of the restored daemon. See [Backup and restore](operations.md#backup-and-restore).

## Tokens

A [scoped token](configuration.md#scoped-tokens) names collections of the default database, so it may not use another database (`403 forbidden`). Use the full token (`-token-file`) for other databases, and for `/_databases`. The unix socket needs no token.

## Not yet

- The database in the URL of the SDKs (`tcp://host:7070/billion`) and a database selector in the console (milestone M30, E30.3). Until then, send the header.
- A token scoped to one database.
- A restore of one database into a running daemon.
- Metrics per database: `/_metrics` covers the default database.
