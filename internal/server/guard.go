package server

import (
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The browser guards. ndb is for servers, but a user install listens on
// 127.0.0.1:7070 with no token, and every web page in a browser on the same
// machine can send requests there. Two attacks follow:
//
//   - CSRF: a page posts a "simple" request (text/plain, no preflight) to
//     127.0.0.1:7070. The browser sends it with an Origin header (or
//     Sec-Fetch-Site: cross-site). ndb refuses every request with an Origin
//     that is not in cors_origins.
//   - DNS rebinding: a page at evil.example points its name to 127.0.0.1 and
//     then reads the answers. The browser sends "Host: evil.example". Without
//     a token, ndb on TCP answers only a Host that is an IP, localhost, a name
//     with no dot (a Docker service) or a name in allowed_hosts.

// Security holds the browser guards and the limits of the server.
type Security struct {
	// CORSOrigins are the browser origins (scheme://host[:port]) that may
	// call the daemon. Empty: every request with an Origin is refused.
	CORSOrigins []string

	// AllowedHosts are the Host names that a TCP request with no token may
	// use, more than an IP, localhost and a name with no dot.
	AllowedHosts []string

	// TrustProxy takes the client IP from X-Forwarded-For (its last hop,
	// set by the proxy), not from the TCP peer.
	TrustProxy bool

	// AuthFailures failed token checks of one IP in AuthWindow lock that IP
	// out for AuthLockout. 0 turns the lockout off.
	AuthFailures int
	AuthWindow   time.Duration
	AuthLockout  time.Duration

	// MaxWatches is the most _watch streams open at the same time (0 = no
	// limit).
	MaxWatches int
}

// The lockout defaults.
const (
	DefaultAuthFailures = 10
	DefaultAuthWindow   = time.Minute
	DefaultAuthLockout  = 5 * time.Minute
)

// DefaultSecurity has no CORS origin, no extra host and the default lockout.
func DefaultSecurity() Security {
	return Security{AuthFailures: DefaultAuthFailures, AuthWindow: DefaultAuthWindow, AuthLockout: DefaultAuthLockout}
}

// SetSecurity sets the browser guards and the limits. Call it before Serve.
func (s *Server) SetSecurity(sec Security) {
	sec.CORSOrigins = lowerAll(sec.CORSOrigins, "/")
	sec.AllowedHosts = lowerAll(sec.AllowedHosts, "")

	s.sec = sec
	s.fails = newFailLimiter(sec.AuthFailures, sec.AuthWindow, sec.AuthLockout, failLimiterCap)
}

func lowerAll(list []string, cut string) []string {
	out := make([]string, 0, len(list))

	for _, x := range list {
		x = strings.ToLower(strings.TrimSpace(x))

		if cut != "" {
			x = strings.TrimSuffix(x, cut)
		}

		if x != "" {
			out = append(out, x)
		}
	}

	return out
}

// corsMethods and corsHeaders are what a preflight allows.
const (
	corsMethods = "GET, HEAD, POST, PUT, DELETE"
	corsHeaders = "Authorization, Content-Type, X-Request-Id, Range"
	corsExpose  = "X-Request-Id, Retry-After, Content-Range, Etag, Allow, X-Ndb-Total, X-Ndb-Examined, X-Ndb-Plan, X-Ndb-Next, X-Ndb-Took-Ms, X-Ndb-Explain"
	corsMaxAge  = "600"
)

// guard runs the browser guards before the token check: a preflight
// carries no token.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if err := s.checkOrigin(r, origin); err != nil {
			writeError(w, err)

			return
		}

		if err := s.checkHost(r); err != nil {
			writeError(w, err)

			return
		}

		if origin == "" {
			next.ServeHTTP(w, r)

			return
		}

		h := w.Header()

		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")

		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", corsMethods)
			h.Set("Access-Control-Allow-Headers", corsHeaders)
			h.Set("Access-Control-Max-Age", corsMaxAge)
			w.WriteHeader(http.StatusNoContent)

			return
		}

		h.Set("Access-Control-Expose-Headers", corsExpose)

		next.ServeHTTP(w, r)
	})
}

// checkOrigin refuses a browser request from an origin that is not in
// cors_origins. A request with no Origin and no cross-site Sec-Fetch-Site
// (curl, an SDK, the console server) passes.
func (s *Server) checkOrigin(r *http.Request, origin string) error {
	if origin != "" {
		if slices.Contains(s.sec.CORSOrigins, strings.ToLower(strings.TrimSuffix(origin, "/"))) {
			return nil
		}

		return &apiError{status: http.StatusForbidden, code: "forbidden", msg: fmt.Sprintf("a browser request from origin %q is refused; add the origin to cors_origins in ndb.yml to allow it", cut(origin, 200))}
	}

	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "cross-site", "same-site":
		return &apiError{status: http.StatusForbidden, code: "forbidden", msg: fmt.Sprintf("a browser request with Sec-Fetch-Site: %s is refused; give it an origin in cors_origins of ndb.yml", site)}
	}

	return nil
}

// checkHost is the DNS rebinding guard of a TCP request with no token.
// With a token, a rebound page cannot send it, so the Host does not matter.
func (s *Server) checkHost(r *http.Request) error {
	if len(s.tokens) > 0 || !isTCP(r) {
		return nil
	}

	host := hostOnly(r.Host)

	if s.hostAllowed(host) {
		return nil
	}

	return &apiError{status: http.StatusForbidden, code: "forbidden", msg: fmt.Sprintf("host %q is refused: without a token, ndb answers on TCP only an IP, localhost, a name with no dot, or a name in allowed_hosts of ndb.yml (DNS rebinding guard)", cut(host, 200))}
}

func (s *Server) hostAllowed(host string) bool {
	switch {
	case host == "", host == "localhost":
		return true
	case net.ParseIP(host) != nil:
		return true
	case !strings.Contains(host, "."):
		return true
	}

	return slices.Contains(s.sec.AllowedHosts, host)
}

// hostOnly is the Host header without the port and the IPv6 brackets, in
// lower case.
func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}

	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")

	return strings.ToLower(h)
}

func isTCP(r *http.Request) bool {
	addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)

	return addr != nil && strings.HasPrefix(addr.Network(), "tcp")
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}
