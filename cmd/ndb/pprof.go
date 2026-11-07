//go:build pprof

package main

// A build with -tags pprof serves the Go profiles on 127.0.0.1:6061, for
// memory and CPU work on the daemon (go tool pprof http://127.0.0.1:6061/
// debug/pprof/heap). Release builds do not have it.

import (
	"net/http"
	_ "net/http/pprof"
)

func init() {
	go http.ListenAndServe("127.0.0.1:6061", nil)
}
