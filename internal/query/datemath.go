package query

import (
	"strconv"
	"strings"
	"time"
)

// Date math on the time field: "now", then any number of +N<unit> or
// -N<unit>, then an optional /<unit> that rounds down, in UTC.
//
//	now-7d/d   the start of the day, 7 days ago
//	now-1h     one hour ago
//	now/M      the start of this month
//
// Units: s (second), m (minute), h (hour), d (day), w (week, starting
// Monday), M (month), y (year).

// isDateMath tells if a string operand on the time field is date math.
func isDateMath(s string) bool {
	return strings.HasPrefix(s, "now")
}

// resolveDateMath turns a date math expression into an instant, from now.
func resolveDateMath(expr string, now time.Time) (time.Time, error) {
	t := now.UTC()
	rest := strings.TrimPrefix(expr, "now")

	for rest != "" && rest[0] != '/' {
		sign := 1

		switch rest[0] {
		case '+':
		case '-':
			sign = -1
		default:
			return time.Time{}, badDateMath(expr)
		}

		digits := 1

		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}

		n, err := strconv.Atoi(rest[1:digits])
		if err != nil || digits == len(rest) {
			return time.Time{}, badDateMath(expr)
		}

		if t, err = addUnit(t, sign*n, rest[digits]); err != nil {
			return time.Time{}, badDateMath(expr)
		}

		rest = rest[digits+1:]
	}

	if rest == "" {
		return t, nil
	}

	if len(rest) != 2 {
		return time.Time{}, badDateMath(expr)
	}

	t, err := roundDown(t, rest[1])
	if err != nil {
		return time.Time{}, badDateMath(expr)
	}

	return t, nil
}

func addUnit(t time.Time, n int, unit byte) (time.Time, error) {
	switch unit {
	case 's':
		return t.Add(time.Duration(n) * time.Second), nil
	case 'm':
		return t.Add(time.Duration(n) * time.Minute), nil
	case 'h':
		return t.Add(time.Duration(n) * time.Hour), nil
	case 'd':
		return t.AddDate(0, 0, n), nil
	case 'w':
		return t.AddDate(0, 0, 7*n), nil
	case 'M':
		return t.AddDate(0, n, 0), nil
	case 'y':
		return t.AddDate(n, 0, 0), nil
	}

	return time.Time{}, errBadUnit
}

func roundDown(t time.Time, unit byte) (time.Time, error) {
	y, mo, d := t.Date()

	switch unit {
	case 's':
		return t.Truncate(time.Second), nil
	case 'm':
		return t.Truncate(time.Minute), nil
	case 'h':
		return t.Truncate(time.Hour), nil
	case 'd':
		return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC), nil
	case 'w':
		back := (int(t.Weekday()) + 6) % 7

		return time.Date(y, mo, d-back, 0, 0, 0, 0, time.UTC), nil
	case 'M':
		return time.Date(y, mo, 1, 0, 0, 0, 0, time.UTC), nil
	case 'y':
		return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC), nil
	}

	return time.Time{}, errBadUnit
}

var errBadUnit = NewError("invalid_query", "bad date math unit")

func badDateMath(expr string) error {
	return invalid("bad date math %q: use now, then +N or -N with a unit (s m h d w M y), then an optional /unit", expr)
}

// timeValue is a time Value; Raw is its RFC 3339 form.
func timeValue(t time.Time) Value {
	return Value{Kind: Time, Int: t.UnixNano(), Raw: t.UTC().Format(time.RFC3339Nano)}
}
