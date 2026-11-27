package server

import (
	"strings"
	"testing"
)

func TestGateOf(t *testing.T) {
	cases := []struct {
		method, path string
		local        bool
		want         gateAction
	}{
		{"GET", "/users/_doc/1", true, gateLocal},
		{"GET", "/users/_doc/1", false, gateForward},
		{"POST", "/users/_search", true, gateLocal},
		{"POST", "/users/_count", false, gateForward},
		{"POST", "/users/_doc", true, gateForwardWait},
		{"POST", "/users/_bulk", true, gateForwardWait},
		{"POST", "/users/_update/1", true, gateForwardWait},
		{"DELETE", "/users/_doc/1", true, gateForwardWait},
		{"POST", "/users/_tx", true, gateForwardWait},
		{"POST", "/users/_lock", true, gateForwardWait},
		{"POST", "/users/_update_by_query", true, gateForwardWait},
		{"PUT", "/users", true, gateForwardWait},
		{"DELETE", "/users", true, gateForwardWait},
		{"PUT", "/users/_indexes/a", true, gateForwardWait},
		{"POST", "/users/_truncate", true, gateForwardWait},
		{"POST", "/users/_watch", true, gateForward},
		{"POST", "/users/_ack", true, gateForward},
		{"GET", "/s/_streams", true, gateForward},
		{"POST", "/s/_streams/groups/g/read", true, gateForward},
		{"POST", "/users/_restore", true, gateForward},
		{"POST", "/users/_compact", true, gateLocal},
		{"GET", "/users/_stats", false, gateLocal},
		{"POST", "/_blobs", true, gateForward},
		{"GET", "/_blobs/x", false, gateForward},
		{"GET", "/_collections", true, gateLocal},
		{"POST", "/_restore", true, gateRefuse},
		{"GET", "/_replication", false, gateLocal},
		{"GET", "/_health", false, gateLocal},
	}

	for _, c := range cases {
		segs, _ := segments(c.path)

		if got := gateOf(c.method, segs, c.local); got != c.want {
			t.Errorf("%s %s (local %v) = %d, want %d", c.method, c.path, c.local, got, c.want)
		}
	}
}

func TestReplicateAllows(t *testing.T) {
	for path, want := range map[string]bool{
		"/_replication/stream":   true,
		"/_replication/snapshot": true,
		"/_blobs/abc":            true,
		"/_replication/promote":  false,
		"/_replication":          false,
		"/users/_doc/1":          false,
	} {
		segs, _ := segments(path)

		if got := replicateAllows("GET", segs); got != want {
			t.Errorf("GET %s = %v", path, got)
		}
	}

	segs, _ := segments("/_replication/stream")

	if replicateAllows("POST", segs) {
		t.Error("POST of the stream is allowed")
	}

	if _, need := required("GET", segs); need != accessAdmin || !strings.HasPrefix(segs[0], "_") {
		t.Errorf("a scoped token may read the stream: %v", need)
	}
}
