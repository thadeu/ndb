//go:build pprof

package main

// A build with -tags pprof serves the Go profiles on 127.0.0.1:6061, for
// memory and CPU work on the daemon (go tool pprof http://127.0.0.1:6061/
// debug/pprof/heap). It also samples the mutex and block profiles, for the
// waits of concurrent reads (debug/pprof/mutex, debug/pprof/block).
// Release builds do not have it.

import (
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"time"
)

func init() {
	runtime.SetMutexProfileFraction(10)
	runtime.SetBlockProfileRate(int((100 * time.Microsecond).Nanoseconds()))

	go http.ListenAndServe("127.0.0.1:6061", nil)
}
