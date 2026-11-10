package server

import (
	"container/list"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// failLimiterCap is the most client IPs that the lockout remembers. Over
// it, the IP seen least recently is forgotten, so the memory is bounded.
const failLimiterCap = 10_000

// failLimiter counts the failed token checks of each client IP. After max
// failures in window, the IP is locked out for lockout: its token checks
// get 429 too_many_requests, so a guess cannot be tested.
type failLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	lockout time.Duration
	cap     int

	// order has the newest entry at the front; byIP finds an entry.
	order *list.List
	byIP  map[string]*list.Element
}

type failEntry struct {
	ip    string
	count int
	first time.Time
	until time.Time
}

func newFailLimiter(max int, window, lockout time.Duration, capacity int) *failLimiter {
	return &failLimiter{max: max, window: window, lockout: lockout, cap: capacity, order: list.New(), byIP: map[string]*list.Element{}}
}

func (l *failLimiter) off() bool {
	return l == nil || l.max <= 0 || l.window <= 0 || l.lockout <= 0
}

// locked is the time left of the lockout of ip, or 0.
func (l *failLimiter) locked(ip string, now time.Time) time.Duration {
	if l.off() {
		return 0
	}

	l.mu.Lock()

	defer l.mu.Unlock()

	el, ok := l.byIP[ip]

	if !ok {
		return 0
	}

	if left := el.Value.(*failEntry).until.Sub(now); left > 0 {
		return left
	}

	return 0
}

// fail counts one failed token check of ip. It tells if this failure
// starts a lockout.
func (l *failLimiter) fail(ip string, now time.Time) bool {
	if l.off() {
		return false
	}

	l.mu.Lock()

	defer l.mu.Unlock()

	e := l.entry(ip)

	if e.count == 0 || now.Sub(e.first) > l.window {
		e.count, e.first = 0, now
	}

	e.count++

	if e.count < l.max {
		return false
	}

	e.count = 0
	e.until = now.Add(l.lockout)

	return true
}

// entry finds or adds the entry of ip and moves it to the front. A new
// entry over the cap forgets the oldest one.
func (l *failLimiter) entry(ip string) *failEntry {
	if el, ok := l.byIP[ip]; ok {
		l.order.MoveToFront(el)

		return el.Value.(*failEntry)
	}

	for l.order.Len() >= l.cap {
		old := l.order.Back()

		l.order.Remove(old)
		delete(l.byIP, old.Value.(*failEntry).ip)
	}

	e := &failEntry{ip: ip}

	l.byIP[ip] = l.order.PushFront(e)

	return e
}

// size is the count of IPs in memory.
func (l *failLimiter) size() int {
	l.mu.Lock()

	defer l.mu.Unlock()

	return len(l.byIP)
}

// tooManyFailures answers 429 to a locked-out IP.
func tooManyFailures(w http.ResponseWriter, ip string, left time.Duration) {
	secs := int((left + time.Second - 1) / time.Second)

	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, &apiError{status: http.StatusTooManyRequests, code: "too_many_requests", msg: "too many failed token checks from " + ip + "; retry in " + strconv.Itoa(secs) + " s"})
}

// logLockout writes the warn line of an IP that is now locked out.
func (s *Server) logLockout(w http.ResponseWriter, r *http.Request, ip string) {
	if !s.log.Enabled(r.Context(), slog.LevelWarn) {
		return
	}

	s.log.LogAttrs(r.Context(), slog.LevelWarn, "token lockout",
		slog.String("request_id", logOf(w).idOrEmpty()),
		slog.String("ip", ip),
		slog.Int("failures", s.sec.AuthFailures),
		slog.String("window", s.sec.AuthWindow.String()),
		slog.String("lockout", s.sec.AuthLockout.String()),
	)
}

// clientIP is the key of the lockout: the TCP peer, or with trust_proxy
// the last X-Forwarded-For hop (the one the proxy added). An IPv6 address
// counts by its /64, as one host often has the whole /64.
func (s *Server) clientIP(r *http.Request) string {
	raw := r.RemoteAddr

	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}

	if s.sec.TrustProxy {
		if hop := lastHop(r.Header.Values("X-Forwarded-For")); hop != "" {
			raw = hop
		}
	}

	ip := net.ParseIP(raw)

	if ip == nil {
		return raw
	}

	if ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}

	return ip.String()
}

// lastHop is the last address of the X-Forwarded-For headers.
func lastHop(values []string) string {
	if len(values) == 0 {
		return ""
	}

	hops := strings.Split(values[len(values)-1], ",")

	return strings.TrimSpace(hops[len(hops)-1])
}

// watchStarted counts one more open _watch stream. Over max_watches it
// counts nothing and tells false.
func (s *Server) watchStarted() bool {
	limit := int64(s.sec.MaxWatches)

	for {
		n := s.watches.Load()

		if limit > 0 && n >= limit {
			return false
		}

		if s.watches.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func (s *Server) watchEnded() {
	s.watches.Add(-1)
}

// tooManyWatches answers 429 before a _watch stream starts.
func (s *Server) tooManyWatches(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	writeError(w, &apiError{status: http.StatusTooManyRequests, code: "too_many_requests", msg: "max_watches (" + strconv.Itoa(s.sec.MaxWatches) + ") _watch streams are open; retry later or raise max_watches"})
}
