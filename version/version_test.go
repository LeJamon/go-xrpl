package version

import "testing"

func TestVersion(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must never be empty: server_info would report a blank build version")
	}
	// A plain `go test` build passes no -ldflags, so the compiled-in release
	// target applies.
	if Version != "3.3.0" {
		t.Errorf("default Version = %q, want %q", Version, "3.3.0")
	}
}
