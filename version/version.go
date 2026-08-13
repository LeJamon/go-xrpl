// Package version holds the node's build version, injected at link time.
package version

// Version is set at build time via:
//
//	go build -ldflags "-X github.com/LeJamon/go-xrpl/version.Version=x.y.z"
//
// The default identifies the protocol release implemented by this branch;
// release builds may replace it with a more specific build identifier.
var Version = "3.3.0"
