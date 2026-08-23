package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// sfServerVersion uses 16 implementation bits followed by implementation-
// defined data. go-xrpl uses rippled's lower-bit SemVer layout under its own ID.
const implementationID uint16 = 0x4000

var (
	serverVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	encodedServerVersion = mustEncodeServerVersion(SemanticVersion)
)

// EncodedServerVersion returns the canonical 64-bit sfServerVersion value for
// this go-xrpl release.
func EncodedServerVersion() uint64 {
	return encodedServerVersion
}

func mustEncodeServerVersion(value string) uint64 {
	encoded, err := encodeServerVersion(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func encodeServerVersion(value string) (uint64, error) {
	parts := serverVersionPattern.FindStringSubmatch(value)
	if parts == nil {
		return 0, fmt.Errorf("invalid go-xrpl semantic version %q", value)
	}

	major, err := strconv.ParseUint(parts[1], 10, 8)
	if err != nil {
		return 0, fmt.Errorf("invalid go-xrpl major version %q: %w", parts[1], err)
	}
	minor, err := strconv.ParseUint(parts[2], 10, 8)
	if err != nil {
		return 0, fmt.Errorf("invalid go-xrpl minor version %q: %w", parts[2], err)
	}
	patch, err := strconv.ParseUint(parts[3], 10, 8)
	if err != nil {
		return 0, fmt.Errorf("invalid go-xrpl patch version %q: %w", parts[3], err)
	}

	release, err := encodePrerelease(parts[4])
	if err != nil {
		return 0, err
	}

	return uint64(implementationID)<<48 |
		major<<40 |
		minor<<32 |
		patch<<24 |
		release<<16, nil
}

func encodePrerelease(value string) (uint64, error) {
	if value == "" {
		return 0xC0, nil
	}

	for _, identifier := range strings.Split(value, ".") {
		if identifier[0] == '0' {
			return 0, fmt.Errorf("invalid prerelease identifier %q", identifier)
		}

		for _, candidate := range []struct {
			prefix string
			kind   uint64
		}{
			{prefix: "rc", kind: 0x80},
			{prefix: "b", kind: 0x40},
		} {
			if !strings.HasPrefix(identifier, candidate.prefix) {
				continue
			}
			ordinal, err := strconv.ParseUint(strings.TrimPrefix(identifier, candidate.prefix), 10, 6)
			if err == nil {
				return candidate.kind | ordinal, nil
			}
		}
	}

	return 0, nil
}
