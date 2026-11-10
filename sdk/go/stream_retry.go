package ndb

import (
	"slices"
	"time"
)

// Tiers gives a Work.Retry with one wait per try: the wait after the n-th
// failed delivery is waits[min(n, len(waits))-1], so the last wait repeats.
// Tiers(15*time.Second, time.Minute) waits 15 s after the first failure,
// then 1 min after each next one. With no waits, every retry is at once.
func Tiers(waits ...time.Duration) func(deliveries int64) time.Duration {
	waits = slices.Clone(waits)

	return func(deliveries int64) time.Duration {
		if len(waits) == 0 {
			return 0
		}

		n := min(max(deliveries, 1), int64(len(waits)))

		return waits[n-1]
	}
}

// Step is one tier of TierSteps.
type Step struct {
	// Wait is the wait before the next try.
	Wait time.Duration

	// UpTo is the last delivery of this tier: the step applies to a
	// failure on delivery n when n <= UpTo, and no step before it applied.
	// 0 is no bound: the step applies to every n that is left.
	UpTo int64
}

// TierSteps gives a Work.Retry from tiers with bounds. For the failure on
// delivery n (1 is the first delivery), the wait is the Wait of the first
// step with n <= UpTo, or with UpTo 0. After the last bound, the Wait of
// the last step repeats. With no steps, every retry is at once.
//
// The schedule of the fsw-esl outbox: try 1 waits 15 s, tries 2 to 5 wait
// 1 min, 6 to 11 wait 10 min, 12 to 23 wait 1 h, then 6 h:
//
//	ndb.TierSteps(
//		ndb.Step{Wait: 15 * time.Second, UpTo: 1},
//		ndb.Step{Wait: time.Minute, UpTo: 5},
//		ndb.Step{Wait: 10 * time.Minute, UpTo: 11},
//		ndb.Step{Wait: time.Hour, UpTo: 23},
//		ndb.Step{Wait: 6 * time.Hour},
//	)
func TierSteps(steps ...Step) func(deliveries int64) time.Duration {
	steps = slices.Clone(steps)

	return func(deliveries int64) time.Duration {
		if len(steps) == 0 {
			return 0
		}

		n := max(deliveries, 1)

		for _, s := range steps {
			if s.UpTo == 0 || n <= s.UpTo {
				return s.Wait
			}
		}

		return steps[len(steps)-1].Wait
	}
}
