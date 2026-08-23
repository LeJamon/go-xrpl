// Package version holds the node's software and build versions.
package version

// SemanticVersion is the canonical go-xrpl software version advertised in
// protocol messages.
const SemanticVersion = "3.3.0"

// Version is set at build time via:
//
//	go build -ldflags "-X github.com/LeJamon/go-xrpl/version.Version=build-id"
//
// It defaults to SemanticVersion, while development builds may replace it with
// a commit or another human-readable build identifier.
var Version = SemanticVersion
