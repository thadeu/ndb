// Package streams is the append-only log with consumer groups of
// docs/dev/streams.md: the behavior of Redis Streams on an engine
// collection, with the group state (PEL) in one bbolt file per stream.
package streams

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/thadeu/ndb/internal/query"
)

// ID is an entry ID: <ms>-<seq>.
type ID struct {
	Ms  uint64
	Seq uint64
}

// Max is the highest ID.
var Max = ID{math.MaxUint64, math.MaxUint64}

func (id ID) String() string {
	return strconv.FormatUint(id.Ms, 10) + "-" + strconv.FormatUint(id.Seq, 10)
}

// Key is the _id of the entry on disk: both numbers padded to 20 digits, so
// that the text order of the engine is the ID order.
func (id ID) Key() string {
	return fmt.Sprintf("%020d-%020d", id.Ms, id.Seq)
}

func (id ID) Less(o ID) bool {
	return id.Ms < o.Ms || id.Ms == o.Ms && id.Seq < o.Seq
}

func (id ID) IsZero() bool {
	return id == ID{}
}

// Next is the ID right after id.
func (id ID) Next() ID {
	if id.Seq == math.MaxUint64 {
		return ID{id.Ms + 1, 0}
	}

	return ID{id.Ms, id.Seq + 1}
}

var keyRe = regexp.MustCompile(`^(\d{20})-(\d{20})$`)

// FromKey reads a padded _id.
func FromKey(k string) (ID, bool) {
	m := keyRe.FindStringSubmatch(k)

	if m == nil {
		return ID{}, false
	}

	ms, err1 := strconv.ParseUint(m[1], 10, 64)
	seq, err2 := strconv.ParseUint(m[2], 10, 64)

	return ID{ms, seq}, err1 == nil && err2 == nil
}

// Parse reads <ms>-<seq>, or <ms> alone with seq as the given default (0 as
// a start, max as an end, as in Redis). "-" is the lowest ID and "+" the
// highest.
func Parse(s string, seqDefault uint64) (ID, error) {
	switch s {
	case "-":
		return ID{}, nil
	case "+":
		return Max, nil
	}

	msText, seqText, hasSeq := strings.Cut(s, "-")

	ms, err := strconv.ParseUint(msText, 10, 64)
	if err != nil {
		return ID{}, invalidID(s)
	}

	if !hasSeq {
		return ID{ms, seqDefault}, nil
	}

	seq, err := strconv.ParseUint(seqText, 10, 64)
	if err != nil {
		return ID{}, invalidID(s)
	}

	return ID{ms, seq}, nil
}

func invalidID(s string) error {
	return query.NewError("invalid_stream_id", "%q is not an entry ID (<ms>-<seq>)", s)
}

var paddedRe = regexp.MustCompile(`"_id":"(\d{20})-(\d{20})"`)

// Unpad turns every padded "_id" of a JSON row back into <ms>-<seq>.
func Unpad(row []byte) []byte {
	return paddedRe.ReplaceAllFunc(row, func(m []byte) []byte {
		id, _ := FromKey(string(m[7 : len(m)-1]))

		return []byte(`"_id":"` + id.String() + `"`)
	})
}

// PadQuery turns the _id values of the query of a find body into padded
// keys: an ID in a filter is <ms>-<seq> (or <ms>), the _id on disk is
// padded. An <ms> alone is <ms>-0, or <ms>-<max> with $lte and $gt, as in
// Redis ranges. A value that is not an ID is left as it is.
func PadQuery(body []byte) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return body, nil
	}

	var top map[string]json.RawMessage

	if err := json.Unmarshal(body, &top); err != nil || top["query"] == nil {
		return body, nil
	}

	var q map[string]any

	dec := json.NewDecoder(bytes.NewReader(top["query"]))
	dec.UseNumber()

	if err := dec.Decode(&q); err != nil || q == nil {
		return body, nil
	}

	padWhere(q)

	raw, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}

	top["query"] = raw

	return json.Marshal(top)
}

func padWhere(q map[string]any) {
	for k, v := range q {
		switch k {
		case "_id":
			q[k] = padValue(v, 0)
		case "$and", "$or", "$nor":
			if list, ok := v.([]any); ok {
				for _, item := range list {
					if m, ok := item.(map[string]any); ok {
						padWhere(m)
					}
				}
			}
		}
	}
}

func padValue(v any, seqDefault uint64) any {
	switch x := v.(type) {
	case string:
		id, err := Parse(x, seqDefault)

		if err != nil {
			return x
		}

		return id.Key()
	case []any:
		for i := range x {
			x[i] = padValue(x[i], seqDefault)
		}

		return x
	case map[string]any:
		for op, val := range x {
			def := uint64(0)

			if op == "$lte" || op == "$gt" {
				def = Max.Seq
			}

			x[op] = padValue(val, def)
		}

		return x
	}

	return v
}
