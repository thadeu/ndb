package server

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// The lockout remembers at most its cap of IPs: the one seen least
// recently is forgotten first.
func TestFailLimiterIsBounded(t *testing.T) {
	l := newFailLimiter(2, time.Minute, time.Minute, 100)
	now := time.Now()

	l.fail("locked", now)
	l.fail("locked", now)

	for i := range 1000 {
		l.fail("10.0."+strconv.Itoa(i/256)+"."+strconv.Itoa(i%256), now)

		if i%10 == 0 {
			l.fail("locked", now)
		}
	}

	if n := l.size(); n != 100 {
		t.Fatalf("size = %d, want the cap 100", n)
	}

	if l.locked("locked", now) == 0 {
		t.Fatal("an IP in use was forgotten")
	}

	if _, ok := l.byIP["10.0.0.0"]; ok {
		t.Fatal("the oldest IP is still in memory")
	}
}

func TestFailLimiterWindowAndLockout(t *testing.T) {
	l := newFailLimiter(3, time.Minute, 5*time.Minute, 10)
	now := time.Now()

	l.fail("a", now)
	l.fail("a", now)

	// The window ends: the count starts again.
	if l.fail("a", now.Add(2*time.Minute)) {
		t.Fatal("a failure after the window locked out")
	}

	l.fail("a", now.Add(2*time.Minute))

	if !l.fail("a", now.Add(2*time.Minute)) {
		t.Fatal("the third failure in the window did not lock out")
	}

	if left := l.locked("a", now.Add(3*time.Minute)); left != 4*time.Minute {
		t.Fatalf("left = %v, want 4m", left)
	}

	if l.locked("a", now.Add(8*time.Minute)) != 0 {
		t.Fatal("the lockout did not end")
	}

	if newFailLimiter(0, time.Minute, time.Minute, 10).fail("a", now) {
		t.Fatal("auth_failures 0 locked out")
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		remote, xff string
		trust       bool
		want        string
	}{
		{"192.0.2.1:5000", "", false, "192.0.2.1"},
		{"192.0.2.1:5000", "203.0.113.9", false, "192.0.2.1"},
		{"192.0.2.1:5000", "203.0.113.9", true, "203.0.113.9"},
		{"192.0.2.1:5000", "6.6.6.6, 203.0.113.9", true, "203.0.113.9"},
		{"192.0.2.1:5000", "", true, "192.0.2.1"},
		{"[2001:db8:1:2:3:4:5:6]:5000", "", false, "2001:db8:1:2::/64"},
	}

	for _, c := range cases {
		s := &Server{sec: Security{TrustProxy: c.trust}}
		r := &http.Request{RemoteAddr: c.remote, Header: http.Header{}}

		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}

		if got := s.clientIP(r); got != c.want {
			t.Errorf("%s xff=%q trust=%v: %q, want %q", c.remote, c.xff, c.trust, got, c.want)
		}
	}
}
