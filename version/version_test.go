package version

import "testing"

func TestVersion(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must never be empty: server_info would report a blank build version")
	}
	if SemanticVersion != "3.3.0" {
		t.Errorf("SemanticVersion = %q, want %q", SemanticVersion, "3.3.0")
	}
	if Version != SemanticVersion {
		t.Errorf("default Version = %q, want SemanticVersion %q", Version, SemanticVersion)
	}
}
