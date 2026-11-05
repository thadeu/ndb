# CRUD

Insert a document, read it, change it, delete it, count the rest. It shows `if_version`, the optimistic lock of ndb.

## When to use

Your app owns the data and writes it through ndb (engine mode). Use `if_version` when two writers can change the same document.

## Collection

Declared in `examples/ndb.yml`:

<<< @/examples/ndb.yml#users{yaml}

## Connect

Each SDK reads `NDB_URL` and `NDB_TOKEN`. The Go and TypeScript code declares the collection from its type; Ruby declares it with `ensure_collection`. The declaration agrees with `ndb.yml`, and the indexes are united.

::: code-group

<<< @/examples/crud/go/main.go#connect [Go]

<<< @/examples/crud/ruby/main.rb#connect [Ruby]

<<< @/examples/crud/rails/main.rb#connect [Rails]

<<< @/examples/crud/ts/main.ts#connect [TypeScript]

:::

## Model (Go and Rails)

Go reads the `ndb` struct tags. `Ndb::Record` reads `attribute` lines. `_v` is the document version.

::: code-group

<<< @/examples/crud/go/main.go#model [Go]

<<< @/examples/crud/rails/main.rb#model [Rails]

:::

## Insert

One call inserts both users in one transaction. Every new document has version 1.

::: code-group

<<< @/examples/crud/go/main.go#insert [Go]

<<< @/examples/crud/ruby/main.rb#insert [Ruby]

<<< @/examples/crud/rails/main.rb#insert [Rails]

<<< @/examples/crud/ts/main.ts#insert [TypeScript]

<<< @/examples/crud/curl.sh#insert [curl]

:::

## Get by id

Ask for the version (`versions: true` in the query) so you can send it back.

::: code-group

<<< @/examples/crud/go/main.go#get [Go]

<<< @/examples/crud/ruby/main.rb#get [Ruby]

<<< @/examples/crud/rails/main.rb#get [Rails]

<<< @/examples/crud/ts/main.ts#get [TypeScript]

<<< @/examples/crud/curl.sh#get [curl]

:::

## Update with if_version

The write succeeds only if the stored version is the one you read.

::: code-group

<<< @/examples/crud/go/main.go#update [Go]

<<< @/examples/crud/ruby/main.rb#update [Ruby]

<<< @/examples/crud/rails/main.rb#update [Rails]

<<< @/examples/crud/ts/main.ts#update [TypeScript]

<<< @/examples/crud/curl.sh#update [curl]

:::

## Handle a version conflict

A second writer that still holds version 1 gets `version_conflict` (HTTP 409). Reload and try again. In Rails the error is `Ndb::StaleObjectError`, as in ActiveRecord.

::: code-group

<<< @/examples/crud/go/main.go#conflict [Go]

<<< @/examples/crud/ruby/main.rb#conflict [Ruby]

<<< @/examples/crud/rails/main.rb#conflict [Rails]

<<< @/examples/crud/ts/main.ts#conflict [TypeScript]

<<< @/examples/crud/curl.sh#conflict [curl]

:::

## Delete

Deleting a missing id is `not_found`. The programs ignore it at the start, so a second run works.

::: code-group

<<< @/examples/crud/go/main.go#delete [Go]

<<< @/examples/crud/ruby/main.rb#delete [Ruby]

<<< @/examples/crud/rails/main.rb#delete [Rails]

<<< @/examples/crud/ts/main.ts#delete [TypeScript]

<<< @/examples/crud/curl.sh#delete [curl]

:::

## Count

`count` is the total of the filter, whatever the page size.



::: code-group

<<< @/examples/crud/go/main.go#count [Go]

<<< @/examples/crud/ruby/main.rb#count [Ruby]

<<< @/examples/crud/rails/main.rb#count [Rails]

<<< @/examples/crud/ts/main.ts#count [TypeScript]

<<< @/examples/crud/curl.sh#count [curl]

:::

## Output

The four programs print the same text.

<<< @/examples/crud/output.txt{text}

## Notes

- The Rails tab runs with `ndb-rails` as a plain Ruby script. In a Rails app, `config/ndb.yml` does the connect step. See [Clients](/clients#rails-gem-ndb-record).
- Every SDK supports every step of this example.
- Errors and codes: [Query errors](/query#errors).

## Run it

```sh
cd examples
make run-crud LANG=go   # go, ruby, ts or curl
make run-crud           # every language
```

Source: [examples/crud](https://github.com/thadeu/ndb/tree/main/examples/crud). The programs read `NDB_URL` and `NDB_TOKEN`, and they can run again and again.
