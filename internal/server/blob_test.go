package server_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/server"
)

func do(t *testing.T, c *http.Client, method, path, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()

	req, err := http.NewRequest(method, "http://ndb"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return resp, string(data)
}

func TestBlobPutGetRange(t *testing.T) {
	c := unixServer(t)

	resp, body := do(t, c, "PUT", "/v1/blobs", "0123456789", map[string]string{"Content-Type": "audio/wav"})

	if resp.StatusCode != 200 || !strings.Contains(body, `"$blob":"sha256:`) || !strings.Contains(body, `"size":10`) || !strings.Contains(body, `"type":"audio/wav"`) {
		t.Fatalf("put: %d %s", resp.StatusCode, body)
	}

	ref := between(body, `"$blob":"`, `"`)

	resp, got := do(t, c, "GET", "/v1/blobs/"+ref, "", nil)

	if resp.StatusCode != 200 || got != "0123456789" || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("get: %d %q %v", resp.StatusCode, got, resp.Header)
	}

	resp, got = do(t, c, "GET", "/v1/blobs/"+ref+"?type=audio/wav", "", map[string]string{"Range": "bytes=2-5"})

	if resp.StatusCode != 206 || got != "2345" || resp.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("range: %d %q %v", resp.StatusCode, got, resp.Header)
	}

	resp, _ = do(t, c, "GET", "/v1/blobs/"+ref, "", map[string]string{"If-None-Match": resp.Header.Get("Etag")})

	if resp.StatusCode != 304 {
		t.Fatalf("etag: %d", resp.StatusCode)
	}
}

func between(s, a, b string) string {
	_, rest, _ := strings.Cut(s, a)
	out, _, _ := strings.Cut(rest, b)

	return out
}

func TestBlobErrors(t *testing.T) {
	c := unixServerWith(t, func(s *server.Server) { s.SetBlobLimit(8) })

	resp, body := do(t, c, "PUT", "/v1/blobs", "0123456789", nil)

	if resp.StatusCode != 413 || !strings.Contains(body, "request_too_large") {
		t.Fatalf("too large: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "GET", "/v1/blobs/sha256:nothex", "", nil)

	if resp.StatusCode != 400 || !strings.Contains(body, "invalid_blob_ref") {
		t.Fatalf("bad ref: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "GET", "/v1/blobs/sha256:"+strings.Repeat("a", 64), "", nil)

	if resp.StatusCode != 404 {
		t.Fatalf("missing: %d %s", resp.StatusCode, body)
	}
}

func TestMetricsPage(t *testing.T) {
	c := unixServer(t)

	do(t, c, "PUT", "/v1/c/m", `{"id":"k","indexes":["a"]}`, nil)
	do(t, c, "POST", "/v1/c/m/insert", `{"insert":[{"k":"1","a":1}]}`, nil)
	do(t, c, "POST", "/v1/c/m/find", `{}`, nil)
	do(t, c, "POST", "/v1/c/m/aggregate", `{"pipeline":[{"$count":"n"}]}`, nil)
	do(t, c, "PUT", "/v1/blobs", "abc", nil)

	_, page := do(t, c, "GET", "/metrics", "", nil)

	for _, want := range []string{
		`ndb_wal_records_total{collection="m"} 1`,
		`ndb_commit_seconds_count{collection="m"} 1`,
		`ndb_fsync_seconds_count{collection="m"} 1`,
		`ndb_query_seconds_count{type="find"} 1`,
		`ndb_query_seconds_count{type="aggregate"} 1`,
		`ndb_memtable_bytes{collection="m"} `,
		`ndb_segments{collection="m"} 0`,
		`ndb_tailer_lag_bytes{collection="m"} 0`,
		`ndb_quarantined_total{collection="m"} 0`,
		`ndb_index_build_progress{collection="m",field="a"} 1`,
		`ndb_watch_subscribers{collection="m"} 0`,
		"ndb_blob_store_bytes 3\n",
		"ndb_blob_store_blobs 1\n",
		"ndb_requests_total ",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("metrics page misses %q", want)
		}
	}
}
