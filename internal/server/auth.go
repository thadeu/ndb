package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// authenticate checks the bearer token of every request that did not come in
// on the unix socket (guarded by file permissions instead), and that the
// token may do what the request asks (scope.go).
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.tokens) == 0 || isUnix(r) {
			next.ServeHTTP(w, r)

			return
		}

		t, ok := s.tokenOf(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")

			writeError(w, &apiError{status: http.StatusUnauthorized, code: "unauthorized", msg: "missing or wrong bearer token"})

			return
		}

		noteToken(w, t.Name)

		segs, _ := segments(r.URL.EscapedPath())
		coll, need := required(r.Method, segs)

		if !t.allows(coll, need) {
			writeError(w, &apiError{status: http.StatusForbidden, code: "forbidden", msg: forbidden(t, coll, need)})

			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, t)))
	})
}

type tokenKey struct{}

// mayAlso tells if the request may also do need on coll: a route that
// names a second collection in its body (a rename target) checks it here.
func (s *Server) mayAlso(r *http.Request, coll string, need access) error {
	t, ok := r.Context().Value(tokenKey{}).(Token)
	if !ok || t.allows(coll, need) {
		return nil
	}

	return &apiError{status: http.StatusForbidden, code: "forbidden", msg: forbidden(t, coll, need)}
}

func forbidden(t Token, coll string, need access) string {
	if coll == "" || coll == "*" {
		return fmt.Sprintf("token %s may not %s the daemon", t.Name, need)
	}

	return fmt.Sprintf("token %s may not %s %s", t.Name, need, coll)
}

func isUnix(r *http.Request) bool {
	addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)

	return addr != nil && addr.Network() == "unix"
}

// tokenOf finds the token of the request. It compares SHA-256 digests with
// every token, so the time does not depend on the sent token or on which one
// matches.
func (s *Server) tokenOf(r *http.Request) (Token, bool) {
	got, ok := bearerToken(r.Header.Get("Authorization"))

	if !ok {
		return Token{}, false
	}

	sent := sha256.Sum256([]byte(got))

	var (
		found Token
		match int
	)

	for _, t := range s.tokens {
		want := sha256.Sum256([]byte(t.Secret))

		if subtle.ConstantTimeCompare(sent[:], want[:]) == 1 {
			found, match = t, 1
		}
	}

	return found, match == 1
}

// bearerToken reads the token of an "Authorization: Bearer <token>" header.
// The scheme is case-insensitive (RFC 9110 11.1).
func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")

	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}

	return token, true
}
