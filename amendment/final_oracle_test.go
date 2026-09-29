package amendment

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
)

const (
	privateV341OracleRepository = "XRPLF/xrpld-private"
	privateV341OracleTag        = "3.4.1"
	privateV341OracleCommit     = "d147fccf54a500fce586522f28d6044c37fd8d29"
	privateV341FeatureSource    = "include/xrpl/protocol/detail/features.macro"
	privateV341FeatureSHA256    = "1708df2ea0150a3105f13f7f39c42c798ac5addae9833c073fe6df9e9e17208b"
)

//go:embed testdata/private3.4.1_registry.json
var privateV341RegistryFixture []byte

type privateV341Fixture struct {
	Oracle        privateV341Oracle         `json:"oracle"`
	Registrations []privateV341Registration `json:"registrations"`
}

type privateV341Oracle struct {
	Repository   string `json:"repository"`
	Tag          string `json:"tag"`
	Commit       string `json:"commit"`
	Source       string `json:"source"`
	SHA256       string `json:"sha256"`
	EntryCount   int    `json:"entry_count"`
	ActiveCount  int    `json:"active_count"`
	RetiredCount int    `json:"retired_count"`
}

type privateV341Registration struct {
	Name       string `json:"name"`
	SourceName string `json:"source_name"`
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Supported  string `json:"supported"`
	Vote       string `json:"vote"`
	Retired    bool   `json:"retired"`
}

var privateV341GoOnlyFeatures = []struct {
	name      string
	supported Supported
	vote      VoteBehavior
	retired   bool
}{
	{name: "InvariantsV1_1", supported: SupportedNo, vote: VoteDefaultNo},
	{name: "NonFungibleTokensV1", supported: SupportedYes, vote: VoteObsolete},
	{name: "fixNFTokenDirV1", supported: SupportedYes, vote: VoteObsolete},
	{name: "fixNFTokenNegOffer", supported: SupportedYes, vote: VoteObsolete},
}

func loadPrivateV341Fixture(t *testing.T) privateV341Fixture {
	t.Helper()
	var fixture privateV341Fixture
	if err := json.Unmarshal(privateV341RegistryFixture, &fixture); err != nil {
		t.Fatalf("decode private v3.4.1 amendment fixture: %v", err)
	}
	if fixture.Oracle.Repository != privateV341OracleRepository {
		t.Fatalf("fixture repository = %q, want %q", fixture.Oracle.Repository, privateV341OracleRepository)
	}
	if fixture.Oracle.Tag != privateV341OracleTag {
		t.Fatalf("fixture tag = %q, want %q", fixture.Oracle.Tag, privateV341OracleTag)
	}
	if fixture.Oracle.Commit != privateV341OracleCommit {
		t.Fatalf("fixture commit = %q, want %q", fixture.Oracle.Commit, privateV341OracleCommit)
	}
	if fixture.Oracle.Source != privateV341FeatureSource {
		t.Fatalf("fixture source = %q, want %q", fixture.Oracle.Source, privateV341FeatureSource)
	}
	if fixture.Oracle.SHA256 != privateV341FeatureSHA256 {
		t.Fatalf("features.macro SHA-256 = %q, want %q", fixture.Oracle.SHA256, privateV341FeatureSHA256)
	}
	if fixture.Oracle.EntryCount != len(fixture.Registrations) {
		t.Fatalf("fixture entry_count = %d, decoded %d registrations", fixture.Oracle.EntryCount, len(fixture.Registrations))
	}
	activeCount := 0
	retiredCount := 0
	for _, registration := range fixture.Registrations {
		if registration.Retired {
			retiredCount++
		} else {
			activeCount++
		}
	}
	if fixture.Oracle.ActiveCount != activeCount || fixture.Oracle.RetiredCount != retiredCount {
		t.Fatalf("fixture active/retired counts = %d/%d, decoded %d/%d", fixture.Oracle.ActiveCount, fixture.Oracle.RetiredCount, activeCount, retiredCount)
	}
	return fixture
}

func decodePrivateSupported(value string) (Supported, error) {
	switch value {
	case "yes":
		return SupportedYes, nil
	case "no":
		return SupportedNo, nil
	default:
		return SupportedNo, fmt.Errorf("unknown supported value %q", value)
	}
}

func decodePrivateVote(value string) (VoteBehavior, error) {
	switch value {
	case "default_yes":
		return VoteDefaultYes, nil
	case "default_no":
		return VoteDefaultNo, nil
	case "obsolete":
		return VoteObsolete, nil
	default:
		return VoteDefaultNo, fmt.Errorf("unknown vote value %q", value)
	}
}

func privateFeatureIDHex(name string) string {
	id := FeatureID(name)
	return strings.ToUpper(hex.EncodeToString(id[:]))
}

func TestPrivateV341FeatureRegistry(t *testing.T) {
	fixture := loadPrivateV341Fixture(t)

	privateNames := make(map[string]privateV341Registration, len(fixture.Registrations))
	for _, expected := range fixture.Registrations {
		if _, duplicate := privateNames[expected.Name]; duplicate {
			t.Fatalf("fixture contains duplicate feature %q", expected.Name)
		}
		privateNames[expected.Name] = expected

		wantName := expected.SourceName
		if expected.Kind == "fix" {
			wantName = "fix" + wantName
		} else if expected.Kind != "feature" {
			t.Fatalf("feature %q has unknown kind %q", expected.Name, expected.Kind)
		}
		if expected.Name != wantName {
			t.Errorf("source registration %q normalized to %q, fixture name is %q", expected.SourceName, wantName, expected.Name)
		}
		if got := privateFeatureIDHex(expected.Name); got != expected.ID {
			t.Errorf("feature %q ID = %s, fixture has %s", expected.Name, got, expected.ID)
		}
		wantSupported, err := decodePrivateSupported(expected.Supported)
		if err != nil {
			t.Fatal(err)
		}
		wantVote, err := decodePrivateVote(expected.Vote)
		if err != nil {
			t.Fatal(err)
		}
		feature := FeatureByName(expected.Name)
		if feature == nil {
			t.Errorf("private v3.4.1 feature %q is missing from the Go registry", expected.Name)
			continue
		}
		if feature.ID != FeatureID(expected.Name) {
			t.Errorf("Go feature %q stores an ID different from its name", expected.Name)
		}
		if expected.Name == "ConfidentialTransfer" {
			wantSupported = SupportedNo
			if mptcrypto.Available() {
				wantSupported = SupportedYes
			}
		}
		if feature.Supported != wantSupported {
			t.Errorf("feature %q support = %v, want %v (native backend available=%v)", expected.Name, feature.Supported, wantSupported, mptcrypto.Available())
		}
		if feature.Vote != wantVote {
			t.Errorf("feature %q vote = %v, want %v", expected.Name, feature.Vote, wantVote)
		}
		if feature.Retired != expected.Retired {
			t.Errorf("feature %q retired = %v, want %v", expected.Name, feature.Retired, expected.Retired)
		}
	}

	registered := make(map[string]*Feature, len(AllFeatures()))
	for _, feature := range AllFeatures() {
		if _, duplicate := registered[feature.Name]; duplicate {
			t.Fatalf("Go registry contains duplicate feature %q", feature.Name)
		}
		registered[feature.Name] = feature
	}
	for _, extra := range privateV341GoOnlyFeatures {
		if _, found := privateNames[extra.name]; found {
			t.Fatalf("Go-only feature %q unexpectedly occurs in private fixture", extra.name)
		}
		feature := registered[extra.name]
		if feature == nil {
			t.Errorf("intentional Go-only feature %q is missing", extra.name)
			continue
		}
		if feature.ID != FeatureID(extra.name) || feature.Supported != extra.supported || feature.Vote != extra.vote || feature.Retired != extra.retired {
			t.Errorf("Go-only feature %q = (%X, %v, %v, retired=%v), want (%X, %v, %v, retired=%v)", extra.name, feature.ID, feature.Supported, feature.Vote, feature.Retired, FeatureID(extra.name), extra.supported, extra.vote, extra.retired)
		}
	}
	if want := len(privateNames) + len(privateV341GoOnlyFeatures); len(registered) != want {
		t.Fatalf("Go registry has %d features, want %d private registrations plus four intentional entries", len(registered), want)
	}
	for name := range registered {
		if _, inPrivate := privateNames[name]; inPrivate {
			continue
		}
		foundExtra := false
		for _, extra := range privateV341GoOnlyFeatures {
			if extra.name == name {
				foundExtra = true
				break
			}
		}
		if !foundExtra {
			t.Errorf("Go registry feature %q is neither pinned in private v3.4.1 nor an intentional extra", name)
		}
	}
}

func TestPrivateV341DefaultVotingAcrossProfiles(t *testing.T) {
	fixture := loadPrivateV341Fixture(t)

	wantDefaultYes := make(map[string]bool)
	for _, expected := range fixture.Registrations {
		if expected.Vote == "default_yes" && !expected.Retired {
			wantDefaultYes[expected.Name] = true
		}
	}
	gotDefaultYes := make(map[string]bool)
	for _, feature := range DefaultYesFeatures() {
		gotDefaultYes[feature.Name] = true
	}
	if len(gotDefaultYes) != len(wantDefaultYes) {
		t.Fatalf("Go default-yes feature count = %d, private v3.4.1 count = %d", len(gotDefaultYes), len(wantDefaultYes))
	}
	for name := range wantDefaultYes {
		if !gotDefaultYes[name] {
			t.Errorf("private default-yes feature %q is absent from Go defaults", name)
		}
	}

	genesis := GenesisRules()
	genesisIDs := make(map[[32]byte]bool)
	for _, id := range genesis.EnabledIDs() {
		genesisIDs[id] = true
	}
	desired := make(map[[32]byte]bool)
	for _, id := range NewTable().Desired() {
		desired[id] = true
	}
	for _, feature := range AllFeatures() {
		wantGenesis := feature.Retired || (feature.Supported == SupportedYes && feature.Vote == VoteDefaultYes)
		if genesisIDs[feature.ID] != wantGenesis {
			t.Errorf("feature %q genesis ID enabled = %v, want %v", feature.Name, genesisIDs[feature.ID], wantGenesis)
		}
		wantDesired := feature.Supported == SupportedYes && feature.Vote == VoteDefaultYes && !feature.Retired
		if desired[feature.ID] != wantDesired {
			t.Errorf("feature %q default desired = %v, want %v", feature.Name, desired[feature.ID], wantDesired)
		}
	}

	confidential := FeatureByName("ConfidentialTransfer")
	if confidential == nil || confidential.Vote != VoteDefaultNo {
		t.Fatalf("ConfidentialTransfer must remain registered with VoteDefaultNo in every profile")
	}
	if got, want := confidential.Supported == SupportedYes, mptcrypto.Available(); got != want {
		t.Fatalf("ConfidentialTransfer support = %v, want backend availability %v", got, want)
	}
}
