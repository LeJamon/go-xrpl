package rippled

import (
	_ "embed"
	"fmt"
	"strconv"
	"strings"
)

//go:embed pin.env
var pinFile string

// PinPath is the repository-relative location of the pin file.
const PinPath = "internal/testutil/rippled/pin.env"

// Release identifies one rippled release and where its checkout lives.
type Release struct {
	Tag        string
	Repository string
	Commit     string
	// Checkout is relative to the repository root or any of its parents.
	Checkout string
}

// Target is the rippled release go-xrpl targets plus the artefacts recorded
// from it. Paths are relative to the repository root.
type Target struct {
	Release
	PublicRepository string
	PublicCommit     string

	Image         string
	PackageURL    string
	PackageSHA256 string
	BinarySHA256  string

	Corpus            string
	FinalOutcomes     string
	AmendmentRegistry string
	DefinitionsHash   string
	DefinitionsBytes  int

	// Prior is the previous release, kept only for historical fixtures.
	Prior Release
}

// Pin is the single source of the targeted rippled release, loaded from pin.env.
var Pin = mustParsePin(pinFile)

// VersionLine is the first line `xrpld --version` prints for the release.
func (r Release) VersionLine() string { return "xrpld version " + r.Tag }

func parsePinFile(data string) (map[string]string, error) {
	values := make(map[string]string)
	for number, line := range strings.Split(data, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || value == "" || strings.ContainsAny(line, " \t\r\"'$\\`") {
			return nil, fmt.Errorf("%s:%d: want KEY=value without quotes, spaces or expansions", PinPath, number+1)
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("%s:%d: duplicate key %s", PinPath, number+1, key)
		}
		values[key] = value
	}
	return values, nil
}

func mustParsePin(data string) Target {
	values, err := parsePinFile(data)
	if err != nil {
		panic(err)
	}
	get := func(key string) string {
		value, ok := values[key]
		if !ok {
			panic(fmt.Sprintf("%s: missing %s", PinPath, key))
		}
		delete(values, key)
		return value
	}
	definitionsBytes, err := strconv.Atoi(get("ORACLE_DEFINITIONS_BYTES"))
	if err != nil {
		panic(fmt.Sprintf("%s: ORACLE_DEFINITIONS_BYTES: %v", PinPath, err))
	}
	target := Target{
		Release: Release{
			Tag:        get("ORACLE_TAG"),
			Repository: get("ORACLE_REPOSITORY"),
			Commit:     get("ORACLE_COMMIT"),
			Checkout:   get("ORACLE_CHECKOUT"),
		},
		PublicRepository:  get("ORACLE_PUBLIC_REPOSITORY"),
		PublicCommit:      get("ORACLE_PUBLIC_COMMIT"),
		Image:             get("ORACLE_IMAGE"),
		PackageURL:        get("ORACLE_PACKAGE_URL"),
		PackageSHA256:     get("ORACLE_PACKAGE_SHA256"),
		BinarySHA256:      get("ORACLE_BINARY_SHA256"),
		Corpus:            get("ORACLE_CORPUS"),
		FinalOutcomes:     get("ORACLE_FINAL_OUTCOMES"),
		AmendmentRegistry: get("ORACLE_AMENDMENT_REGISTRY"),
		DefinitionsHash:   get("ORACLE_DEFINITIONS_SHA512_HALF"),
		DefinitionsBytes:  definitionsBytes,
		Prior: Release{
			Tag:        get("ORACLE_PRIOR_TAG"),
			Repository: get("ORACLE_PRIOR_REPOSITORY"),
			Commit:     get("ORACLE_PRIOR_COMMIT"),
			Checkout:   get("ORACLE_PRIOR_CHECKOUT"),
		},
	}
	for key := range values {
		panic(fmt.Sprintf("%s: unknown key %s", PinPath, key))
	}
	return target
}
