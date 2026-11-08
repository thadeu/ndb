package catalog

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// ParseDuration reads a positive duration for ttl and retention: Go syntax
// ("90m", "1h30m"), plus "d" (24 h) and "w" (7 d) as a single number.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)

	if s == "" {
		return 0, errors.New("empty duration")
	}

	d, err := parse(s)
	if err != nil {
		return 0, err
	}

	if d <= 0 {
		return 0, errors.New("duration must be positive: " + s)
	}

	return d, nil
}

func parse(s string) (time.Duration, error) {
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		num, ok := strings.CutSuffix(s, suffix)
		if !ok {
			continue
		}

		n, err := strconv.ParseInt(num, 10, 64)
		if err != nil {
			return 0, errors.New("bad duration: " + s)
		}

		return time.Duration(n) * unit, nil
	}

	return time.ParseDuration(s)
}

// MaxPITR is the longest PITR window. Older mistakes are a job for backups.
const MaxPITR = 7 * 24 * time.Hour

// ParsePITR reads a PITR window: "off" (0), or a duration up to MaxPITR.
func ParsePITR(s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "off" {
		return 0, nil
	}

	d, err := ParseDuration(s)
	if err != nil {
		return 0, err
	}

	if d > MaxPITR {
		return 0, errors.New("the pitr window is at most 7d: " + s)
	}

	return d, nil
}
