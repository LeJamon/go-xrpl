package version

import "testing"

func TestVersion(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must never be empty: server_info would report a blank build version")
	}
	// A plain `go test` build passes no -ldflags, so the compiled-in default
	// applies. Pinning it guards against the default literal being changed or
	// blanked, which would let a build masquerade as a release.
	if Version != "dev" {
		t.Errorf("default Version = %q, want %q", Version, "dev")
	}
}
