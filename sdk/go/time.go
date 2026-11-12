package ndb

import (
	"encoding/json"
	"time"
)

// TimeLayout is how every SDK writes a time: UTC, with milliseconds, always
// the same width ("2026-11-12T15:00:05.000Z").
const TimeLayout = "2006-01-02T15:04:05.000Z"

// Time is a time.Time that goes out as TimeLayout. encoding/json writes a
// time.Time with 0 to 9 digits and the offset of its zone, which is not the
// width of the other SDKs. A time.Time field of the document type and a
// time.Time in a filter (Gte, Lte, Eq, In...) go out as TimeLayout with no
// change. Use Time where the SDK does not see the field: in a nested struct,
// in a map, in the operators of an update.
//
//	type Attempt struct {
//		At ndb.Time `json:"at"`
//	}
//
// It reads any RFC 3339 time.
type Time struct {
	time.Time
}

// NewTime gives t as a Time.
func NewTime(t time.Time) Time {
	return Time{t}
}

// MarshalJSON writes TimeLayout.
func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(formatTime(t.Time))
}

// UnmarshalJSON reads an RFC 3339 time, or null as the zero Time.
func (t *Time) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*t = Time{}

		return nil
	}

	return t.Time.UnmarshalJSON(data)
}

func formatTime(t time.Time) string {
	return t.UTC().Format(TimeLayout)
}

// wireValue writes a time of a filter as TimeLayout; other values stay.
func wireValue(v any) any {
	switch t := v.(type) {
	case time.Time:
		return formatTime(t)
	case *time.Time:
		if t == nil {
			return nil
		}

		return formatTime(*t)
	case Time:
		return formatTime(t.Time)
	case []any:
		out := make([]any, len(t))

		for i, item := range t {
			out[i] = wireValue(item)
		}

		return out
	}

	return v
}
