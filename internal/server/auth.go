package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// authenticate checks the bearer token of every request that did not come in
// on the unix socket. The unix socket is guarded by file permissions instead.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.token) == 0 || isUnix(r) || s.tokenMatches(r) {
			next.ServeHTTP(w, r)

			return
		}

		w.Header().Set("WWW-Authenticate", "Bearer")

		writeError(w, &apiError{status: http.StatusUnauthorized, code: "unauthorized", msg: "missing or wrong bearer token"})
	})
}

func isUnix(r *http.Request) bool {
	addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)

	return addr != nil && addr.Network() == "unix"
}

// tokenMatches compares SHA-256 digests, so the compare takes the same time
// whatever the length of the sent token.
func (s *Server) tokenMatches(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}

	a := sha256.Sum256([]byte(got))
	b := sha256.Sum256(s.token)

	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
