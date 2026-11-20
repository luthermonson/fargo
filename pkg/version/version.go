// Package version holds the build-stamped version string.
package version

// Version is set at build time via -ldflags "-X .../version.Version=...".
var Version = "dev"
