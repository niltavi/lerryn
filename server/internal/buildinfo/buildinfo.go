// Package buildinfo exposes version information stamped in at build time
// through -ldflags (see the Makefile and Dockerfile).
package buildinfo

// These are overwritten by the linker in release builds.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
