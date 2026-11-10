package ndb

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// modulePath is the import path of this SDK. The build info gives its
// version when an app depends on it.
const modulePath = "github.com/thadeu/ndb/sdk/go"

// WithUserAgent adds the token of the app (for example "myapp/1.2") after
// the User-Agent of the SDK, so the daemon can tell the apps apart.
func WithUserAgent(token string) ConnectOption {
	return func(db *DB) { db.userAgentSuffix = strings.TrimSpace(token) }
}

// sdkVersion is the version of this module in the build of the app, or
// "dev" when the build has no version for it (a test, a replace, a local
// checkout).
func sdkVersion() string {
	info, ok := debug.ReadBuildInfo()

	if !ok {
		return "dev"
	}

	if info.Main.Path == modulePath {
		return moduleVersion(info.Main.Version)
	}

	for _, dep := range info.Deps {
		if dep.Path != modulePath {
			continue
		}

		if dep.Replace != nil {
			return moduleVersion(dep.Replace.Version)
		}

		return moduleVersion(dep.Version)
	}

	return "dev"
}

// moduleVersion turns "v1.17.2" into "1.17.2". An empty or "(devel)"
// version is "dev".
func moduleVersion(v string) string {
	if v == "" || v == "(devel)" {
		return "dev"
	}

	return strings.TrimPrefix(v, "v")
}

// userAgent builds the User-Agent header: the SDK, its version, the Go
// runtime and the platform, then the token of the app, if it has one.
func userAgent(suffix string) string {
	ua := "ndb-go/" + sdkVersion() + " (" + runtime.Version() + "; " + runtime.GOOS + "/" + runtime.GOARCH + ")"

	if suffix != "" {
		ua += " " + suffix
	}

	return ua
}
