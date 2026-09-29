package rippled

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOracleImageRequiresPinnedArtifact(t *testing.T) {
	script, err := filepath.Abs("../../../scripts/acceptance/oracle-image.sh")
	if err != nil {
		t.Fatal(err)
	}
	const imageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const mockDocker = `#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  'image inspect '*'{{.Id}}') printf '%s\n' "$MOCK_IMAGE_ID" ;;
  'image inspect '*'{{.Os}}/{{.Architecture}}') printf '%s\n' "$MOCK_PLATFORM" ;;
  *)
    [[ " $* " == *" $MOCK_IMAGE_ID "* ]] || exit 9
    case "$*" in
      *'--entrypoint /usr/bin/sha256sum '*) printf '%s  /usr/bin/xrpld\n' "$MOCK_BINARY_HASH" ;;
      *'--entrypoint /usr/bin/xrpld '*) printf 'xrpld version %s\nGit commit hash: %s\n' "$MOCK_VERSION" "$MOCK_COMMIT" ;;
      *) exit 2 ;;
    esac
    ;;
esac
`
	for _, test := range []struct {
		name, key, value string
	}{
		{"pinned", "", ""},
		{"mutable_image_id", "MOCK_IMAGE_ID", "oracle:3.4.1"},
		{"wrong_architecture", "MOCK_PLATFORM", "linux/arm64"},
		{"changed_binary", "MOCK_BINARY_HASH", strings.Repeat("0", 64)},
		{"wrong_version", "MOCK_VERSION", "3.4.0"},
		{"wrong_commit", "MOCK_COMMIT", priorCommit},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(mockDocker), 0o700); err != nil {
				t.Fatal(err)
			}
			evidence := t.TempDir()
			for _, name := range []string{"oracle-artifact.txt", "oracle-image-id.txt"} {
				if err := os.WriteFile(filepath.Join(evidence, name), []byte("stale evidence\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			env := map[string]string{
				"PATH":             bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"ORACLE_IMAGE":     "oracle:mutable-tag",
				"EVIDENCE_DIR":     evidence,
				"MOCK_IMAGE_ID":    imageID,
				"MOCK_PLATFORM":    "linux/amd64",
				"MOCK_BINARY_HASH": "fb8430dfdbee9d8016818dab22881e48a125598beebf9b5b8c90ca7fdba1faaa",
				"MOCK_VERSION":     "3.4.1",
				"MOCK_COMMIT":      commit,
			}
			if test.key != "" {
				env[test.key] = test.value
			}
			for key, value := range env {
				t.Setenv(key, value)
			}
			cmd := exec.CommandContext(t.Context(), "bash", script)
			output, err := cmd.CombinedOutput()
			if test.key != "" {
				if err == nil {
					t.Fatalf("accepted mismatched oracle artifact: %s", output)
				}
				for _, name := range []string{"oracle-artifact.txt", "oracle-image-id.txt"} {
					if _, err := os.Stat(filepath.Join(evidence, name)); !os.IsNotExist(err) {
						t.Fatalf("failed verification left %s: %v", name, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("pinned oracle rejected: %v\n%s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(evidence, "oracle-image-id.txt"))
			if err != nil || string(data) != imageID+"\n" {
				t.Fatalf("missing immutable test image: %q, %v", data, err)
			}
		})
	}
}
