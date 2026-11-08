# Backlog: reads

Found by `make bench` (1.9.0). Top-N reading the sort column was done in 1.10.0. Each item names the bench line that shows it
and the number to beat.

## 1. Waking a big collection

The first call on the loaded partition after it slept (210k rows, 481 MB)
takes ~30 ms. The M19 goal was under 10 ms for 1 GB. Profile the open
(meta.bolt, key files, live bits, index files) and make the costly part
lazy. Budget today: `wake.loaded_ms` ≤ 100 ms.

## 2. `_aggregate` without `$group`

`[$match, $sort, $limit]` is refused (`$limit before $group is not
supported`), so the MongoDB shape `[$match, $sort, $limit, $lookup]` fails;
the bench uses `$group` first. Either run such a pipeline as a search, or
point the error to `_search` with `lookup`, which does the same.
