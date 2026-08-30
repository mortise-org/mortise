// Package version carries the build identity of this binary. The values are
// stamped at link time (see the Dockerfile and Makefile LDFLAGS); the
// defaults are what an unstamped `go build` produces, and they are
// deliberately recognisable as such so a dev build can never be mistaken for
// a release.
package version

var (
	// Version is the release tag without the leading "v" (e.g. "1.1.0"), or a
	// non-release marker such as "main-5e3011e0aa30" for untagged builds.
	Version = "0.0.0-dev"
	// Commit is the full git SHA the binary was built from.
	Commit = "unknown"
)

// String renders the identity the way the CLI and logs print it.
func String() string {
	return Version + " (" + Commit + ")"
}
