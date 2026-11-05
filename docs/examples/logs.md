# Logs (direct mode)

Other processes append NDJSON lines to files. ndb only reads them. You query the files in place, with no import.

## When to use

Your apps already write log or event files. Use direct mode when each app can keep its own file and ndb runs on the same machine.

## Collection

Declared in `examples/ndb.yml`:

<<< @/examples/ndb.yml#app_logs{yaml}

## The writer is not ndb

`append.sh` plays the app. It writes one JSON line per event to `data/logs/<app>-<pid>.ndjson`. The rules are: one writer per file, one line per `write()`, a newline at the end. It never talks to ndb.

<<< @/examples/logs/append.sh{bash} [append.sh]

## Filters and sort

`level` is an indexed equality, `ms >= 800` is a range, and the history filter uses the time field.

::: code-group

<<< @/examples/logs/go/main.go#filter [Go]

<<< @/examples/logs/ruby/main.rb#filter [Ruby]

<<< @/examples/logs/ts/main.ts#filter [TypeScript]

<<< @/examples/logs/curl.sh#filter [curl]

:::

## Regular expressions

`$regex` uses Go RE2. A literal prefix such as `^/api` can use an index when the field has one.

::: code-group

<<< @/examples/logs/go/main.go#regex [Go]

<<< @/examples/logs/ruby/main.rb#regex [Ruby]

<<< @/examples/logs/ts/main.ts#regex [TypeScript]

<<< @/examples/logs/curl.sh#regex [curl]

:::

## Cursor pages

Pass the cursor of one page as `after` for the next. It works only with the same `where` and `sort`.

::: code-group

<<< @/examples/logs/go/main.go#cursor [Go]

<<< @/examples/logs/ruby/main.rb#cursor [Ruby]

<<< @/examples/logs/ts/main.ts#cursor [TypeScript]

<<< @/examples/logs/curl.sh#cursor [curl]

:::

## A new line appears

The program starts `append.sh`, then waits until the new id is found. ndb sees file events at once, with a 5 s poll as a fallback.

::: code-group

<<< @/examples/logs/go/main.go#append [Go]

<<< @/examples/logs/ruby/main.rb#append [Ruby]

<<< @/examples/logs/ts/main.ts#append [TypeScript]

<<< @/examples/logs/curl.sh#append [curl]

:::

## Output

The four programs print the same text.

<<< @/examples/logs/output.txt{text}

## Notes

- A direct collection is read only. `insert` on it is `direct_collection`, so these programs have no write step.
- The Go struct and the TypeScript schema declare `id` and `time` as in `ndb.yml`. A different value is `catalog_conflict`. The Ruby client does not declare the collection at all.
- Every SDK supports every step of this example.
- More: [Storage](/storage#write-modes), [Clients](/clients#direct-mode-from-an-app).

## Run it

```sh
cd examples
make run-logs LANG=go   # go, ruby, ts or curl
make run-logs           # every language
```

Source: [examples/logs](https://github.com/thadeu/ndb/tree/main/examples/logs). The programs read `NDB_URL` and `NDB_TOKEN`, and they can run again and again.
