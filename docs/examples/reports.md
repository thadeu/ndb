# Reports (aggregate)

Group, sum and average with a pipeline. The same JSON runs from every SDK and from `curl`.

## When to use

You need "orders per hour" or "revenue per status" straight from the data, without an export.

## Collection

Declared in `examples/ndb.yml`:

<<< @/examples/ndb.yml#orders{yaml}

## Paid orders per hour

`$match` keeps paid orders. `$group` uses `$dateTrunc` to make one group per hour, with `$count`, `$sum` and `$avg`. `$sort` after the group puts the best hour first.

::: code-group

<<< @/examples/reports/go/main.go#hourly [Go]

<<< @/examples/reports/ruby/main.rb#hourly [Ruby]

<<< @/examples/reports/ts/main.ts#hourly [TypeScript]

<<< @/examples/reports/curl.sh#hourly [curl]

:::

## Group by a field

A group key can be a plain field path.

::: code-group

<<< @/examples/reports/go/main.go#by-status [Go]

<<< @/examples/reports/ruby/main.rb#by-status [Ruby]

<<< @/examples/reports/ts/main.ts#by-status [TypeScript]

<<< @/examples/reports/curl.sh#by-status [curl]

:::

## Count with a filter

`count: true` returns the number of matches in the `X-Ndb-Total` header, whatever the page size.

::: code-group

<<< @/examples/reports/go/main.go#count [Go]

<<< @/examples/reports/ruby/main.rb#count [Ruby]

<<< @/examples/reports/ts/main.ts#count [TypeScript]

<<< @/examples/reports/curl.sh#count [curl]

:::

## Output

The four programs print the same text.

<<< @/examples/reports/output.txt{text}

## Notes

- The first run inserts 12 fixed orders. A later run finds them and does not insert again.
- A pipeline has exactly one `$group`. See [Aggregate](/query#aggregate).
- Go returns rows as maps (`AggregateAs` gives typed rows). Ruby and TypeScript return plain hashes and objects.
- Every SDK supports every step of this example. `ndb-rails` also has `group(:status).count` style calls.

## Run it

```sh
cd examples
make run-reports LANG=go   # go, ruby, ts or curl
make run-reports           # every language
```

Source: [examples/reports](https://github.com/thadeu/ndb/tree/main/examples/reports). The programs read `NDB_URL` and `NDB_TOKEN`, and they can run again and again.
