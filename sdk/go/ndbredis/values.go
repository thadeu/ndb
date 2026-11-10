package ndbredis

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// fields makes the entry of an XADD from XAddArgs.Values: a map, or a list
// of field and value pairs.
func fields(values any) (map[string]any, error) {
	out := map[string]any{}

	switch v := values.(type) {
	case map[string]any:
		for k, val := range v {
			out[k] = val
		}

	case map[string]string:
		for k, val := range v {
			out[k] = val
		}

	case []string:
		if len(v)%2 != 0 {
			return nil, reply(fmt.Sprintf(textArgs, "xadd"))
		}

		for i := 0; i < len(v); i += 2 {
			out[v[i]] = v[i+1]
		}

	case []any:
		if len(v)%2 != 0 {
			return nil, reply(fmt.Sprintf(textArgs, "xadd"))
		}

		for i := 0; i < len(v); i += 2 {
			out[fmt.Sprint(v[i])] = v[i+1]
		}

	default:
		return nil, notSupported(fmt.Sprintf("XADD with Values of type %T (use a map or a list of pairs)", values))
	}

	if len(out) == 0 {
		return nil, reply(fmt.Sprintf(textArgs, "xadd"))
	}

	for k, val := range out {
		enc, err := fieldValue(val)

		if err != nil {
			return nil, fmt.Errorf("ndbredis: field %q: %w", k, err)
		}

		out[k] = enc
	}

	return out, nil
}

// fieldValue gives the JSON value of one field. Text, bytes, a time and a
// BinaryMarshaler are JSON strings, as go-redis sends them. Every other
// value keeps its JSON type: a number, a boolean, an object, an array.
func fieldValue(v any) (any, error) {
	switch v := v.(type) {
	case nil:
		return nil, nil
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case json.RawMessage:
		if !json.Valid(v) {
			return nil, fmt.Errorf("the json.RawMessage is not JSON")
		}

		return v, nil
	case time.Time:
		return v.Format(time.RFC3339Nano), nil
	case time.Duration:
		return v.Nanoseconds(), nil
	case encoding.BinaryMarshaler:
		b, err := v.MarshalBinary()

		return string(b), err
	}

	raw, err := json.Marshal(v)

	return json.RawMessage(raw), err
}

// message makes the XMessage of an entry. Redis keeps text only: a JSON
// string is its text, and every other value is its JSON text ("42",
// "true", `{"a":1}`). null is "".
func message(e ndb.Entry[doc]) redis.XMessage {
	msg := redis.XMessage{ID: e.ID}

	// The entry is pending, but it is no longer in the stream: Redis
	// gives the ID with no fields.
	if e.Deleted {
		return msg
	}

	msg.Values = make(map[string]any, len(e.Doc))

	for k, raw := range e.Doc {
		if k == "_id" || k == "_ts" {
			continue
		}

		msg.Values[k] = text(raw)
	}

	return msg
}

func messages(entries []ndb.Entry[doc]) []redis.XMessage {
	out := make([]redis.XMessage, 0, len(entries))

	for _, e := range entries {
		out = append(out, message(e))
	}

	return out
}

func text(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)

	if len(raw) > 0 && raw[0] == '"' {
		var s string

		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}

	if bytes.Equal(raw, []byte("null")) {
		return ""
	}

	return string(raw)
}
