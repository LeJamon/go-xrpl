// Package version holds the node's build version, injected at link time.
package version

// Version is set at build time via:
//
//	go build -ldflags "-X github.com/LeJamon/go-xrpl/version.Version=x.y.z"
//
// Defaults to "dev" so a build that forgot the ldflags is visible in
// server_info rather than masquerading as a release.
var Version = "dev"
