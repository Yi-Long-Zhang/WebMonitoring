// Package version carries the build identity of the running binary.
//
// The values are injected at link time (-X webmonitoring/internal/version.*).
// A binary built without that, which is every `go run` and every `go build` on a
// developer's machine, reports the placeholders below rather than an empty
// string: "dev" is a true statement about such a binary, while "" reads as a
// bug in the endpoint.
package version

import "runtime"

// These are overridden with -ldflags. They are variables rather than constants
// because a constant cannot be set at link time.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Info is what the version endpoint reports.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
	GoVersion string `json:"goVersion"`
}

// Current returns the build identity of this process.
func Current() Info {
	return Info{Version: Version, Commit: Commit, BuildTime: BuildTime, GoVersion: runtime.Version()}
}
