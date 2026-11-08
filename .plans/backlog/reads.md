# Backlog: reads

Found by `make bench` (1.9.0). Top-N reading the sort column was done in 1.10.0; counts, aggregations and the wake in 1.11.0. Each item names the bench line that shows it
and the number to beat.

## 1. Waking a big collection

The first call on the loaded partition after it slept (210k rows, 481 MB)
takes ~17 ms (it was 30 ms before 1.11.0: the page index and the bloom
filters are now read on first use, and the segment footers are decoded in
parallel). The M19 goal was under 10 ms for 1 GB. What is left is the
thrift decode of the footers (one per segment, ~40 columns x the row
groups): a smaller footer (no statistics in it, the page index has them)
or a lazy open of each segment would remove it. Budget: `wake.loaded_ms`
≤ 50 ms.

## 2. `_aggregate` without `$group`

`[$match, $sort, $limit]` is refused (`$limit before $group is not
supported`), so the MongoDB shape `[$match, $sort, $limit, $lookup]` fails;
the bench uses `$group` first. Either run such a pipeline as a search, or
point the error to `_search` with `lookup`, which does the same.
