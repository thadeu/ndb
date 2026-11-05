// Package query parses, validates and evaluates the JSON query language
// (plan §10.1, §10.6). It has no storage: the caller feeds it decoded
// documents and plans index use from Query.Indexable.
//
// # Type order
//
// One total order is used by sort, cursors and index keys. It follows the
// idea of a BSON-like order:
//
//	missing < null < number < string < time < object < array < bool
//
// Inside a type: numbers by value (float64; integers above 2^53 lose
// precision), strings by bytes, time by instant, false < true. Objects and
// arrays compare by their canonical JSON (sorted keys): stable and total,
// but not meaningful to a user.
//
// # Matching
//
//   - Missing is not null. {a: null} matches an explicit null only;
//     {a: {$exists: false}} matches a missing field. $ne and $nin are the
//     negation of $eq and $in, so they also match a missing field.
//   - $gt, $gte, $lt, $lte only compare values of the same type: {$gt: 1}
//     never matches a string.
//   - Arrays: a field matches when any element matches. When the operand is
//     an array, the whole array is compared too ({tags: ["a","b"]}).
//     Dot paths walk through arrays of objects ("items.sku").
//   - Time: when the field is the collection time field, RFC 3339 strings
//     (on both sides) are values of type time, so offsets compare as instants.
//     A string that does not parse stays a string.
//
// # Sort
//
// The document id is the last sort key, in the direction of the previous
// key, so the order is total. With no sort keys the order is insertion
// order (Pos.Seq). An array field sorts by its smallest element (ascending) or its
// largest (descending); an empty array counts as missing.
package query
