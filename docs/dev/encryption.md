# Encrypted fields

**ndb encrypts the fields you name, and still finds them by value.** A CPF, a card number, an API token or a `.pem` goes into the WAL, the segments, the backups and the logs as `"ndb:e1:k1:..."`, never as the plain value. A **blind field** also keeps a hash of the value, so `{"cpf":"390.533.447-05"}` finds the document, a unique key refuses a second CPF, and a `$group` counts per CPF. You add no library and no hash column in your app. Every client gets it, curl too.

```bash
curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/users \
  -d '{"blind":{"cpf":"numeric"},"encrypted":["card_token"],"unique":["cpf"]}'
```

| You want | You declare | You get |
|---|---|---|
| a value that nobody reads from the files | `"encrypted": ["pem"]` | AES-256-GCM before the WAL; `$exists` only |
| a value that you also find by value | `"blind": {"cpf": "numeric"}` | the same, plus `$eq`, `$in`, `$ne`, `$nin`, `unique`, `indexes`, `$group` |

## What it costs elsewhere

To find an encrypted value by equality, other databases need work in the app:

| | Search by value of an encrypted field |
|---|---|
| MongoDB | Client-Side Field Level Encryption or Queryable Encryption: a driver library, a key vault, setup in each app |
| PostgreSQL | `pgcrypto`, plus a hash column that the app computes and keeps in step |
| Elasticsearch | no native field encryption: the app encrypts and hashes |
| **ndb** | two keys in the collection definition: `encrypted` and `blind` |

## Quick start

1. Declare the fields. A field in `blind` is also encrypted.
2. Write documents as usual, with plain values.
3. Read with the SDK: it asks for the plain values. Over HTTP, add `?decrypt=1`.

::: code-group

```go [Go]
type User struct {
	ID   string `ndb:"_id"`
	Name string `ndb:"name"`
	CPF  string `ndb:"cpf"`
	Card string `ndb:"card_token"`
}

users, _ := ndb.Collection[User](db, "users",
	ndb.Blind("cpf", "numeric"), ndb.Unique("cpf"), ndb.Encrypted("card_token"))

users.Insert(ctx, User{ID: "u1", Name: "Ana", CPF: "390.533.447-05", Card: "tok_4242"})

found, _ := users.Query(ndb.Eq("cpf", "39053344705")).All(ctx) // found[0].CPF == "390.533.447-05"
```

```ts [TypeScript]
const users = db.collection('users', {
  fields: {
    name: { type: 'string' },
    cpf: { type: 'string', blind: 'numeric', unique: true },
    card_token: { type: 'string', encrypted: true },
  },
})

await users.insert({ _id: 'u1', name: 'Ana', cpf: '390.533.447-05', card_token: 'tok_4242' })

const found = await users.query({ cpf: '39053344705' }).all() // found[0].cpf === '390.533.447-05'
```

```ruby [Rails]
class User < Ndb::Record
  attribute :name, :string
  attribute :cpf, :string, blind: :numeric, unique: true
  attribute :card_token, :string, encrypted: true
end

User.create!(id: "u1", name: "Ana", cpf: "390.533.447-05", card_token: "tok_4242")

User.find_by(cpf: "39053344705").cpf # => "390.533.447-05"
```

```ruby [Ruby]
users = client.collection("users", blind: { cpf: "numeric" }, unique: %w[cpf], encrypted: %w[card_token])

users.insert(_id: "u1", name: "Ana", cpf: "390.533.447-05", card_token: "tok_4242")

users.query(cpf: "39053344705").first["cpf"] # => "390.533.447-05"
```

```bash [curl]
curl ... -X PUT /users -d '{"blind":{"cpf":"numeric"},"unique":["cpf"],"encrypted":["card_token"]}'
curl ... /users/_doc -d '{"_id":"u1","name":"Ana","cpf":"390.533.447-05","card_token":"tok_4242"}'

curl ... '/users/_search?decrypt=1' -d '{"query":{"cpf":"39053344705"}}'
# {"_id":"u1","name":"Ana","cpf":"390.533.447-05","card_token":"tok_4242"}

curl ... /users/_search -d '{"query":{"cpf":"39053344705"}}'
# {"_id":"u1","name":"Ana","cpf":"ndb:e1:k1:sV0m...","card_token":"ndb:e1:k1:Qe7x..."}
```

:::

## Blind fields

A blind field keeps a second value in the document, `<field>_bidx`: an HMAC-SHA256 of the normalized value (`"ndb:h1:k1:<hex>"`), with a key of its own. A filter on the field becomes a filter on that hash. The hash field is in no answer.

| Type | The hash of | `"390.533.447-05"` | For |
|---|---|---|---|
| `exact` (default) | the value as sent | `390.533.447-05` | tokens, ids |
| `lower` | lower case, no spaces at the ends | | an email |
| `upper` | upper case, no spaces at the ends | | a car plate, a code |
| `numeric` | only the digits (a zero at the start stays) | `39053344705` | a CPF, a card, a phone |

`blind` is an object (`{"cpf":"numeric","email":"lower"}`) or a list of exact fields (`["card_token"]`). The type of a field cannot change: the stored hashes would be wrong (`409 catalog_conflict`).

### Find, unique and index

| Works | Refused (`400 invalid_query`) |
|---|---|
| `{"cpf": "390.533.447-05"}`, `$eq`, `$in`, `$ne`, `$nin`, `$exists` | `$gt`, `$lt`, a range, `$regex`, a sort on the field |
| inside `$and`, `$or`, `$nor`, in `_count`, `_update_by_query`, `_delete_by_query`, `$match` | |
| `"unique": ["cpf"]`, `"indexes": ["cpf"]`, compound keys (`"org,cpf"`) | |

An index on a blind field is an index on `cpf_bidx`: the definition shows that name, and `explain` shows `index:cpf_bidx`. An encrypted field that is not blind accepts only `$exists`.

### Group by a blind field

`{"$group":{"_id":"$cpf"}}` groups by the hash, so `390.533.447-05` and `39053344705` are one group. The `_id` of each group is the value of one of its documents, plain for a read with `?decrypt=1` and the right. It also works as a key of a compound `_id`, and across partitions.

::: code-group

```ruby [Rails]
Order.group(:cpf).sum(:total) # => {"390.533.447-05" => 15, "111.222.333-44" => 7}
```

```bash [curl]
curl ... '/orders/_aggregate?decrypt=1' -d '{"pipeline":[{"$group":{"_id":"$cpf","total":{"$sum":"$total"}}}]}'
# {"_id":"390.533.447-05","total":15}
```

:::

`$first` and `$last` of an encrypted field give its value. `$sum`, `$max`, `$project` and the other stages that read the value are `400 invalid_query`. A `$sort` or a `$match` on the group `_id` after the group sees the hash.

## Who reads the plain value

An answer has the stored `"ndb:e1:k1:..."` by default. It has the plain value only when **both** are true:

1. The read asks for it: `?decrypt=1`.
2. The caller has the right: the unix socket, the token of `-token-file`, or a token with `decrypt` on the collection.

A caller without the right that sends `?decrypt=1` gets the stored value, not an error. The rule applies to `_doc`, `_search`, `_count`, `_aggregate`, `_watch`, the `doc` of `_update` and `_lock`, and to the documents that a lookup adds (with the right on the collection they come from).

```yaml
tokens:
  - name: kyc
    file: /etc/ndb/kyc.token
    read: [users]
    decrypt: [users]   # not part of admin: give it on its own
```

The SDKs send `decrypt=1` on every read. The console never does: it shows `[encrypted]`.

### Keep the values encrypted on a read

A list of secrets should show none of them. Read it with `encrypted`, and read one secret again when the user asks for it:

::: code-group

```go [Go]
list, _ := vault.Encrypted().Query(ndb.Eq("owner", "u1")).All(ctx) // "ndb:e1:k1:..."
secret, _ := vault.Get(ctx, id)                                     // the plain value

// every read of the collection encrypted:
vault, _ := ndb.Collection[Secret](db, "vault", ndb.Encrypted("value"), ndb.KeepEncrypted())
```

```ts [TypeScript]
const list = await vault.encrypted().query({ owner: 'u1' }).all()
const secret = await vault.get(id)
```

```ruby [Rails]
Secret.encrypted.where(owner: user.id) # the list
Secret.find(id)                        # the Show button
```

```ruby [Ruby]
vault.encrypted.query(owner: "u1").all
vault.get(id)
```

```bash [curl]
curl ... /vault/_search -d '{"query":{"owner":"u1"}}'
curl ... '/vault/_doc/s1?decrypt=1'
```

:::

A value that you read encrypted and send back in a write (an edit form) stays as it is: the daemon keeps it and does not encrypt it again.

## Writes

- `_doc`, `_bulk`, upsert, `_tx`, `$set` and `$setOnInsert` encrypt the field and set its hash.
- `$unset` removes the field and its hash.
- `$inc`, `$push`, `$min` and the other operators on an encrypted field are `400 invalid_query`: they need the value.
- An encrypted field is a top-level field. It cannot be `_id`, the `time` field, `expires_field`, a field of a stream, or a field of a direct-mode collection (the app writes those files).
- A field that you add to `encrypted` or `blind` later encrypts the old documents too. See [A field added later](#a-field-added-later).

## A field added later

When a field becomes `encrypted` or `blind`, the documents that you wrote before have the plain value. The daemon encrypts them in the background, as it builds a new index. You run no command.

1. New writes are encrypted at once.
2. A job reads each old document with a plain value, encrypts the field and sets its hash. It writes in batches of 500, through the WAL. Reads and writes do not wait for it.
3. While the job runs, `GET /{coll}` and `GET /{coll}/_stats` show `"encrypting":[{"field":"rcpt_to","state":"building","done":200,"total":16000}]`. A filter on a blind field also tests the plain value of the old documents, with the same normalization as the hash: `{"rcpt_to":"ana@dest.test"}` finds an old `"Ana@Dest.test"` for `lower`. The answer is complete. Such a filter does not use the index, so it scans.
4. At the end the state is `ready`, and the filter uses only the hash.

The job also starts at the open of a collection: for a field added to `ndb.yml` while the daemon was stopped, for a job that a stop cut, and for a data dir of 1.22.0 or older. The file `encrypted.json` in the collection dir lists the fields whose old documents are encrypted.

The old plain values stay in the WAL files and in older backups until `wal_retention` and your backup rotation drop them.

## The key

Each data dir has one key of 32 bytes. Two keys are derived from it once, at start: one for AES-256-GCM, one for the hashes. A read or a write does no key work and takes no lock.

| Key | Where | What it protects |
|---|---|---|
| default | made at the first start, kept in the data dir and in each backup | the API (tokens without `decrypt`), the logs, the console |
| `NDB_ENCRYPTION_KEY` (64 hex) | your secret store; only its fingerprint is in the data dir | all of that, plus a copy of the data dir, a disk or a backup |

`GET /_cluster/settings` shows the key masked (`e5af****…****edff`). See [The encryption key](security.md#the-encryption-key).

## The stored format

```
ndb:e1:k1:<base64url(nonce + ciphertext + tag)>    the value
ndb:h1:k1:<32 hex>                                  the hash of a blind field
```

`e1` and `h1` are the formats, `k1` the key: a key rotation will write `k2`. Each value is bound to its collection and field: a value copied to another field or collection does not decrypt.

## Cost

`make bench` writes and reads 2000 documents with a 2 KB `.pem`, plain and encrypted, on the same daemon:

| | Extra time (median) | Check |
|---|---|---|
| write | about 7% | at most 25% |
| read with `?decrypt=1` | about 7% | at most 25% |

The encryption of a CPF takes 0.1 µs; the hash 0.5 µs. A stored value is about 35% bigger than the plain one.

## What to encrypt

| Data | Do |
|---|---|
| a user password | do **not** encrypt it: keep a bcrypt or argon2 hash made by the app. Nobody needs to read it back |
| a card number, a CPF, a document number | `blind` (you find the record by it) |
| an email or a phone you search by | `blind` with `lower` or `numeric` |
| a `.pem`, a `.pfx`, an API token of a third party | `encrypted` |
| a note, an address | `encrypted` when it is personal data |

## Limits

- Equality only: no range, prefix, regex or sort on an encrypted field.
- The hash of a value is the same for each document. Who reads the files sees which documents have the same CPF, not the CPF.
- No key rotation yet.
- The daemon sees the value while it handles a request. A mode where the app holds the key is possible later with the same format.
