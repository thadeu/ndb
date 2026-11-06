package main

import (
	"fmt"
	"runtime"
)

// socketPathMax is the longest unix socket path the OS accepts: sun_path is
// 104 bytes on macOS and the BSDs and 108 on Linux, and holds a final NUL.
func socketPathMax() int {
	if runtime.GOOS == "linux" || runtime.GOOS == "android" {
		return 107
	}

	return 103
}

// checkSocketPath names the length limit, instead of the bare "invalid
// argument" that bind and connect give for a path that is too long.
func checkSocketPath(path string) error {
	limit := socketPathMax()

	if len(path) <= limit {
		return nil
	}

	return fmt.Errorf("unix socket path is %d bytes long, over the %d-byte limit of this OS (macOS 103, Linux 107): use a shorter path: %s", len(path), limit, path)
}
