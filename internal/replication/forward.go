package replication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// ReadsLocal tells if the copy answers reads itself: it caught up once and
// no resync runs. Until then reads go to the owner too, so an app never
// reads a half copy.
func (c *Copy) ReadsLocal() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.st.Synced && c.state != StateResync
}

type waitKey struct{}

// Forward sends the request to the owner and copies its answer. With
// wait (a write), the copy answers after it has applied the position the
// owner gave (Ndb-Position), or after Options.Wait with Ndb-Stale: true.
// An owner that does not answer is 503 owner_unavailable.
func (c *Copy) Forward(w http.ResponseWriter, r *http.Request, wait bool) {
	r = r.WithContext(context.WithValue(r.Context(), waitKey{}, wait))

	c.proxy().ServeHTTP(w, r)
}

func (c *Copy) proxy() *httputil.ReverseProxy {
	target, _ := url.Parse(c.src.Base)

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host

			c.auth(pr.Out.Header, true)
			pr.Out.Header.Set(ForwardedHeader, "1")
		},
		Transport:      c.opts.Client.Transport,
		FlushInterval:  -1,
		ModifyResponse: c.afterForward,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return
			}

			c.log.Warn("replication: forward to the owner failed", "path", r.URL.Path, "err", err)

			OwnerUnavailable(w, "the owner of this database does not answer: "+err.Error())
		},
	}
}

// afterForward waits for the position of a forwarded write.
func (c *Copy) afterForward(resp *http.Response) error {
	wait, _ := resp.Request.Context().Value(waitKey{}).(bool)

	if !wait || resp.StatusCode >= 300 {
		return nil
	}

	want, err := ParsePositions(resp.Header.Get(PositionHeader))
	if err != nil || len(want) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(resp.Request.Context(), c.opts.Wait)
	defer cancel()

	if !c.WaitFor(ctx, want) {
		resp.Header.Set(StaleHeader, "true")
	}

	return nil
}

// OwnerUnavailable answers 503 owner_unavailable.
func OwnerUnavailable(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)

	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "owner_unavailable", "message": msg}})
}
