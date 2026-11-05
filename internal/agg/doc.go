// Package agg parses and runs the aggregation pipeline (plan §10.2, §11.3).
// It has no storage: the caller feeds it the matching documents (or their
// column values) and finishes with the pipeline stages that follow the group.
//
// # Shape
//
// A pipeline has exactly one $group. Before it only $match and one $sort are
// allowed; after it $match, $sort, $limit, $skip and $project. All the
// $match stages before the group are ANDed into one where, which the
// collection plans like a find (index, pruning, snapshot). A $sort before
// the group only sets the order of $first and $last; it is ignored when
// the group has neither.
//
// # Values
//
// A field path ("$a.b") has one value per document:
//
//   - the value itself, when no array is on the way;
//   - an array of the values (one level flattened), when the path ends in an
//     array or walks through one ("$items.sku"). The array is one value: a
//     group key, or a number for nothing. An empty array is an empty array.
//   - missing, when the path does not exist.
//
// The collection time field holds RFC 3339 strings as time values, so two
// offsets of one instant are one group key.
//
// # Group keys
//
// _id is a path, a constant, null, a $dateTrunc, or an object of those
// (a composite key). Missing and null are the same key (null): a group
// never has two null keys. Keys follow the cross-type order of package query
// (number < string < time < object < array < bool, null first); a composite
// key compares field by field, in the order the fields were written. Groups
// leave the table sorted by _id, so a pipeline with no $sort is deterministic
// and a cursor can resume it.
//
// # Accumulators
//
//	$count        the number of documents; its argument is {}.
//	$sum          the sum of the numbers. Non-numbers (missing, null,
//	              strings, arrays) are ignored. No number gives 0.
//	$avg          sum / count of the numbers. No number gives null.
//	$min, $max    the lowest / highest value in the cross-type order. Missing
//	              and null are ignored; nothing left gives null. So
//	              $max of [2, "x"] is "x".
//	$first,$last  the value of the first / last document of the group, with
//	              missing as null. Order: the $sort before the group when
//	              there is one (ties: document id), else insertion order.
//
// Sums are float64. Partial tables merge in segment order, so a sum of
// non-integers can differ from a sequential sum in the last bits.
//
// # $dateTrunc
//
// {"$dateTrunc": {"date": "$ts", "unit": "hour", "timezone": "Europe/Rome"}}
// cuts a time down to minute, hour, day, week (Monday) or month, in the
// time zone (default UTC). The result is the start of the bucket as an RFC
// 3339 string in UTC. A value that is not a time (missing, null, a number,
// a string that is not RFC 3339 or YYYY-MM-DD) gives null.
//
// # Pagination
//
// limit and skip cut the output of the last stage. after is the token of
// the previous page: the position in the final output, bound to the pipeline
// text. The pipeline runs again for each page, so rows can move between
// pages when data changes between requests (as plan §10.6 says for offset).
package agg
