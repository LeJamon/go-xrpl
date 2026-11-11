package rippled

import (
	"errors"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const repositoryRoot = "../../.."

func TestPinFileRejectsMalformedLines(t *testing.T) {
	for name, data := range map[string]string{
		"quoted":    "ORACLE_TAG=\"1.2.3\"\n",
		"spaced":    "ORACLE_TAG = 1.2.3\n",
		"expansion": "ORACLE_IMAGE=oracle:$ORACLE_TAG\n",
		"no_value":  "ORACLE_TAG=\n",
		"no_key":    "=1.2.3\n",
		"bare_word": "ORACLE_TAG\n",
		"duplicate": "ORACLE_TAG=1.2.3\nORACLE_TAG=1.2.4\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePinFile(data); err == nil {
				t.Fatalf("accepted %q", data)
			}
		})
	}
	values, err := parsePinFile("# comment\n\nORACLE_TAG=1.2.3\n")
	if err != nil || len(values) != 1 || values["ORACLE_TAG"] != "1.2.3" {
		t.Fatalf("values=%v err=%v", values, err)
	}
}

func TestPinIsSelfConsistent(t *testing.T) {
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	digest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for name, value := range map[string]string{
		"ORACLE_COMMIT":        Pin.Commit,
		"ORACLE_PUBLIC_COMMIT": Pin.PublicCommit,
		"ORACLE_PRIOR_COMMIT":  Pin.Prior.Commit,
	} {
		if !commit.MatchString(value) {
			t.Errorf("%s=%q is not a full lowercase commit", name, value)
		}
	}
	for name, value := range map[string]string{
		"ORACLE_PACKAGE_SHA256": Pin.PackageSHA256,
		"ORACLE_BINARY_SHA256":  Pin.BinarySHA256,
	} {
		if !digest.MatchString(value) {
			t.Errorf("%s=%q is not a lowercase SHA-256", name, value)
		}
	}
	// Recorded artefacts may lag the tag while a release bump is in progress.
	for name, value := range map[string]string{
		"ORACLE_CHECKOUT":    Pin.Checkout,
		"ORACLE_IMAGE":       Pin.Image,
		"ORACLE_PACKAGE_URL": Pin.PackageURL,
	} {
		if !strings.Contains(value, Pin.Tag) {
			t.Errorf("%s=%q does not name release %s", name, value, Pin.Tag)
		}
	}
	if !strings.Contains(Pin.Prior.Checkout, Pin.Prior.Tag) {
		t.Errorf("ORACLE_PRIOR_CHECKOUT=%q does not name release %s", Pin.Prior.Checkout, Pin.Prior.Tag)
	}
	if Pin.Prior.Tag == Pin.Tag || Pin.Prior.Commit == Pin.Commit || Pin.Prior.Commit == Pin.PublicCommit {
		t.Error("the prior release must differ from the target")
	}
	for _, path := range []string{Pin.Corpus, Pin.FinalOutcomes, Pin.AmendmentRegistry, PinPath} {
		if _, err := os.Stat(filepath.Join(repositoryRoot, filepath.FromSlash(path))); err != nil {
			t.Errorf("pinned artefact: %v", err)
		}
	}
}

// The Dockerfile cannot read the pin, so its literals are checked against it.
func TestOracleDockerfileMatchesPin(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repositoryRoot, "scripts/peer-interop/Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ADD --checksum=sha256:" + Pin.PackageSHA256 + " " + Pin.PackageURL + " ",
		"'" + Pin.BinarySHA256 + "  /usr/bin/xrpld'",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("scripts/peer-interop/Dockerfile does not contain %q from %s", want, PinPath)
		}
	}
}

// pinnedIdentityAllowed lists the only tracked paths, besides the pin itself
// and prose (docs/, *.md), that may spell out the pinned commits or the names
// of the oracle checkout and recorded corpora. A trailing slash is a prefix.
var pinnedIdentityAllowed = map[string]string{
	PinPath: "the pin",

	// Recorded from the oracle: provenance is part of the recording.
	Pin.Corpus + "/":                                             "recorded corpus",
	Pin.FinalOutcomes + "/":                                      "recorded outcomes",
	Pin.AmendmentRegistry:                                        "recorded amendment registry",
	"scripts/oracle/recorded/":                                   "archived recorder sources",
	"scripts/engine-coverage/inventory.json":                     "generated coverage inventory",
	"internal/rpc/testdata/server_definitions_final_hashes.json": "generated definitions fixture",
	"internal/testing/batch/testdata/wrapper-oracle/":            "recorded batch fixtures and recorder patch",
	"internal/tx/payment/testdata/":                              "recorded amount fixture and its recorder",
	"internal/tx/vault/testdata/":                                "recorded vault fixture and its recorder",

	// Recorder inputs compiled into, and checksummed with, each recording.
	"scripts/oracle/strict-corpus-config.json": "recorder config",
	"scripts/oracle/strict_recorder.cpp":       "recorder source",

	"internal/tx/vault/send_assets_oracle_test.go": "asserts its fixture's recorded commit",
}

// pinnedTagAllowed lists the code files that may still spell out the release
// tag. Go files are only checked inside string literals, so comments that
// describe the release's behaviour are not restricted.
var pinnedTagAllowed = map[string]string{
	PinPath:                                "the pin",
	"scripts/peer-interop/Dockerfile":      "package URL, checked by TestOracleDockerfileMatchesPin",
	".github/workflows/ci.yml":             "job names and the release branch trigger only (see workflowTagLine)",
	"internal/testing/enginefuzz/model.go": "stable name of the frozen fuzz amendment profile",
	"internal/tx/vault/testdata/":          "recorded vault fixture and its recorder",
	"scripts/oracle/recorded/":             "archived recorder sources",
}

var workflowTagLine = regexp.MustCompile(`^\s*(name|branches):`)

func allowed(list map[string]string, path string) bool {
	for entry := range list {
		if path == entry || strings.HasSuffix(entry, "/") && strings.HasPrefix(path, entry) {
			return true
		}
	}
	return false
}

func isProse(path string) bool {
	return strings.HasPrefix(path, "docs/") || strings.HasSuffix(path, ".md")
}

// gitGrep returns "path:line:text" for every tracked text line containing one
// of the fixed strings.
func gitGrep(t *testing.T, patterns []string, pathspecs ...string) []string {
	t.Helper()
	args := []string{"-C", repositoryRoot, "grep", "--full-name", "-n", "-I", "-F"}
	for _, pattern := range patterns {
		args = append(args, "-e", pattern)
	}
	if len(pathspecs) == 0 {
		pathspecs = []string{":/"}
	}
	args = append(append(args, "--"), pathspecs...)
	out, err := exec.CommandContext(t.Context(), "git", args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil
	}
	if err != nil {
		t.Fatalf("git grep: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

// TestPinnedIdentityIsNotDuplicated keeps the pin the only place a release
// bump has to edit: see docs/upgrading-rippled.md.
func TestPinnedIdentityIsNotDuplicated(t *testing.T) {
	identity := []string{
		Pin.Commit, Pin.PublicCommit,
		filepath.Base(Pin.Checkout), filepath.Base(Pin.Corpus), filepath.Base(Pin.FinalOutcomes),
	}
	for _, hit := range gitGrep(t, identity) {
		path, _, _ := strings.Cut(hit, ":")
		if !isProse(path) && !allowed(pinnedIdentityAllowed, path) {
			t.Errorf("%s\n\tread it from %s (rippled.Pin in Go) instead of hard-coding it", hit, PinPath)
		}
	}

	code := []string{":/*.go", ":/*.sh", ":/*.py", ":/*.yml", ":/*.yaml", ":/justfile", ":/*Dockerfile"}
	goLiterals := make(map[string]map[string]bool)
	for _, hit := range gitGrep(t, []string{Pin.Tag}, code...) {
		path, rest, _ := strings.Cut(hit, ":")
		line, text, _ := strings.Cut(rest, ":")
		switch {
		case allowed(pinnedTagAllowed, path):
			if strings.HasPrefix(path, ".github/workflows/") && !workflowTagLine.MatchString(text) {
				t.Errorf("%s\n\tonly job names and branch triggers may name the release; load %s with ./.github/actions/oracle-pin", hit, PinPath)
			}
			continue
		case strings.HasSuffix(path, ".go"):
			if goLiterals[path] == nil {
				goLiterals[path] = stringLiteralLines(t, path, Pin.Tag)
			}
			if !goLiterals[path][line] {
				continue
			}
		}
		t.Errorf("%s\n\tread the release tag from %s (rippled.Pin in Go) instead of hard-coding it", hit, PinPath)
	}
}

// stringLiteralLines reports the lines of a Go file on which a string literal
// contains the substring.
func stringLiteralLines(t *testing.T, path, substring string) map[string]bool {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(repositoryRoot, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	var lexer scanner.Scanner
	lexer.Init(files.AddFile(path, files.Base(), len(source)), source, nil, 0)
	lines := make(map[string]bool)
	for {
		position, kind, literal := lexer.Scan()
		if kind == token.EOF {
			return lines
		}
		if kind != token.STRING || !strings.Contains(literal, substring) {
			continue
		}
		// A raw string may span lines; mark each one that carries the substring.
		first := files.Position(position).Line
		for offset, text := range strings.Split(literal, "\n") {
			if strings.Contains(text, substring) {
				lines[strconv.Itoa(first+offset)] = true
			}
		}
	}
}
