package amendment

import (
	"fmt"
	"testing"
)

func TestRC1AmendmentsRemainStaged(t *testing.T) {
	for name, id := range map[string]string{
		"fixCleanup3_4_0":     "98433DD001A5737F773D74F8CA2A25A065089C73B2E611C760BAF369E4FECA76",
		"LendingProtocolV1_1": "A360E2BFD775A5B0DCE1C36C16DF31B72735A57584FD163655D2F9564F8E7AC8",
	} {
		feature := FeatureByName(name)
		if feature == nil {
			t.Fatalf("missing %s", name)
		}
		if fmt.Sprintf("%X", feature.ID) != id || feature.Supported != SupportedNo || feature.Vote != VoteDefaultNo || AllSupportedRules().Enabled(feature.ID) {
			t.Errorf("incorrect staging for %s: %+v", name, feature)
		}
	}
}
