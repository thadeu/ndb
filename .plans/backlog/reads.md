# Backlog: reads

Found by `make bench` (1.9.0). Each item names the bench line that shows it
and the number to beat.

## 1. Top-N by a field reads the column, not the documents

`last 20 of a customer` (index on `customer`, sort `ts` desc, limit 20) takes
~185 ms for ~525 matches of 210k rows: the sort decodes the `_raw` of every
match to read `ts`. Reading the `ts` column of the matching rows, keeping the
top N, and decoding only those N documents should bring it near 20 ms.

It is also the cost of the `has_many` lookup, which runs one such search per
row: `20 customers with their last 5 events` takes ~1.2 s. Budget today:
`lookups.worst` ≤ 4 s (checks.go); lower it with this item.

## 2. Waking a big collection

The first call on the loaded partition after it slept (210k rows, 481 MB)
takes ~30 ms. The M19 goal was under 10 ms for 1 GB. Profile the open
(meta.bolt, key files, live bits, index files) and make the costly part
lazy. Budget today: `wake.loaded_ms` ≤ 100 ms.

## 3. `_aggregate` without `$group`

`[$match, $sort, $limit]` is refused (`$limit before $group is not
supported`), so the MongoDB shape `[$match, $sort, $limit, $lookup]` fails;
the bench uses `$group` first. Either run such a pipeline as a search, or
point the error to `_search` with `lookup`, which does the same.
